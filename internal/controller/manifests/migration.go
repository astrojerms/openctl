package manifests

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/openctl/openctl/internal/controller/refs"
	"github.com/openctl/openctl/pkg/protocol"
)

var (
	ErrMigrationBusy        = errors.New("controller has pending or running operations; wait for them to finish before migration")
	ErrMigrationConflict    = errors.New("migration conflicts with current controller intent")
	ErrMigrationUnsupported = errors.New("migration requires a standalone Proxmox VirtualMachine without provider state or resource references")
	ErrMigrationNotFound    = errors.New("resource has no managed desired manifest")
)

// ForgottenError fences all ordinary submissions after ownership is released.
// Explicit adoption is the only operation that clears the fence.
type ForgottenError struct {
	APIVersion string
	Kind       string
	Name       string
}

func (e *ForgottenError) Error() string {
	return fmt.Sprintf("%s %q was forgotten by this controller; use ctl adopt to reclaim management", e.Kind, e.Name)
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// CheckNotForgotten works with a DB or transaction. Call inside the operation
// admission transaction to serialize release against new queued work.
func CheckNotForgotten(ctx context.Context, q rowQuerier, av, kind, name string) error {
	var released bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM resource_releases WHERE api_version = ? AND kind = ? AND name = ?)`, av, kind, name).Scan(&released); err != nil {
		return fmt.Errorf("check resource release: %w", err)
	}
	if released {
		return &ForgottenError{APIVersion: av, Kind: kind, Name: name}
	}
	return nil
}

func (s *Store) IsForgotten(ctx context.Context, av, kind, name string) (bool, error) {
	err := CheckNotForgotten(ctx, s.db, av, kind, name)
	var forgotten *ForgottenError
	if errors.As(err, &forgotten) {
		return true, nil
	}
	return false, err
}

// MigrationDigest covers the entire desired manifest, including annotations.
// Unlike the apply cache hash, changing automation/ownership annotations must
// invalidate an exported handoff. Observed status is not desired intent.
func MigrationDigest(r *protocol.Resource) (string, error) {
	if r == nil {
		return "", fmt.Errorf("migration manifest is required")
	}
	desired := *r
	desired.Status = nil
	data, err := json.Marshal(&desired)
	if err != nil {
		return "", fmt.Errorf("encode migration manifest: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

type MigrationSnapshot struct {
	ControllerID string
	Digest       string
	Resource     *protocol.Resource
}

// ExportMigration snapshots only one resource's desired intent. No operations,
// authentication material, or unrelated resources leave the controller.
func (s *Store) ExportMigration(ctx context.Context, av, kind, name string) (*MigrationSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := migrationIdle(ctx, tx); err != nil {
		return nil, err
	}
	if err := CheckNotForgotten(ctx, tx, av, kind, name); err != nil {
		return nil, err
	}
	r, err := loadMigrationManifest(ctx, tx, av, kind, name)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, ErrMigrationNotFound
	}
	if err := migrationStandalone(ctx, tx, r); err != nil {
		return nil, err
	}
	id, err := migrationControllerID(ctx, tx)
	if err != nil {
		return nil, err
	}
	digest, err := MigrationDigest(r)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &MigrationSnapshot{ControllerID: id, Digest: digest, Resource: r}, nil
}

// Adopt records desired intent without applying it. The RPC verifies the live
// provider identity before this transaction; this method never calls a provider.
func (s *Store) Adopt(ctx context.Context, r *protocol.Resource) error {
	if err := validateMigrationResource(r); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := migrationIdle(ctx, tx); err != nil {
		return err
	}
	if err := migrationStandalone(ctx, tx, r); err != nil {
		return err
	}
	old, err := loadMigrationManifest(ctx, tx, r.APIVersion, r.Kind, r.Metadata.Name)
	if err != nil {
		return err
	}
	if old != nil {
		oldDigest, err := MigrationDigest(old)
		if err != nil {
			return err
		}
		newDigest, err := MigrationDigest(r)
		if err != nil {
			return err
		}
		if oldDigest != newDigest {
			return fmt.Errorf("%w: destination already manages a different manifest for %s %q", ErrMigrationConflict, r.Kind, r.Metadata.Name)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM resource_releases WHERE api_version = ? AND kind = ? AND name = ?`, r.APIVersion, r.Kind, r.Metadata.Name); err != nil {
		return err
	}
	if err := saveManifest(ctx, tx, r, "", SourceCLI); err != nil {
		return err
	}
	return tx.Commit()
}

