package server

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/schema"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
)

// resourceHandler implements apiv1.ResourceServiceServer by routing each RPC
// to the appropriate Provider based on the resource's apiVersion.
type resourceHandler struct {
	apiv1.UnimplementedResourceServiceServer
	registry *providers.Registry
}

func newResourceHandler(reg *providers.Registry) *resourceHandler {
	return &resourceHandler{registry: reg}
}

func (h *resourceHandler) Apply(ctx context.Context, req *apiv1.ApplyRequest) (*apiv1.ApplyResponse, error) {
	if req.GetResource() == nil {
		return nil, status.Error(codes.InvalidArgument, "resource is required")
	}
	p, err := h.registry.For(req.GetResource().GetApiVersion())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	manifest := protoToResource(req.GetResource())
	// Re-validate against the embedded CUE schema, even though the CLI
	// already validated. The controller never trusts the wire blindly —
	// a misbehaving or out-of-date client should not get past schema checks.
	if err := schema.Validate(manifest); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "schema validation: %v", err)
	}
	result, err := p.Apply(ctx, manifest)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "apply: %v", err)
	}

	out, err := resourceToProto(result)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode result: %v", err)
	}
	return &apiv1.ApplyResponse{
		Resource: out,
		Message:  fmt.Sprintf("%s %q applied", manifest.Kind, manifest.Metadata.Name),
	}, nil
}

func (h *resourceHandler) Get(ctx context.Context, req *apiv1.GetRequest) (*apiv1.GetResponse, error) {
	p, err := h.registry.For(req.GetApiVersion())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	r, err := p.Get(ctx, req.GetKind(), req.GetName())
	if err != nil {
		var notFound *providers.NotFoundError
		if errors.As(err, &notFound) {
			return nil, status.Errorf(codes.NotFound, "%s %q not found", req.GetKind(), req.GetName())
		}
		return nil, status.Errorf(codes.Internal, "get: %v", err)
	}
	out, err := resourceToProto(r)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode: %v", err)
	}
	return &apiv1.GetResponse{Resource: out}, nil
}

func (h *resourceHandler) List(ctx context.Context, req *apiv1.ListRequest) (*apiv1.ListResponse, error) {
	p, err := h.registry.For(req.GetApiVersion())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	rs, err := p.List(ctx, req.GetKind())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list: %v", err)
	}
	out := make([]*apiv1.Resource, 0, len(rs))
	for _, r := range rs {
		pr, err := resourceToProto(r)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "encode: %v", err)
		}
		out = append(out, pr)
	}
	return &apiv1.ListResponse{Resources: out}, nil
}

func (h *resourceHandler) Delete(ctx context.Context, req *apiv1.DeleteRequest) (*apiv1.DeleteResponse, error) {
	p, err := h.registry.For(req.GetApiVersion())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := p.Delete(ctx, req.GetKind(), req.GetName()); err != nil {
		return nil, status.Errorf(codes.Internal, "delete: %v", err)
	}
	return &apiv1.DeleteResponse{
		Message: fmt.Sprintf("%s %q deleted", req.GetKind(), req.GetName()),
	}, nil
}

// protoToResource converts the wire form into the in-process Resource type
// used by the providers.
func protoToResource(p *apiv1.Resource) *protocol.Resource {
	r := &protocol.Resource{
		APIVersion: p.GetApiVersion(),
		Kind:       p.GetKind(),
	}
	if md := p.GetMetadata(); md != nil {
		r.Metadata = protocol.ResourceMetadata{
			Name:        md.GetName(),
			Labels:      md.GetLabels(),
			Annotations: md.GetAnnotations(),
		}
	}
	if s := p.GetSpec(); s != nil {
		r.Spec = s.AsMap()
	}
	if s := p.GetStatus(); s != nil {
		r.Status = s.AsMap()
	}
	return r
}

// resourceToProto converts the in-process Resource into the wire form.
func resourceToProto(r *protocol.Resource) (*apiv1.Resource, error) {
	out := &apiv1.Resource{
		ApiVersion: r.APIVersion,
		Kind:       r.Kind,
		Metadata: &apiv1.Metadata{
			Name:        r.Metadata.Name,
			Labels:      r.Metadata.Labels,
			Annotations: r.Metadata.Annotations,
		},
	}
	if r.Spec != nil {
		s, err := structpb.NewStruct(normalize(r.Spec))
		if err != nil {
			return nil, fmt.Errorf("spec: %w", err)
		}
		out.Spec = s
	}
	if r.Status != nil {
		s, err := structpb.NewStruct(normalize(r.Status))
		if err != nil {
			return nil, fmt.Errorf("status: %w", err)
		}
		out.Status = s
	}
	return out, nil
}

// normalize walks a map[string]any tree and converts unsupported number
// types (int, int64, etc.) to float64 — which is what structpb.NewStruct
// requires. YAML decoders produce int values for whole numbers; we turn
// them into floats so structpb accepts them.
func normalize(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = normalizeValue(v)
	}
	return out
}

func normalizeValue(v any) any {
	switch val := v.(type) {
	case int:
		return float64(val)
	case int32:
		return float64(val)
	case int64:
		return float64(val)
	case uint:
		return float64(val)
	case uint32:
		return float64(val)
	case uint64:
		return float64(val)
	case map[string]any:
		return normalize(val)
	case []any:
		out := make([]any, len(val))
		for i, x := range val {
			out[i] = normalizeValue(x)
		}
		return out
	default:
		return v
	}
}
