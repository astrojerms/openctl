// Package providerstate is the controller's opaque per-resource provider-state
// store — the "observed provider state" backend from
// docs/plugin-architecture.md, distinct from the desired-state manifests store.
//
// A stateful provider (a stateful external plugin today; the Terraform/OpenTofu
// host later) hands back an opaque blob after each Apply/Read and expects to
// see it again on the next call. openctl stores it verbatim and never parses
// it. Because the store is per-resource and opaque, the painful properties of
// a monolithic tfstate file — global lock, whole-world blob, state surgery —
// structurally don't apply: each resource's blob is independent, keyed exactly
// like applied_manifests.
package providerstate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Store is the provider-state data layer, backed by SQLite via the *sql.DB
// passed to New.
type Store struct {
	db *sql.DB
}

// New constructs a Store.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// LoadState returns the stored opaque state/private blobs and schema version
// for a resource. When no row exists it returns (nil, nil, 0, nil) — a genuine
// miss is not an error, matching the "first apply has no prior state" case.
func (s *Store) LoadState(ctx context.Context, apiVersion, kind, name string) (state, private []byte, schemaVersion int, err error) {
	var st, pv []byte
	var sv int
	err = s.db.QueryRowContext(ctx,
		`SELECT state_blob, private_blob, schema_version
		 FROM provider_state
		 WHERE api_version = ? AND kind = ? AND name = ?`,
		apiVersion, kind, name,
	).Scan(&st, &pv, &sv)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, 0, nil
	}
	if err != nil {
		return nil, nil, 0, fmt.Errorf("load provider_state: %w", err)
	}
	return st, pv, sv, nil
}

// SaveState upserts the opaque state/private blobs for a resource. Called
// after a successful Apply (and, for providers that refresh, after Get). nil
// blobs are stored as SQL NULL.
func (s *Store) SaveState(ctx context.Context, apiVersion, kind, name string, state, private []byte, schemaVersion int) error {
	if apiVersion == "" || kind == "" || name == "" {
		return fmt.Errorf("save provider_state: apiVersion, kind, name all required")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO provider_state (api_version, kind, name, state_blob, private_blob, schema_version, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(api_version, kind, name) DO UPDATE SET
		   state_blob = excluded.state_blob,
		   private_blob = excluded.private_blob,
		   schema_version = excluded.schema_version,
		   updated_at = excluded.updated_at`,
		apiVersion, kind, name,
		nilIfEmpty(state), nilIfEmpty(private), schemaVersion,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("upsert provider_state: %w", err)
	}
	return nil
}

// DeleteState removes a resource's state row. Idempotent — delete-on-missing
// returns nil, matching the provider Delete contract.
func (s *Store) DeleteState(ctx context.Context, apiVersion, kind, name string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM provider_state WHERE api_version = ? AND kind = ? AND name = ?`,
		apiVersion, kind, name,
	)
	if err != nil {
		return fmt.Errorf("delete provider_state: %w", err)
	}
	return nil
}

// nilIfEmpty maps an empty (or nil) blob to a nil interface so it lands in
// SQLite as NULL rather than an empty BLOB, keeping "no state" unambiguous.
func nilIfEmpty(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
