package operations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openctl/openctl/pkg/protocol"
)

// RequestBinding identifies the caller and original, unresolved Apply intent.
// Only this digest, actor, and operation ID are retained beyond operation GC.
type RequestBinding struct {
	ID         string
	Actor      string
	IntentHash string
}

// ValidateRequestID permits an absent optional key, or 1-128 printable ASCII
// bytes. Whitespace inside a printable key is significant; keys are never trimmed.
func ValidateRequestID(id string) error {
	if len(id) > 128 {
		return fmt.Errorf("request_id must contain at most 128 printable ASCII characters")
	}
	for i := range len(id) {
		if id[i] < 0x20 || id[i] > 0x7e {
			return fmt.Errorf("request_id must contain only printable ASCII characters")
		}
	}
	return nil
}

// NewApplyRequest hashes desired metadata/spec and explicit consent flags before
// any secret/reference expansion or consent-annotation injection. Source and
// observed status are not desired intent. JSON map ordering is deterministic.
func NewApplyRequest(id, actor string, desired *protocol.Resource, createOnly, allowDestructive, breakCluster bool) (RequestBinding, error) {
	if err := ValidateRequestID(id); err != nil {
		return RequestBinding{}, err
	}
	if id == "" || actor == "" || desired == nil {
		return RequestBinding{}, fmt.Errorf("request ID, actor, and desired resource are required")
	}
	resource := *desired
	resource.Status = nil
	body, err := json.Marshal(struct {
		Resource         *protocol.Resource `json:"resource"`
		CreateOnly       bool               `json:"createOnly"`
		AllowDestructive bool               `json:"allowDestructive"`
		BreakCluster     bool               `json:"breakCluster"`
	}{&resource, createOnly, allowDestructive, breakCluster})
	if err != nil {
		return RequestBinding{}, fmt.Errorf("encode request intent: %w", err)
	}
	digest := sha256.Sum256(body)
	return RequestBinding{ID: id, Actor: actor, IntentHash: hex.EncodeToString(digest[:])}, nil
}

// RequestConflictError refuses reuse of an accepted key by another actor or
// with different desired intent/flags. Neither original payload nor actor leaks.
type RequestConflictError struct {
	RequestID   string
	OperationID string
}

func (e *RequestConflictError) Error() string {
	return fmt.Sprintf("request_id is already bound to operation %s with a different actor or apply intent", e.OperationID)
}

// RequestPrunedError is a durable tombstone, not an invented operation status.
// The accepted key remains reserved even after its operation history is removed.
type RequestPrunedError struct {
	OperationID string
}

func (e *RequestPrunedError) Error() string {
	return fmt.Sprintf("accepted operation %s was pruned from history; inspect the current resource before explicitly submitting a newly reviewed request with a new request_id", e.OperationID)
}

type requestQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getRequest(ctx context.Context, q requestQuerier, id string) (RequestBinding, string, error) {
	binding := RequestBinding{ID: id}
	var operationID string
	err := q.QueryRowContext(ctx,
		`SELECT actor, intent_hash, operation_id FROM operation_requests WHERE request_id = ?`, id,
	).Scan(&binding.Actor, &binding.IntentHash, &operationID)
	return binding, operationID, err
}

func replayRequest(ctx context.Context, q requestQuerier, request RequestBinding) (*Operation, error) {
	stored, operationID, err := getRequest(ctx, q, request.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if stored.Actor != request.Actor || stored.IntentHash != request.IntentHash {
		return nil, &RequestConflictError{RequestID: request.ID, OperationID: operationID}
	}
	return getAcceptedOperation(ctx, q, operationID)
}

func getAcceptedOperation(ctx context.Context, q requestQuerier, id string) (*Operation, error) {
	op, err := scanOp(q.QueryRowContext(ctx, baseSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &RequestPrunedError{OperationID: id}
	}
	return op, err
}

// ReplayRequest resolves an accepted binding before mutable admission fences.
// A nil operation and nil error means this key has not yet been accepted. Submit
// rechecks the binding transactionally, so this preliminary read is not admission.
func (s *Store) ReplayRequest(ctx context.Context, request RequestBinding) (*Operation, error) {
	return replayRequest(ctx, s.db, request)
}

// GetByRequest recovers accepted work read-only, with the same visibility as Get.
// Unknown keys return sql.ErrNoRows; pruned history returns RequestPrunedError.
func (s *Store) GetByRequest(ctx context.Context, id string) (*Operation, error) {
	if err := ValidateRequestID(id); err != nil {
		return nil, err
	}
	if id == "" {
		return nil, fmt.Errorf("request_id is required")
	}
	_, operationID, err := getRequest(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	return getAcceptedOperation(ctx, s.db, operationID)
}

// SubmitRequest atomically binds an Apply request to its admitted operation.
// admitted is false on replay. An accepted key is checked before all resource
// fences, and is never rebound, even if the original operation was pruned.
func (s *Store) SubmitRequest(ctx context.Context, op *Operation, request RequestBinding) (*Operation, bool, error) {
	if err := ValidateRequestID(request.ID); err != nil {
		return nil, false, err
	}
	if request.ID == "" || request.Actor == "" || request.IntentHash == "" {
		return nil, false, fmt.Errorf("request ID, actor, and intent hash are required")
	}
	if op.Type != TypeApply || op.ParentID != "" {
		return nil, false, fmt.Errorf("request bindings are only valid for top-level apply operations")
	}
	accepted, err := s.submit(ctx, op, &request)
	// Fresh admission returns the caller's operation; replay loads the original.
	return accepted, accepted == op, err
}
