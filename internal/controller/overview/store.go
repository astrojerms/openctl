// Package overview reads persisted control-plane metadata, never provider state.
package overview

import (
	"context"
	"database/sql"
	"fmt"
)

const (
	DefaultOperationLimit = 10
	MaxOperationLimit = 100
)

// Store shares the controller database without resolving providers or secrets.
type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// KindCount includes every saved desired record, including owned resources and
// kinds whose providers are no longer registered. Discovery alone adds no rows.
type KindCount struct {
	APIVersion string
	Kind string
	Resources uint32
}

// OperationMetadata deliberately cannot carry manifests, results, labels or
// errors. Those fields can contain credentials even when they are not JSON.
type OperationMetadata struct {
	ID string
	Type string
	APIVersion string
	Kind string
	ResourceName string
	Status string
	SubmittedAt string
	StartedAt string
	CompletedAt string
	Source string
}

// Snapshot describes retained control-plane records, not infrastructure health.
// ReviewResources counts resources whose latest retained root admission ended
// unsuccessfully; a newer successful or active admission supersedes it.
type Snapshot struct {
	AsOf string
	ManagedResources uint32
	Kinds []KindCount
	PendingOperations uint32
	RunningOperations uint32
	ReviewResources uint32
	ActiveOperations []OperationMetadata
	ReviewOperations []OperationMetadata
}

const rootPredicate = `(parent_id IS NULL OR parent_id = '') AND type IN ('apply', 'delete')`
const reviewPredicate = `status IN ('failed', 'interrupted', 'canceled') AND rowid IN (
	SELECT MAX(rowid) FROM operations WHERE ` + rootPredicate + `
	GROUP BY api_version, kind, resource_name
)`
const metadataColumns = `id, type, api_version, kind, resource_name, status,
	submitted_at, COALESCE(started_at, ''), COALESCE(completed_at, ''), COALESCE(source, '')`

// Snapshot reads every total and bounded list in a single read-only SQLite
// transaction. Admission order is rowid, not caller/wall-clock timestamps.
func (s *Store) Snapshot(ctx context.Context, operationLimit uint32) (*Snapshot, error) {
	if operationLimit == 0 {
		operationLimit = DefaultOperationLimit
	}
	if operationLimit > MaxOperationLimit {
		return nil, fmt.Errorf("operation limit exceeds %d", MaxOperationLimit)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin overview snapshot: %w", err)
	}
	defer tx.Rollback()

	out := &Snapshot{}
	// This first table read pins the snapshot. Its clock value identifies the
	// read, not the last provider observation or a health-check timestamp.
	if err := tx.QueryRowContext(ctx, `SELECT strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), COUNT(*) FROM applied_manifests`).Scan(&out.AsOf, &out.ManagedResources); err != nil {
		return nil, fmt.Errorf("read overview inventory: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT api_version, kind, COUNT(*) FROM applied_manifests GROUP BY api_version, kind ORDER BY api_version, kind`)
	if err != nil {
		return nil, fmt.Errorf("read overview kinds: %w", err)
	}
	out.Kinds, err = readKinds(rows)
	if err != nil {
		return nil, fmt.Errorf("read overview kinds: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(CASE WHEN status = 'pending' THEN 1 END), COUNT(CASE WHEN status = 'running' THEN 1 END) FROM operations WHERE ` + rootPredicate).Scan(&out.PendingOperations, &out.RunningOperations); err != nil {
		return nil, fmt.Errorf("read overview active totals: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM operations WHERE ` + reviewPredicate).Scan(&out.ReviewResources); err != nil {
		return nil, fmt.Errorf("read overview review total: %w", err)
	}
	out.ActiveOperations, err = readOperations(ctx, tx, rootPredicate + ` AND status IN ('pending', 'running')`, operationLimit)
	if err != nil {
		return nil, fmt.Errorf("read overview active operations: %w", err)
	}
	out.ReviewOperations, err = readOperations(ctx, tx, reviewPredicate, operationLimit)
	if err != nil {
		return nil, fmt.Errorf("read overview review operations: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("finish overview snapshot: %w", err)
	}
	return out, nil
}

func readKinds(rows *sql.Rows) ([]KindCount, error) {
	defer rows.Close()
	var out []KindCount
	for rows.Next() {
		var kind KindCount
		if err := rows.Scan(&kind.APIVersion, &kind.Kind, &kind.Resources); err != nil {
			return nil, err
		}
		out = append(out, kind)
	}
	return out, rows.Err()
}

func readOperations(ctx context.Context, tx *sql.Tx, predicate string, limit uint32) ([]OperationMetadata, error) {
	rows, err := tx.QueryContext(ctx, `SELECT ` + metadataColumns + ` FROM operations WHERE ` + predicate + ` ORDER BY rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OperationMetadata
	for rows.Next() {
		var op OperationMetadata
		if err := rows.Scan(&op.ID, &op.Type, &op.APIVersion, &op.Kind, &op.ResourceName, &op.Status, &op.SubmittedAt, &op.StartedAt, &op.CompletedAt, &op.Source); err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}
