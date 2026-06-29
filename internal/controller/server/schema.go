package server

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/openctl/openctl/internal/schema"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

// schemaHandler implements apiv1.SchemaServiceServer. Surface is read-only
// introspection over the embedded CUE schemas plus a Validate RPC that
// reuses the same validation path the controller runs pre-apply.
type schemaHandler struct {
	apiv1.UnimplementedSchemaServiceServer
}

func newSchemaHandler() *schemaHandler { return &schemaHandler{} }

func (h *schemaHandler) ListSchemas(_ context.Context, _ *apiv1.ListSchemasRequest) (*apiv1.ListSchemasResponse, error) {
	infos := schema.Registry()
	out := make([]*apiv1.SchemaInfo, 0, len(infos))
	for _, i := range infos {
		out = append(out, infoToProto(i))
	}
	return &apiv1.ListSchemasResponse{Schemas: out}, nil
}

func (h *schemaHandler) GetSchema(_ context.Context, req *apiv1.GetSchemaRequest) (*apiv1.GetSchemaResponse, error) {
	if req.GetApiVersion() == "" || req.GetKind() == "" {
		return nil, status.Error(codes.InvalidArgument, "api_version and kind are required")
	}
	for _, i := range schema.Registry() {
		if i.APIVersion != req.GetApiVersion() || i.Kind != req.GetKind() {
			continue
		}
		src, err := schema.SourceFor(i)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "read schema: %v", err)
		}
		return &apiv1.GetSchemaResponse{
			Info:      infoToProto(i),
			CueSource: string(src),
		}, nil
	}
	return nil, status.Errorf(codes.NotFound, "no embedded schema for %s/%s", req.GetApiVersion(), req.GetKind())
}

func (h *schemaHandler) Validate(_ context.Context, req *apiv1.ValidateRequest) (*apiv1.ValidateResponse, error) {
	if req.GetResource() == nil {
		return nil, status.Error(codes.InvalidArgument, "resource is required")
	}
	r := protoToResource(req.GetResource())
	if err := schema.Validate(r); err != nil {
		return &apiv1.ValidateResponse{Errors: []string{err.Error()}}, nil
	}
	return &apiv1.ValidateResponse{}, nil
}

func infoToProto(i schema.Info) *apiv1.SchemaInfo {
	return &apiv1.SchemaInfo{
		ApiVersion: i.APIVersion,
		Kind:       i.Kind,
		Provider:   i.Provider,
		FileName:   i.FileName,
	}
}
