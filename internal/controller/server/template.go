package server

import (
	"context"
	"encoding/json"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/openctl/openctl/internal/templates"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

// templateHandler implements apiv1.TemplateServiceServer. Surface is
// read-plus-render — no mutation of the template registry itself
// (registry is compile-time; user-authored templates on disk are a
// future extension).
type templateHandler struct {
	apiv1.UnimplementedTemplateServiceServer
	registry *templates.Registry
}

func newTemplateHandler(reg *templates.Registry) *templateHandler {
	return &templateHandler{registry: reg}
}

func (h *templateHandler) ListTemplates(_ context.Context, _ *apiv1.ListTemplatesRequest) (*apiv1.ListTemplatesResponse, error) {
	all := h.registry.All()
	out := &apiv1.ListTemplatesResponse{Templates: make([]*apiv1.TemplateSummary, 0, len(all))}
	for _, t := range all {
		out.Templates = append(out.Templates, summaryOf(t))
	}
	return out, nil
}

func (h *templateHandler) GetTemplate(_ context.Context, req *apiv1.GetTemplateRequest) (*apiv1.GetTemplateResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	t := h.registry.Get(req.GetName())
	if t == nil {
		return nil, status.Errorf(codes.NotFound, "template %q not found", req.GetName())
	}
	params := make([]*apiv1.TemplateParameter, 0, len(t.Parameters))
	for _, p := range t.Parameters {
		pb := &apiv1.TemplateParameter{
			Name:        p.Name,
			Type:        p.Type,
			Description: p.Description,
			Required:    p.Required,
			Enum:        p.Enum,
			OptionsKind: p.OptionsKind,
		}
		if p.Default != nil {
			if b, err := json.Marshal(p.Default); err == nil {
				pb.DefaultJson = string(b)
			}
		}
		params = append(params, pb)
	}
	return &apiv1.GetTemplateResponse{
		Summary:    summaryOf(t),
		Parameters: params,
	}, nil
}

func (h *templateHandler) RenderTemplate(_ context.Context, req *apiv1.RenderTemplateRequest) (*apiv1.RenderTemplateResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if h.registry.Get(req.GetName()) == nil {
		return nil, status.Errorf(codes.NotFound, "template %q not found", req.GetName())
	}
	params := map[string]any{}
	if req.GetParams() != nil {
		params = req.GetParams().AsMap()
	}
	resource, err := h.registry.Render(req.GetName(), params)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "render: %v", err)
	}
	// Convert protocol.Resource → apiv1.Resource. Reuses the existing
	// normalize helper from resource.go (coerces int→float64 so
	// structpb.NewStruct accepts numeric values).
	spec, err := structpb.NewStruct(normalize(resource.Spec))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode spec: %v", err)
	}
	pbResource := &apiv1.Resource{
		ApiVersion: resource.APIVersion,
		Kind:       resource.Kind,
		Metadata: &apiv1.Metadata{
			Name:        resource.Metadata.Name,
			Labels:      resource.Metadata.Labels,
			Annotations: resource.Metadata.Annotations,
		},
		Spec: spec,
	}
	if resource.Status != nil {
		if statusStruct, err := structpb.NewStruct(normalize(resource.Status)); err == nil {
			pbResource.Status = statusStruct
		}
	}
	return &apiv1.RenderTemplateResponse{Resource: pbResource}, nil
}

func summaryOf(t *templates.Template) *apiv1.TemplateSummary {
	return &apiv1.TemplateSummary{
		Name:        t.Name,
		DisplayName: t.DisplayName,
		Description: t.Description,
		ApiVersion:  t.APIVersion,
		Kind:        t.Kind,
	}
}

// normalize is defined in resource.go — reused here to coerce int
// values (which templates emit natively) into float64 for structpb.
