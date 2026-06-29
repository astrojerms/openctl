// Package operations is the controller's persisted async-operation store.
// Apply/Delete RPCs insert pending ops here; the dispatcher (also in this
// package) picks them up, runs them via providers, and updates status. On
// startup, any operation still marked `running` is rewritten as
// `interrupted` so users know the previous controller died mid-op.
//
// See CONTROLLER.md "Operation model" for the locked architectural
// decisions this implements (persisted, parent/child, fail-fast on same
// resource, GC on apply, no auto-resume).
package operations

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Op type values. Kept as string constants so the wire format and the SQL
// value match.
//
// "apply" and "delete" are the dispatched top-level operation types. "step"
// is used by Phase 4.5 child rows for sub-operations that don't correspond
// to a real resource apply (e.g. "install-k3s") — they exist purely to
// surface substep progress under a parent.
const (
	TypeApply  = "apply"
	TypeDelete = "delete"
	TypeStep   = "step"
)

// Status values for the op lifecycle.
const (
	StatusPending     = "pending"
	StatusRunning     = "running"
	StatusSucceeded   = "succeeded"
	StatusFailed      = "failed"
	StatusInterrupted = "interrupted"
)

// Operation is the in-process representation of a row in the operations
// table. Times are RFC3339 strings; empty means unset.
type Operation struct {
	ID           string
	ParentID     string
	Type         string
	APIVersion   string
	Kind         string
	ResourceName string
	Label        string
	ManifestJSON string
	ResultJSON   string
	Status       string
	Error        string
	SubmittedAt  string
	StartedAt    string
	CompletedAt  string
}

// IsTerminal reports whether the operation has reached a final status —
// nothing more will happen to it.
func (o *Operation) IsTerminal() bool {
	switch o.Status {
	case StatusSucceeded, StatusFailed, StatusInterrupted:
		return true
	}
	return false
}

// ConflictError is returned by Submit when an in-flight op already exists
// for the same resource. The CLI/RPC layer maps this to AlreadyExists +
// surfaces InflightID so the user can poll the existing op.
type ConflictError struct {
	InflightID string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("operation %s already in flight for this resource", e.InflightID)
}

// Store is the operations data layer. Backed by SQLite via the *sql.DB
// passed to New.
type Store struct {
	db *sql.DB
	// retainPerResource is the GC cap: when inserting a new op for a
	// resource, completed ops beyond this count get pruned. 0 disables GC.
	retainPerResource int
}

// New constructs a Store. retainPerResource controls how many completed ops
// per resource the GC keeps; sensible default is 50.
func New(db *sql.DB, retainPerResource int) *Store {
	return &Store{db: db, retainPerResource: retainPerResource}
}

