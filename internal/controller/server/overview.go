package server

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/overview"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

type overviewHandler struct {
	apiv1.UnimplementedOverviewServiceServer
	store *overview.Store
}

func newOverviewHandler(store *overview.Store) *overviewHandler {
	return &overviewHandler{store: store}
}

func (h *overviewHandler) GetOverview(ctx context.Context, req *apiv1.GetOverviewRequest) (*apiv1.GetOverviewResponse, error) {
	if err := authorize(ctx, auth.RoleViewer); err != nil {
		return nil, err
	}
	if req.GetOperationLimit() > overview.MaxOperationLimit {
		return nil, status.Errorf(codes.InvalidArgument, "operation_limit must be at most %d", overview.MaxOperationLimit)
	}
	if h.store == nil {
		return nil, status.Error(codes.FailedPrecondition, "persisted overview is not configured")
	}
	snapshot, err := h.store.Snapshot(ctx, req.GetOperationLimit())
	if err != nil {
		// Drivers may return their interruption error instead of ctx.Err().
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, status.FromContextError(err).Err()
		}
		return nil, status.Errorf(codes.Internal, "read persisted overview: %v", err)
	}
	out := &apiv1.GetOverviewResponse{
		AsOf: snapshot.AsOf,
		ManagedResources: snapshot.ManagedResources,
		PendingOperations: snapshot.PendingOperations,
		RunningOperations: snapshot.RunningOperations,
		ReviewResources: snapshot.ReviewResources,
		Kinds: make([]*apiv1.ManagedKindCount, 0, len(snapshot.Kinds)),
		ActiveOperations: overviewOperations(snapshot.ActiveOperations),
		ReviewOperations: overviewOperations(snapshot.ReviewOperations),
	}
	for _, kind := range snapshot.Kinds {
		out.Kinds = append(out.Kinds, &apiv1.ManagedKindCount{
			ApiVersion: kind.APIVersion,
			Kind: kind.Kind,
			Resources: kind.Resources,
		})
	}
	return out, nil
}

func overviewOperations(ops []overview.OperationMetadata) []*apiv1.Operation {
	out := make([]*apiv1.Operation, 0, len(ops))
	for _, op := range ops {
		// Do not use opToProto: that history/detail conversion can decode
		// results and exposes free-form errors and labels.
		out = append(out, &apiv1.Operation{
			Id: op.ID,
			Type: op.Type,
			ApiVersion: op.APIVersion,
			Kind: op.Kind,
			ResourceName: op.ResourceName,
			Status: op.Status,
			SubmittedAt: op.SubmittedAt,
			StartedAt: op.StartedAt,
			CompletedAt: op.CompletedAt,
			Source: op.Source,
		})
	}
	return out
}
