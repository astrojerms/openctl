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
// the provider call. Equivalent to SaveWithRefsHash(ctx, r, "") — pre-Phase-8
// callers.
func (s *Store) Save(ctx context.Context, r *protocol.Resource) error {
	return s.SaveWithRefsHash(ctx, r, "")
}

// SaveWithRefsHash is Save plus an explicit refs_hash for the
// verifying-trace cache's ref-invalidation dimension. Phase 8 step 5:
// the dispatcher hashes the raw (pre-resolution) manifest into
// input_hash and the resolved-ref values into refs_hash; both must
// match on the next apply for a cache hit. Non-dispatcher callers
// (disk mirror, GitOps watcher) pass refsHash="" — those rows always
// miss the ref-hash check, so the dispatcher will re-verify on first
// live apply.
func (s *Store) SaveWithRefsHash(ctx context.Context, r *protocol.Resource, refsHash string) error {
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
	// SQLite treats "" as NULL-adjacent for this workflow — we store
	// the empty string when no refs_hash is supplied, and read it back
	// via COALESCE. This keeps the schema uniform without requiring
	// callers to plumb sql.NullString values.
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO applied_manifests (api_version, kind, name, spec_json, metadata_json, applied_at, input_hash, refs_hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(api_version, kind, name) DO UPDATE SET
		   spec_json = excluded.spec_json,
		   metadata_json = excluded.metadata_json,
		   applied_at = excluded.applied_at,
		   input_hash = excluded.input_hash,
		   refs_hash = excluded.refs_hash`,
		r.APIVersion, r.Kind, r.Metadata.Name,
		string(specJSON), string(metaJSON),
		time.Now().UTC().Format(time.RFC3339Nano),
		Hash(r), refsHash,
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
	inputHash, _, err := s.LoadHashes(ctx, apiVersion, kind, name)
	return inputHash, err
}

// LoadHashes returns both stored verifying-trace hashes for a resource:
// input_hash (raw manifest hash — user intent) and refs_hash (resolved
// $ref values hash — upstream state). Either or both may be "" for
// resources saved by pre-Phase-8 callers. Returns ("","",nil) when no
// row exists.
func (s *Store) LoadHashes(ctx context.Context, apiVersion, kind, name string) (inputHash, refsHash string, err error) {
	var iHash, rHash sql.NullString
	err = s.db.QueryRowContext(ctx,
		`SELECT COALESCE(input_hash, ''), COALESCE(refs_hash, '')
		 FROM applied_manifests
		 WHERE api_version = ? AND kind = ? AND name = ?`,
		apiVersion, kind, name,
	).Scan(&iHash, &rHash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("load hashes: %w", err)
	}
	return iHash.String, rHash.String, nil
}

// Load returns the most recent applied manifest spec for the resource, or
// (nil, nil) if none has been recorded.
func (s *Store) Load(ctx context.Context, apiVersion, kind, name string) (*protocol.Resource, error) {
	r, _, err := s.LoadWithTime(ctx, apiVersion, kind, name)
	return r, err
}

// LoadWithTime is Load plus the applied_at timestamp. Returns (nil, zero,
// nil) when no manifest is on file. Used by Get RPC handlers that want
// to surface "last applied" to clients without a second round-trip.
func (s *Store) LoadWithTime(ctx context.Context, apiVersion, kind, name string) (*protocol.Resource, time.Time, error) {
	var specJSON, metaJSON, appliedAt sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT spec_json, COALESCE(metadata_json, ''), COALESCE(applied_at, '')
		 FROM applied_manifests
		 WHERE api_version = ? AND kind = ? AND name = ?`,
		apiVersion, kind, name,
	).Scan(&specJSON, &metaJSON, &appliedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("load applied_manifest: %w", err)
	}
	r := &protocol.Resource{
		APIVersion: apiVersion,
		Kind:       kind,
		Metadata:   protocol.ResourceMetadata{Name: name},
	}
	if specJSON.Valid && specJSON.String != "" && specJSON.String != "null" {
		if err := json.Unmarshal([]byte(specJSON.String), &r.Spec); err != nil {
			return nil, time.Time{}, fmt.Errorf("decode spec: %w", err)
		}
	}
	if metaJSON.Valid && metaJSON.String != "" && metaJSON.String != "null" {
		var md protocol.ResourceMetadata
		if err := json.Unmarshal([]byte(metaJSON.String), &md); err == nil {
			r.Metadata = md
		}
	}
	var ts time.Time
	if appliedAt.Valid && appliedAt.String != "" {
		// Tolerate RFC3339 with and without sub-second precision; the writer
		// uses RFC3339Nano but older rows may have second-resolution stamps.
		if t, parseErr := time.Parse(time.RFC3339Nano, appliedAt.String); parseErr == nil {
			ts = t
		} else if t, parseErr := time.Parse(time.RFC3339, appliedAt.String); parseErr == nil {
			ts = t
		}
	}
	return r, ts, nil
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

// ListNames returns the set of resource names that have an applied manifest
// for (apiVersion, kind). Cheap: doesn't decode spec/metadata. Used by the
// managed-only filter in ResourceService.List so observed-but-unmanaged
// resources are hidden in one DB round-trip per List call.
func (s *Store) ListNames(ctx context.Context, apiVersion, kind string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name FROM applied_manifests WHERE api_version = ? AND kind = ?`,
		apiVersion, kind)
	if err != nil {
		return nil, fmt.Errorf("list names: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
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