// Submit inserts a new operation in `pending` state. Enforces the
// fail-fast concurrency rule: returns *ConflictError if another op for
// the same (apiVersion, kind, name) is currently pending or running.
//
// Also runs the per-resource GC after a successful submission.
func (s *Store) Submit(ctx context.Context, op *Operation) (*Operation, error) {
	if op.Type == "" || op.APIVersion == "" || op.Kind == "" || op.ResourceName == "" {
		return nil, fmt.Errorf("submit: type, apiVersion, kind, resource_name all required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// Fail-fast concurrency check inside the transaction.
	var inflightID sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM operations
		 WHERE api_version = ? AND kind = ? AND resource_name = ?
		   AND status IN (?, ?)
		 LIMIT 1`,
		op.APIVersion, op.Kind, op.ResourceName,
		StatusPending, StatusRunning,
	).Scan(&inflightID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("conflict check: %w", err)
	}
	if inflightID.Valid && inflightID.String != "" {
		return nil, &ConflictError{InflightID: inflightID.String}
	}

	// Mint an ID, set defaults.
	op.ID = newOpID()
	op.Status = StatusPending
	op.SubmittedAt = nowUTC()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO operations
		   (id, parent_id, type, api_version, kind, resource_name, label,
		    manifest_json, status, submitted_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		op.ID, nullable(op.ParentID), op.Type, op.APIVersion, op.Kind, op.ResourceName,
		nullable(op.Label), nullable(op.ManifestJSON), op.Status, op.SubmittedAt,
	); err != nil {
		return nil, fmt.Errorf("insert: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	// GC after commit so we don't roll back the new op if GC errors.
	if s.retainPerResource > 0 {
		if err := s.gcResource(ctx, op.APIVersion, op.Kind, op.ResourceName); err != nil {
			// Non-fatal: log via returned error string so caller can decide.
			return op, fmt.Errorf("submit ok, gc warning: %w", err)
		}
	}

	return op, nil
}

// Get returns the op by ID, or sql.ErrNoRows if missing.
func (s *Store) Get(ctx context.Context, id string) (*Operation, error) {
	row := s.db.QueryRowContext(ctx, baseSelect+` WHERE id = ?`, id)
	return scanOp(row)
}

// List returns ops matching the optional filters. Empty filter = match all.
// Ordered newest first.
func (s *Store) List(ctx context.Context, filter ListFilter) ([]*Operation, error) {
	q := baseSelect + " WHERE 1=1"
	var args []any
	if filter.Status != "" {
		q += " AND status = ?"
		args = append(args, filter.Status)
	}
	if filter.APIVersion != "" {
		q += " AND api_version = ?"
		args = append(args, filter.APIVersion)
	}
	if filter.Kind != "" {
		q += " AND kind = ?"
		args = append(args, filter.Kind)
	}
	if filter.ResourceName != "" {
		q += " AND resource_name = ?"
		args = append(args, filter.ResourceName)
	}
	q += " ORDER BY submitted_at DESC"
	if filter.Limit > 0 {
		// #nosec G202 -- filter.Limit is an int from typed Go API, not user-supplied SQL.
		q += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*Operation
	for rows.Next() {
		op, err := scanOp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// ListFilter is the optional filter set for List.
type ListFilter struct {
	Status       string
	APIVersion   string
	Kind         string
	ResourceName string
	Limit        int
}

// ClaimNextPending atomically marks the oldest pending op as running and
// returns it. Returns sql.ErrNoRows if no pending op exists. Used by the
// dispatcher's poll loop.
func (s *Store) ClaimNextPending(ctx context.Context) (*Operation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRowContext(ctx,
		baseSelect+` WHERE status = ? ORDER BY submitted_at ASC LIMIT 1`,
		StatusPending)
	op, err := scanOp(row)
	if err != nil {
		return nil, err
	}
	now := nowUTC()
	if _, err := tx.ExecContext(ctx,
		`UPDATE operations SET status = ?, started_at = ? WHERE id = ?`,
		StatusRunning, now, op.ID,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	op.Status = StatusRunning
	op.StartedAt = now
	return op, nil
}

// Complete writes the terminal status + result/error for an op. Also runs
// per-resource GC so the retention cap holds even after long stretches of
// submissions all targeting the same resource (Submit's GC alone leaves
// retain+1 because the just-inserted pending op isn't yet a completed op).
func (s *Store) Complete(ctx context.Context, id, status, errMsg, resultJSON string) error {
	if status != StatusSucceeded && status != StatusFailed {
		return fmt.Errorf("complete: status must be %s or %s, got %s", StatusSucceeded, StatusFailed, status)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE operations SET status = ?, error = ?, result_json = ?, completed_at = ?
		 WHERE id = ?`,
		status, nullable(errMsg), nullable(resultJSON), nowUTC(), id,
	); err != nil {
		return err
	}
	if s.retainPerResource > 0 {
		op, err := s.Get(ctx, id)
		if err != nil {
			return nil // we already wrote the terminal status, gc is best-effort
		}
		_ = s.gcResource(ctx, op.APIVersion, op.Kind, op.ResourceName)
	}
	return nil
}

// BeginChild inserts a child operation row in `running` state under the
// given parent and returns it. Unlike Submit, this does NOT run the
// fail-fast concurrency check — the provider executing on behalf of the
// parent has already serialized work and knows what it's doing.
//
// Child rows make sub-step progress visible (one row per VM applied, one
// row for "install-k3s", etc.) but aren't independently dispatched: the
// parent's provider runs them in-process and reports terminal status via
// EndChild. Restart semantics: if the controller dies mid-parent, the
// parent's MarkRunningInterrupted sweep does NOT touch its children
// (they're already non-pending; orphaned `running` children are surfaced
// as their own interrupted rows on the next startup sweep).
func (s *Store) BeginChild(ctx context.Context, parentID string, op *Operation) (*Operation, error) {
	if parentID == "" {
		return nil, fmt.Errorf("begin child: parentID required")
	}
	if op.Type == "" {
		return nil, fmt.Errorf("begin child: type required")
	}
	op.ID = newOpID()
	op.ParentID = parentID
	op.Status = StatusRunning
	op.SubmittedAt = nowUTC()
	op.StartedAt = op.SubmittedAt

	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO operations
		   (id, parent_id, type, api_version, kind, resource_name, label,
		    manifest_json, status, submitted_at, started_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		op.ID, op.ParentID, op.Type, op.APIVersion, op.Kind, op.ResourceName,
		nullable(op.Label), nullable(op.ManifestJSON), op.Status,
		op.SubmittedAt, op.StartedAt,
	); err != nil {
		return nil, fmt.Errorf("insert child: %w", err)
	}
	return op, nil
}

// EndChild writes the terminal status (succeeded|failed) for a child op.
// resultJSON may be empty.
func (s *Store) EndChild(ctx context.Context, childID, status, errMsg, resultJSON string) error {
	if status != StatusSucceeded && status != StatusFailed {
		return fmt.Errorf("end child: status must be %s or %s, got %s", StatusSucceeded, StatusFailed, status)
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE operations SET status = ?, error = ?, result_json = ?, completed_at = ?
		 WHERE id = ?`,
		status, nullable(errMsg), nullable(resultJSON), nowUTC(), childID,
	)
	return err
}

// ListChildren returns the children of the given parent op, oldest first.
// Empty slice if no children exist.
func (s *Store) ListChildren(ctx context.Context, parentID string) ([]*Operation, error) {
	if parentID == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		baseSelect+` WHERE parent_id = ? ORDER BY submitted_at ASC`,
		parentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*Operation
	for rows.Next() {
		op, err := scanOp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// SetLabel updates the label column for an op. Used by the dispatcher to
// annotate ops (e.g. "cached: input hash unchanged") after they're already
// in flight, since the label is set at Submit time and we don't always
// know what to write then.
func (s *Store) SetLabel(ctx context.Context, id, label string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE operations SET label = ? WHERE id = ?`,
		nullable(label), id,
	)
	return err
}

// MarkRunningInterrupted rewrites every op currently in `running` as
// `interrupted`. Called once at controller startup so the user knows which
// ops were active when the previous controller died.
func (s *Store) MarkRunningInterrupted(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE operations SET status = ?, error = ?, completed_at = ?
		 WHERE status = ?`,
		StatusInterrupted,
		"controller restarted while this operation was running; re-apply if needed",
		nowUTC(),
		StatusRunning,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// gcResource prunes completed ops for a resource beyond the retention cap,
// keeping the most recent ones.
func (s *Store) gcResource(ctx context.Context, apiVersion, kind, name string) error {
	if s.retainPerResource <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM operations
		 WHERE id IN (
		   SELECT id FROM operations
		   WHERE api_version = ? AND kind = ? AND resource_name = ?
		     AND status IN (?, ?, ?)
		   ORDER BY submitted_at DESC
		   LIMIT -1 OFFSET ?
		 )`,
		apiVersion, kind, name,
		StatusSucceeded, StatusFailed, StatusInterrupted,
		s.retainPerResource,
	)
	return err
}

const baseSelect = `SELECT id, COALESCE(parent_id, ''), type, api_version, kind, resource_name,
	COALESCE(label, ''),
	COALESCE(manifest_json, ''), COALESCE(result_json, ''), status,
	COALESCE(error, ''), submitted_at, COALESCE(started_at, ''), COALESCE(completed_at, '')
	FROM operations`

// rowScanner is the common interface implemented by sql.Row and sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanOp(s rowScanner) (*Operation, error) {
	op := &Operation{}
	if err := s.Scan(
		&op.ID, &op.ParentID, &op.Type, &op.APIVersion, &op.Kind, &op.ResourceName,
		&op.Label,
		&op.ManifestJSON, &op.ResultJSON, &op.Status,
		&op.Error, &op.SubmittedAt, &op.StartedAt, &op.CompletedAt,
	); err != nil {
		return nil, err
	}
	return op, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// newOpID returns a fresh op ID like "op-a1b2c3d4e5f67890" (16 hex chars,
// 8 random bytes — 64 bits of entropy, plenty for op uniqueness).
func newOpID() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return "op-" + strings.ToLower(hex.EncodeToString(buf[:]))
}
