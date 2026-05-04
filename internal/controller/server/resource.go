package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/schema"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
)

// resourceHandler implements apiv1.ResourceServiceServer. Apply/Delete
// insert ops into the operations Store and notify the Dispatcher; Get/List
// remain synchronous (read-only).
//
// If ops or dispatcher are nil (test mode), Apply/Delete fall back to
// calling the Provider synchronously and return a synthetic operation_id.
type resourceHandler struct {
	apiv1.UnimplementedResourceServiceServer
	registry   *providers.Registry
	ops        *operations.Store
	dispatcher *operations.Dispatcher
}

func newResourceHandler(reg *providers.Registry, ops *operations.Store, d *operations.Dispatcher) *resourceHandler {
	return &resourceHandler{registry: reg, ops: ops, dispatcher: d}
}

func (h *resourceHandler) Apply(ctx context.Context, req *apiv1.ApplyRequest) (*apiv1.ApplyResponse, error) {
	if req.GetResource() == nil {
		return nil, status.Error(codes.InvalidArgument, "resource is required")
	}
	if _, err := h.registry.For(req.GetResource().GetApiVersion()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	manifest := protoToResource(req.GetResource())
	// Re-validate server-side; the CLI already validated, but the controller
	// never trusts the wire blindly.
	if err := schema.Validate(manifest); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "schema validation: %v", err)
	}
	if manifest.Metadata.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "metadata.name is required")
	}

	// Phase 3: enqueue an op and return immediately. Phase 2 sync fallback
	// kicks in only if ops/dispatcher weren't wired (test mode).
	if h.ops == nil || h.dispatcher == nil {
		return h.applySync(ctx, manifest)
	}

	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode manifest: %v", err)
	}
	op, err := h.ops.Submit(ctx, &operations.Operation{
		Type:         operations.TypeApply,
		APIVersion:   manifest.APIVersion,
		Kind:         manifest.Kind,
		ResourceName: manifest.Metadata.Name,
		ManifestJSON: string(manifestJSON),
	})
	if err != nil {
		var conflict *operations.ConflictError
		if errors.As(err, &conflict) {
			return nil, status.Errorf(codes.AlreadyExists,
				"operation %s already in flight for %s/%s", conflict.InflightID, manifest.Kind, manifest.Metadata.Name)
		}
		return nil, status.Errorf(codes.Internal, "submit op: %v", err)
	}
	h.dispatcher.Notify()

	return &apiv1.ApplyResponse{
		OperationId: op.ID,
		Message:     fmt.Sprintf("%s %q apply submitted as %s", manifest.Kind, manifest.Metadata.Name, op.ID),
	}, nil
}

// applySync is the Phase 2 synchronous fallback used by tests that don't
// wire up the Operations store/Dispatcher.
func (h *resourceHandler) applySync(ctx context.Context, manifest *protocol.Resource) (*apiv1.ApplyResponse, error) {
	p, err := h.registry.For(manifest.APIVersion)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if _, err := p.Apply(ctx, manifest); err != nil {
		return nil, status.Errorf(codes.Internal, "apply: %v", err)
	}
	return &apiv1.ApplyResponse{
		Message: fmt.Sprintf("%s %q applied (sync mode)", manifest.Kind, manifest.Metadata.Name),
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
	if _, err := h.registry.For(req.GetApiVersion()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if h.ops == nil || h.dispatcher == nil {
		// Phase 2 sync fallback — used by tests.
		p, err := h.registry.For(req.GetApiVersion())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		if err := p.Delete(ctx, req.GetKind(), req.GetName()); err != nil {
			return nil, status.Errorf(codes.Internal, "delete: %v", err)
		}
		return &apiv1.DeleteResponse{
			Message: fmt.Sprintf("%s %q deleted (sync mode)", req.GetKind(), req.GetName()),
		}, nil
	}

	op, err := h.ops.Submit(ctx, &operations.Operation{
		Type:         operations.TypeDelete,
		APIVersion:   req.GetApiVersion(),
		Kind:         req.GetKind(),
		ResourceName: req.GetName(),
	})
	if err != nil {
		var conflict *operations.ConflictError
		if errors.As(err, &conflict) {
			return nil, status.Errorf(codes.AlreadyExists,
				"operation %s already in flight for %s/%s", conflict.InflightID, req.GetKind(), req.GetName())
		}
		return nil, status.Errorf(codes.Internal, "submit op: %v", err)
	}
	h.dispatcher.Notify()

	return &apiv1.DeleteResponse{
		OperationId: op.ID,
		Message:     fmt.Sprintf("%s %q delete submitted as %s", req.GetKind(), req.GetName(), op.ID),
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
