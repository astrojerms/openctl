// Package manifests is the controller's persisted "desired state" store.
// On each successful apply, the dispatcher saves the manifest's spec here;
// on each successful delete, it removes the row. Get RPCs read from this
// store and diff against observed state to surface drift.
//
// Single row per (apiVersion, kind, name). Apply overwrites; Delete removes.
// The store is intentionally narrow — it doesn't track history (operations
// already does that) and doesn't track relationships (provider state does).
package manifests

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/openctl/openctl/pkg/protocol"
)

// Store is the desired-state data layer. Backed by SQLite via the *sql.DB
// passed to New.
type Store struct {
	db *sql.DB
}

// New constructs a Store.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// Hash delegates to the package-level Hash function. Exposed as a method
// so callers that hold a *Store (the dispatcher's ManifestSink) can compute
// hashes without importing the manifests package directly.
func (s *Store) Hash(r *protocol.Resource) string {
	return Hash(r)
}

// Save upserts the manifest's spec for (apiVersion, kind, name). Called by
// the dispatcher after a successful apply. Also stores the verifying-trace
// input hash so subsequent applies of the same manifest can short-circuit
// the provider call.
func (s *Store) Save(ctx context.Context, r *protocol.Resource) error {
	if r == nil || r.APIVersion == "" || r.Kind == "" || r.Metadata.Name == "" {
		return fmt.Errorf("save: apiVersion, kind, metadata.name all required")
	}
	specJSON, err := json.Marshal(r.Spec)
	if err != nil {
		return fmt.Errorf("encode spec: %w", err)
	}
	metaJSON, err := json.Marshal(r.Metadata)
	if err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO applied_manifests (api_version, kind, name, spec_json, metadata_json, applied_at, input_hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(api_version, kind, name) DO UPDATE SET
		   spec_json = excluded.spec_json,
		   metadata_json = excluded.metadata_json,
		   applied_at = excluded.applied_at,
		   input_hash = excluded.input_hash`,
		r.APIVersion, r.Kind, r.Metadata.Name,
		string(specJSON), string(metaJSON),
		time.Now().UTC().Format(time.RFC3339Nano),
		Hash(r),
	)
	if err != nil {
		return fmt.Errorf("upsert applied_manifest: %w", err)
	}
	return nil
}

// LoadHash returns the stored verifying-trace input hash for a resource,
// or "" if no manifest is on file (cache miss). Cheaper than Load — used
// on the hot path before every Apply to decide whether to call the
// provider at all.
func (s *Store) LoadHash(ctx context.Context, apiVersion, kind, name string) (string, error) {
	var hash sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(input_hash, '')
		 FROM applied_manifests
		 WHERE api_version = ? AND kind = ? AND name = ?`,
		apiVersion, kind, name,
	).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load input_hash: %w", err)
	}
	return hash.String, nil
}

// Load returns the most recent applied manifest spec for the resource, or
// (nil, nil) if none has been recorded.
func (s *Store) Load(ctx context.Context, apiVersion, kind, name string) (*protocol.Resource, error) {
	var specJSON, metaJSON sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT spec_json, COALESCE(metadata_json, '')
		 FROM applied_manifests
		 WHERE api_version = ? AND kind = ? AND name = ?`,
		apiVersion, kind, name,
	).Scan(&specJSON, &metaJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load applied_manifest: %w", err)
	}
	r := &protocol.Resource{
		APIVersion: apiVersion,
		Kind:       kind,
		Metadata:   protocol.ResourceMetadata{Name: name},
	}
	if specJSON.Valid && specJSON.String != "" && specJSON.String != "null" {
		if err := json.Unmarshal([]byte(specJSON.String), &r.Spec); err != nil {
			return nil, fmt.Errorf("decode spec: %w", err)
		}
	}
	if metaJSON.Valid && metaJSON.String != "" && metaJSON.String != "null" {
		var md protocol.ResourceMetadata
		if err := json.Unmarshal([]byte(metaJSON.String), &md); err == nil {
			r.Metadata = md
		}
	}
	return r, nil
}

// Ref is a (apiVersion, kind, name) tuple. Returned by ListAll for callers
// that need to enumerate the set of applied manifests without loading every
// spec — e.g. startup reconciliation against the disk mirror.
type Ref struct {
	APIVersion string
	Kind       string
	Name       string
}

// ListAll returns every applied manifest's identity, oldest first by
// applied_at. Cheap: doesn't decode spec/metadata. Used by the disk mirror's
// startup reconciliation to compare against what's on disk.
func (s *Store) ListAll(ctx context.Context) ([]Ref, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT api_version, kind, name FROM applied_manifests ORDER BY applied_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list applied_manifests: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Ref
	for rows.Next() {
		var r Ref
		if err := rows.Scan(&r.APIVersion, &r.Kind, &r.Name); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Delete removes the row for a resource. Idempotent — delete on missing
// returns nil, matching the provider Delete contract.
func (s *Store) Delete(ctx context.Context, apiVersion, kind, name string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM applied_manifests WHERE api_version = ? AND kind = ? AND name = ?`,
		apiVersion, kind, name,
	)
	return err
}
