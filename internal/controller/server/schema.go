package server

import (
	"context"
	"encoding/json"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/schema"
	"github.com/openctl/openctl/internal/schema/form"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

// schemaHandler implements apiv1.SchemaServiceServer. Surface is read-only
// introspection over the embedded CUE schemas plus a Validate RPC that
// reuses the same validation path the controller runs pre-apply.
//
// registry is consulted only to stamp the composite-child "advanced" hint onto
// SchemaInfo (see AdvancedKindDescriber); it may be nil, in which case no kind
// is flagged advanced.
type schemaHandler struct {
	apiv1.UnimplementedSchemaServiceServer
	registry *providers.Registry
}

func newSchemaHandler(registry *providers.Registry) *schemaHandler {
	return &schemaHandler{registry: registry}
}

func (h *schemaHandler) ListSchemas(_ context.Context, _ *apiv1.ListSchemasRequest) (*apiv1.ListSchemasResponse, error) {
	infos := schema.Registry()
	adv := h.registry.AdvancedKinds()
	out := make([]*apiv1.SchemaInfo, 0, len(infos))
	for _, i := range infos {
		out = append(out, infoToProto(i, adv))
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
			Info:      infoToProto(i, h.registry.AdvancedKinds()),
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

// GetFormSchema walks the embedded CUE schema for (apiVersion, kind)
// and returns the form-schema tree as JSON. The wire format is "JSON
// inside a string" so the proto stays non-recursive and the browser
// gets exactly what it needs to render.
func (h *schemaHandler) GetFormSchema(_ context.Context, req *apiv1.GetFormSchemaRequest) (*apiv1.GetFormSchemaResponse, error) {
	if req.GetApiVersion() == "" || req.GetKind() == "" {
		return nil, status.Error(codes.InvalidArgument, "api_version and kind are required")
	}
	f, ok, err := form.BuildForKind(req.GetApiVersion(), req.GetKind())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "build form: %v", err)
	}
	if !ok {
		return nil, status.Errorf(codes.NotFound,
			"no embedded schema for %s/%s", req.GetApiVersion(), req.GetKind())
	}
	out, err := json.Marshal(f)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode form schema: %v", err)
	}
	return &apiv1.GetFormSchemaResponse{Json: string(out)}, nil
}

// infoToProto maps a schema.Info to the wire type, stamping the composite-child
// "advanced" hint when the provider registry declares this kind as one (keyed
// by "<provider>/<kind>"). adv may be nil/empty — then no kind is flagged.
func infoToProto(i schema.Info, adv map[string]providers.AdvancedKind) *apiv1.SchemaInfo {
	out := &apiv1.SchemaInfo{
		ApiVersion: i.APIVersion,
		Kind:       i.Kind,
		Provider:   i.Provider,
		FileName:   i.FileName,
	}
	if a, ok := adv[i.Provider+"/"+i.Kind]; ok {
		out.Advanced = true
		out.OwnerKind = a.OwnerKind
		out.AdvancedNote = a.Note
	}
	return out
}