// Forget atomically removes only desired intent and installs a persistent
// release fence. History is retained. A stale/wrong-controller bundle cannot
// relinquish newer intent. The same successful release is retryable.
func (s *Store) Forget(ctx context.Context, r *protocol.Resource, controllerID, digest string) error {
	if err := validateMigrationResource(r); err != nil {
		return err
	}
	actualDigest, err := MigrationDigest(r)
	if err != nil {
		return err
	}
	if controllerID == "" || digest == "" || digest != actualDigest {
		return fmt.Errorf("%w: invalid bundle manifest digest or source controller", ErrMigrationConflict)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := migrationIdle(ctx, tx); err != nil {
		return err
	}
	id, err := migrationControllerID(ctx, tx)
	if err != nil {
		return err
	}
	if id != controllerID {
		return fmt.Errorf("%w: bundle belongs to another source controller", ErrMigrationConflict)
	}
	old, err := loadMigrationManifest(ctx, tx, r.APIVersion, r.Kind, r.Metadata.Name)
	if err != nil {
		return err
	}
	if old == nil {
		var prior string
		err := tx.QueryRowContext(ctx, `SELECT manifest_digest FROM resource_releases WHERE api_version = ? AND kind = ? AND name = ?`, r.APIVersion, r.Kind, r.Metadata.Name).Scan(&prior)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMigrationNotFound
		}
		if err != nil {
			return err
		}
		if prior != digest {
			return fmt.Errorf("%w: release belongs to a different export", ErrMigrationConflict)
		}
		return tx.Commit()
	}
	currentDigest, err := MigrationDigest(old)
	if err != nil {
		return err
	}
	if currentDigest != digest {
		return fmt.Errorf("%w: source manifest changed after export; export it again", ErrMigrationConflict)
	}
	if err := migrationStandalone(ctx, tx, old); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO resource_releases (api_version, kind, name, manifest_digest, released_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(api_version, kind, name) DO UPDATE SET manifest_digest = excluded.manifest_digest, released_at = excluded.released_at`, r.APIVersion, r.Kind, r.Metadata.Name, digest, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM applied_manifests WHERE api_version = ? AND kind = ? AND name = ?`, r.APIVersion, r.Kind, r.Metadata.Name); err != nil {
		return err
	}
	return tx.Commit()
}

func validateMigrationResource(r *protocol.Resource) error {
	if r == nil || r.APIVersion != "proxmox.openctl.io/v1" || r.Kind != "VirtualMachine" || r.Metadata.Name == "" {
		return ErrMigrationUnsupported
	}
	if r.Metadata.Labels["openctl.io/owner-kind"] != "" || r.Metadata.Labels["openctl.io/owner-name"] != "" || r.Metadata.Labels["openctl.io/cluster"] != "" || len(refs.Collect(r.Spec)) != 0 {
		return ErrMigrationUnsupported
	}
	return nil
}

func migrationIdle(ctx context.Context, tx *sql.Tx) error {
	var busy bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM operations WHERE status IN ('pending', 'running'))`).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return ErrMigrationBusy
	}
	return nil
}

func migrationStandalone(ctx context.Context, tx *sql.Tx, r *protocol.Resource) error {
	if err := validateMigrationResource(r); err != nil {
		return err
	}
	var stateful bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM provider_state WHERE api_version = ? AND kind = ? AND name = ?)`, r.APIVersion, r.Kind, r.Metadata.Name).Scan(&stateful); err != nil {
		return err
	}
	if stateful {
		return fmt.Errorf("%w: opaque provider state is present", ErrMigrationUnsupported)
	}
	rows, err := tx.QueryContext(ctx, `SELECT api_version, kind, name, spec_json, COALESCE(metadata_json, '') FROM applied_manifests`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var av, kind, name, specJSON, metaJSON string
		if err := rows.Scan(&av, &kind, &name, &specJSON, &metaJSON); err != nil {
			return err
		}
		if av == r.APIVersion && kind == r.Kind && name == r.Metadata.Name {
			continue
		}
		var spec map[string]any
		if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
			return fmt.Errorf("read managed resource references: %w", err)
		}
		for _, ref := range refs.Collect(spec) {
			if ref.APIVersion == r.APIVersion && ref.Kind == r.Kind && ref.Name == r.Metadata.Name {
				return fmt.Errorf("%w: %s %q references this VM", ErrMigrationUnsupported, kind, name)
			}
		}
		if metaJSON != "" {
			var meta protocol.ResourceMetadata
			if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
				return fmt.Errorf("read managed ownership: %w", err)
			}
			if meta.Labels["openctl.io/owner-kind"] == r.Kind && meta.Labels["openctl.io/owner-name"] == r.Metadata.Name {
				return fmt.Errorf("%w: %s %q is owned by this VM", ErrMigrationUnsupported, kind, name)
			}
		}
	}
	return rows.Err()
}

func loadMigrationManifest(ctx context.Context, tx *sql.Tx, av, kind, name string) (*protocol.Resource, error) {
	var specJSON, metaJSON string
	err := tx.QueryRowContext(ctx, `SELECT spec_json, COALESCE(metadata_json, '') FROM applied_manifests WHERE api_version = ? AND kind = ? AND name = ?`, av, kind, name).Scan(&specJSON, &metaJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r := &protocol.Resource{APIVersion: av, Kind: kind, Metadata: protocol.ResourceMetadata{Name: name}}
	if err := json.Unmarshal([]byte(specJSON), &r.Spec); err != nil {
		return nil, fmt.Errorf("decode desired spec: %w", err)
	}
	if metaJSON != "" {
		if err := json.Unmarshal([]byte(metaJSON), &r.Metadata); err != nil {
			return nil, fmt.Errorf("decode desired metadata: %w", err)
		}
	}
	return r, nil
}

func migrationControllerID(ctx context.Context, tx *sql.Tx) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT value FROM controller_meta WHERE key = 'migration_controller_id'`).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	id = hex.EncodeToString(raw[:])
	if _, err := tx.ExecContext(ctx, `INSERT INTO controller_meta (key, value) VALUES ('migration_controller_id', ?)`, id); err != nil {
		return "", err
	}
	return id, nil
}
