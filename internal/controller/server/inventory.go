package server

import (
	"context"
	"errors"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/manifests"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

// inventoryHandler reads saved intent only. It deliberately has no registry,
// provider or dispatcher dependency: these records convey no live authority.
type inventoryHandler struct {
	apiv1.UnimplementedInventoryServiceServer
	store *manifests.Store
}

func newInventoryHandler(store *manifests.Store) *inventoryHandler {
	return &inventoryHandler{store: store}
}

func (h *inventoryHandler) ListSavedResources(ctx context.Context, req *apiv1.ListRequest) (*apiv1.ListSavedResourcesResponse, error) {
	if err := authorize(ctx, auth.RoleViewer); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetApiVersion()) == "" || strings.TrimSpace(req.GetKind()) == "" {
		return nil, status.Error(codes.InvalidArgument, "api_version and kind are required")
	}
	if h.store == nil {
		return nil, status.Error(codes.FailedPrecondition, "saved inventory is not configured")
	}
	records, err := h.store.ListSavedMetadata(ctx, req.GetApiVersion(), req.GetKind())
	if err != nil {
		return nil, inventoryReadError(ctx, err)
	}
	out := &apiv1.ListSavedResourcesResponse{Resources: make([]*apiv1.SavedResourceMetadata, 0, len(records))}
	for _, record := range records {
		out.Resources = append(out.Resources, &apiv1.SavedResourceMetadata{
			ApiVersion: record.APIVersion,
			Kind:       record.Kind,
			Metadata: &apiv1.Metadata{
				Name:        record.Metadata.Name,
				Labels:      record.Metadata.Labels,
				Annotations: record.Metadata.Annotations,
			},
			AppliedAt: savedAppliedAt(record.AppliedAt),
			Source:    record.Source,
		})
	}
	return out, nil
}

func (h *inventoryHandler) GetSavedResource(ctx context.Context, req *apiv1.GetRequest) (*apiv1.GetSavedResourceResponse, error) {
	if err := authorize(ctx, auth.RoleViewer); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetApiVersion()) == "" || strings.TrimSpace(req.GetKind()) == "" || strings.TrimSpace(req.GetName()) == "" {
		return nil, status.Error(codes.InvalidArgument, "api_version, kind, and name are required")
	}
	if h.store == nil {
		return nil, status.Error(codes.FailedPrecondition, "saved inventory is not configured")
	}
	record, err := h.store.LoadSavedRecord(ctx, req.GetApiVersion(), req.GetKind(), req.GetName())
	if err != nil {
		return nil, inventoryReadError(ctx, err)
	}
	if record == nil {
		return nil, status.Errorf(codes.NotFound, "saved %s %q not found", req.GetKind(), req.GetName())
	}
	resource, err := resourceToProto(record.Resource)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode saved resource: %v", err)
	}
	return &apiv1.GetSavedResourceResponse{
		Resource:  resource,
		AppliedAt: savedAppliedAt(record.AppliedAt),
		Source:    record.Source,
	}, nil
}

func savedAppliedAt(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.Format(time.RFC3339Nano)
}

func inventoryReadError(ctx context.Context, err error) error {
	// SQLite interruption errors need not wrap the context error.
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	return status.Errorf(codes.Internal, "read saved inventory: %v", err)
}
