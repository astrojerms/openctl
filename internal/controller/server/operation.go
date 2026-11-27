package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/operations"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
)

// runningCanceler aborts an in-flight op's context. Implemented by the
// dispatcher. Nil when no dispatcher is wired (some unit tests) — running
// ops then stay non-cancelable (FailedPrecondition), preserving the
// pending-only behavior.
type runningCanceler interface {
	CancelRunning(id string) bool
}

// operationHandler implements apiv1.OperationServiceServer. Mostly a
// read-only surface; CancelOperation is the one mutation — it cancels a
// pending op (store) or requests cooperative cancellation of a running one
// (canceler).
type operationHandler struct {
	apiv1.UnimplementedOperationServiceServer
	store    *operations.Store
	canceler runningCanceler
}

func newOperationHandler(store *operations.Store, canceler runningCanceler) *operationHandler {
	return &operationHandler{store: store, canceler: canceler}
}

func (h *operationHandler) GetOperation(ctx context.Context, req *apiv1.GetOperationRequest) (*apiv1.Operation, error) {
	if err := authorize(ctx, auth.RoleViewer); err != nil {
		return nil, err
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	op, err := h.store.Get(ctx, req.GetId())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "operation %s not found", req.GetId())
		}
		return nil, status.Errorf(codes.Internal, "get: %v", err)
	}
	return h.operationResponse(ctx, op, req.GetIncludeChildren())
}

func (h *operationHandler) GetOperationByRequest(ctx context.Context, req *apiv1.GetOperationByRequestRequest) (*apiv1.Operation, error) {
	if err := authorize(ctx, auth.RoleViewer); err != nil {
		return nil, err
	}
	if req.GetRequestId() == "" {
		return nil, status.Error(codes.InvalidArgument, "request_id is required")
	}
	if err := operations.ValidateRequestID(req.GetRequestId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	op, err := h.store.GetByRequest(ctx, req.GetRequestId())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "request_id has no accepted operation")
		}
		var pruned *operations.RequestPrunedError
		if errors.As(err, &pruned) {
			return nil, status.Error(codes.FailedPrecondition, pruned.Error())
		}
		return nil, status.Errorf(codes.Internal, "get by request: %v", err)
	}
	return h.operationResponse(ctx, op, req.GetIncludeChildren())
}

func (h *operationHandler) operationResponse(ctx context.Context, op *operations.Operation, includeChildren bool) (*apiv1.Operation, error) {
	pb, err := opToProto(op)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode: %v", err)
	}
	// Recovery returns the original unresolved declaration, not provider input
	// with expanded secrets/references. List/Watch deliberately omit it.
	pb.ManifestJson = op.ManifestJSON
	if includeChildren {
		children, err := h.store.ListChildren(ctx, op.ID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "list children: %v", err)
		}
		for _, c := range children {
			cpb, err := opToProto(c)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "encode child %s: %v", c.ID, err)
			}
			pb.Children = append(pb.Children, cpb)
		}
	}
	return pb, nil
}

func (h *operationHandler) ListOperations(ctx context.Context, req *apiv1.ListOperationsRequest) (*apiv1.ListOperationsResponse, error) {
	ops, err := h.store.List(ctx, operations.ListFilter{
		Status:       req.GetStatus(),
		APIVersion:   req.GetApiVersion(),
		Kind:         req.GetKind(),
		ResourceName: req.GetResourceName(),
		Source:       req.GetSource(),
		Since:        req.GetSince(),
		Until:        req.GetUntil(),
		Limit:        int(req.GetLimit()),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list: %v", err)
	}
	out := make([]*apiv1.Operation, 0, len(ops))
	for _, op := range ops {
		pb, err := opToProto(op)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "encode op %s: %v", op.ID, err)
		}
		out = append(out, pb)
	}
	return &apiv1.ListOperationsResponse{Operations: out}, nil
}

// CancelOperation marks a pending op as canceled. UI Phase U7 surfaces
// this as a "Cancel" button on pending-op rows. Running ops require
// cooperative cancellation in providers (deferred); canceling a running
// op returns FailedPrecondition.
func (h *operationHandler) CancelOperation(ctx context.Context, req *apiv1.CancelOperationRequest) (*apiv1.CancelOperationResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	res, err := h.store.CancelPending(ctx, req.GetId())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "operation %s not found", req.GetId())
		}
		return nil, status.Errorf(codes.Internal, "cancel: %v", err)
	}
	// Pending op was flipped to canceled synchronously.
	if res.Status == operations.StatusCancelled {
		return &apiv1.CancelOperationResponse{
			Status:  res.Status,
			Message: fmt.Sprintf("operation %s canceled", req.GetId()),
		}, nil
	}
	// Running op: request cooperative cancellation via the dispatcher. The op
	// transitions to canceled asynchronously once its provider yields to the
	// aborted context; the client observes that via Watch.
	if res.Status == operations.StatusRunning && h.canceler != nil {
		if h.canceler.CancelRunning(req.GetId()) {
			return &apiv1.CancelOperationResponse{
				Status:  res.Status,
				Message: fmt.Sprintf("cancellation requested for running operation %s; it will stop when the provider yields", req.GetId()),
			}, nil
		}
	}
	return nil, status.Errorf(codes.FailedPrecondition,
		"operation %s status=%s: %s", req.GetId(), res.Status, res.Reason)
}

// opToProto converts the in-process Operation into the wire form. The
// result_json field, when present, is decoded back into a Resource value.
// manifest_json is left empty by default; GetOperation explicitly sets it
// (UI Phase U7) so List/Watch responses stay cheap.
func opToProto(op *operations.Operation) (*apiv1.Operation, error) {
	pb := &apiv1.Operation{
		Id:           op.ID,
		ParentId:     op.ParentID,
		Type:         op.Type,
		ApiVersion:   op.APIVersion,
		Kind:         op.Kind,
		ResourceName: op.ResourceName,
		Label:        op.Label,
		Status:       op.Status,
		Error:        op.Error,
		SubmittedAt:  op.SubmittedAt,
		StartedAt:    op.StartedAt,
		CompletedAt:  op.CompletedAt,
		Source:       op.Source,
		RequestId:    op.RequestID,
	}
	if op.ResultJSON != "" {
		var r protocol.Resource
		if err := json.Unmarshal([]byte(op.ResultJSON), &r); err != nil {
			return nil, fmt.Errorf("decode result: %w", err)
		}
		out, err := resourceToProto(&r)
		if err != nil {
			return nil, err
		}
		pb.Result = out
	}
	return pb, nil
}
