// Package provideridentity stores immutable instance bindings, separately from
// desired intent and opaque plugin state. Bindings are never inferred by name.
package provideridentity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type Identity struct {
	Context  string
	Endpoint string
	Node     string
	VMID     int
	Identity string
}

type Store struct{ db *sql.DB }

func New(db *sql.DB) *Store { return &Store{db: db} }

type identityDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) LoadIdentity(ctx context.Context, name string) (*Identity, error) {
	return loadIdentity(ctx, s.db, name)
}

func loadIdentity(ctx context.Context, db identityDB, name string) (*Identity, error) {
	var id Identity
	err := db.QueryRowContext(ctx, `SELECT context, endpoint, node, vmid, identity FROM provider_identities WHERE name = ?`, name).Scan(&id.Context, &id.Endpoint, &id.Node, &id.VMID, &id.Identity)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// SaveIdentity is compare-and-set: ordinary operations cannot replace a binding.
func (s *Store) SaveIdentity(ctx context.Context, name string, id *Identity) error {
	return saveIdentity(ctx, s.db, name, id)
}

func saveIdentity(ctx context.Context, db identityDB, name string, id *Identity) error {
	if name == "" || id == nil || id.Endpoint == "" || id.Node == "" || id.VMID <= 0 || id.Identity == "" {
		return fmt.Errorf("incomplete VM identity")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO provider_identities(name, context, endpoint, node, vmid, identity) VALUES(?, ?, ?, ?, ?, ?) ON CONFLICT(name) DO NOTHING`, name, id.Context, id.Endpoint, id.Node, id.VMID, id.Identity); err != nil {
		return err
	}
	stored, err := loadIdentity(ctx, db, name)
	if err != nil {
		return err
	}
	if stored == nil || *stored != *id {
		return fmt.Errorf("VM %q already has a different immutable identity", name)
	}
	return nil
}

// ConfirmIdentity fences legacy identity recovery against queued/running work.
func (s *Store) ConfirmIdentity(ctx context.Context, name string, id *Identity) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var busy bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM operations WHERE status IN ('pending', 'running'))`).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return fmt.Errorf("identity confirmation requires an idle controller")
	}
	if err := saveIdentity(ctx, tx, name, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteIdentity(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM provider_identities WHERE name = ?`, name)
	return err
}
