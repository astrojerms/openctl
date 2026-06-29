package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/openctl/openctl/internal/controller/operations"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
)

// operationHandler implements apiv1.OperationServiceServer. Read-only
// surface — no mutations beyond what Apply/Delete already enqueue via the
// resource handler.
type operationHandler struct {
	apiv1.UnimplementedOperationServiceServer
	store *operations.Store
}

func newOperationHandler(store *operations.Store) *operationHandler {
	return &operationHandler{store: store}
}

func (h *operationHandler) GetOperation(ctx context.Context, req *apiv1.GetOperationRequest) (*apiv1.Operation, error) {
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
	pb, err := opToProto(op)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode: %v", err)
	}
	// UI Phase U7: GetOperation includes the submitted manifest so the
	// browser's retry path can pre-fill the editor with the exact YAML
	// the failed/interrupted op was applying. List/Watch omit it.
	pb.ManifestJson = op.ManifestJSON
	if req.GetIncludeChildren() {
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

// CancelOperation marks a pending op as cancelled. UI Phase U7 surfaces
// this as a "Cancel" button on pending-op rows. Running ops require
// cooperative cancellation in providers (deferred); cancelling a running
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
	if res.Status != operations.StatusCancelled {
		return nil, status.Errorf(codes.FailedPrecondition,
			"operation %s status=%s: %s", req.GetId(), res.Status, res.Reason)
	}
	return &apiv1.CancelOperationResponse{
		Status:  res.Status,
		Message: fmt.Sprintf("operation %s cancelled", req.GetId()),
	}, nil
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
