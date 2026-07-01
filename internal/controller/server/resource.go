package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/schema"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
)

// sourceMetadataKey is the incoming-metadata header that the HTTP gateway
// stamps on every request it proxies. CLI clients (direct gRPC) don't
// set it, so absent = CLI.
const sourceMetadataKey = "x-openctl-source"

// sourceFromContext returns the request originator: SourceUI when the
// metadata header is present and set to "ui", SourceCLI otherwise. Used to
// stamp Operation.Source so git commit messages can distinguish browser
// from CLI traffic.
func sourceFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return manifests.SourceCLI
	}
	for _, v := range md.Get(sourceMetadataKey) {
		if v == manifests.SourceUI {
			return manifests.SourceUI
		}
	}
	return manifests.SourceCLI
}

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
	// manifests is optional: when set, Get/List populate the drift field by
	// comparing observed state against the persisted desired manifest.
	manifests *manifests.Store
}

func newResourceHandler(reg *providers.Registry, ops *operations.Store, d *operations.Dispatcher, m *manifests.Store) *resourceHandler {
	return &resourceHandler{registry: reg, ops: ops, dispatcher: d, manifests: m}
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

	// Phase 5: surface the destructive flags to providers via annotations.
	// Annotations ride along on manifest_json through the operations table,
	// so the dispatcher delivers them to provider.Apply unchanged.
	if req.GetAllowDestructive() || req.GetIKnowThisBreaksTheCluster() {
		if manifest.Metadata.Annotations == nil {
			manifest.Metadata.Annotations = map[string]string{}
		}
		if req.GetAllowDestructive() {
			manifest.Metadata.Annotations["openctl.io/allow-destructive"] = "true"
		}
		if req.GetIKnowThisBreaksTheCluster() {
			manifest.Metadata.Annotations["openctl.io/i-know-this-breaks-the-cluster"] = "true"
		}
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
		Source:       sourceFromContext(ctx),
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
	// Managed-only filter: hide resources that openctl never applied (and
	// aren't promoted by being a child of something it did apply). Observed-
	// only kinds bypass this. Returns NotFound so a stale UI link looks the
	// same as an actual NotFound — there's no in-between "exists but hidden"
	// state to expose.
	managed, err := h.isManaged(ctx, req.GetApiVersion(), req.GetKind(), req.GetName(), nil, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "managed check: %v", err)
	}
	if !managed {
		return nil, status.Errorf(codes.NotFound, "%s %q not found", req.GetKind(), req.GetName())
	}
	out, err := resourceToProto(r)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode: %v", err)
	}

	resp := &apiv1.GetResponse{Resource: out}
	if h.manifests != nil {
		desired, appliedAt, lerr := h.manifests.LoadWithTime(ctx,
			r.APIVersion, r.Kind, r.Metadata.Name)
		if lerr != nil {
			return nil, status.Errorf(codes.Internal, "load applied manifest: %v", lerr)
		}
		if desired != nil {
			out.Drift = computeDrift(desired.Spec, r.Spec)
			applied, perr := resourceToProto(desired)
			if perr != nil {
				return nil, status.Errorf(codes.Internal, "encode applied: %v", perr)
			}
			// Arch Phase 8: applied also gets the relationship fields so the
			// UI can show the same children/owner_refs in the "desired" pane
			// without a second lookup.
			attachRelationships(h.registry, applied)
			resp.Applied = applied
			if !appliedAt.IsZero() {
				resp.AppliedAt = appliedAt.UTC().Format(time.RFC3339Nano)
			}
		}
	}
	attachRelationships(h.registry, out)
	return resp, nil
}

// attachRelationships populates out.Children and out.Metadata.OwnerRefs from
// the registry's ChildrenOf / OwnerRefOf helpers (arch Phase 8 scoped
// owner-ref plumbing). No-op when nothing's claimed.
func attachRelationships(reg *providers.Registry, out *apiv1.Resource) {
	if out == nil || reg == nil {
		return
	}
	kind := out.GetKind()
	name := out.GetMetadata().GetName()
	if children := reg.ChildrenOf(kind, name); len(children) > 0 {
		out.Children = make([]*apiv1.ResourceRef, 0, len(children))
		for _, c := range children {
			out.Children = append(out.Children, &apiv1.ResourceRef{
				ApiVersion: c.APIVersion,
				Kind:       c.Kind,
				Name:       c.Name,
			})
		}
	}
	if owner, ok := reg.OwnerRefOf(kind, name); ok {
		if out.Metadata == nil {
			out.Metadata = &apiv1.Metadata{Name: name}
		}
		out.Metadata.OwnerRefs = []*apiv1.ResourceRef{{
			ApiVersion: owner.APIVersion,
			Kind:       owner.Kind,
			Name:       owner.Name,
		}}
	}
}

// DryRunApply previews what an Apply would do without enqueuing an op or
// touching any provider state. Runs the same schema validation the Apply
// path runs (errors surface in the response, NOT as an RPC error — the
// editor wants to see them inline), computes spec-level drift against
// the currently-applied manifest, and asks any DryRunner-capable provider
// for the per-child action list + required-gate set.
func (h *resourceHandler) DryRunApply(ctx context.Context, req *apiv1.DryRunApplyRequest) (*apiv1.DryRunApplyResponse, error) {
	if req.GetResource() == nil {
		return nil, status.Error(codes.InvalidArgument, "resource is required")
	}
	manifest := protoToResource(req.GetResource())

	resp := &apiv1.DryRunApplyResponse{}

	// Schema validation: surface inline so the editor can mark them.
	if err := schema.Validate(manifest); err != nil {
		resp.ValidationErrors = []string{err.Error()}
		resp.Summary = "schema validation failed"
		return resp, nil
	}
	if manifest.Metadata.Name == "" {
		resp.ValidationErrors = []string{"metadata.name is required"}
		resp.Summary = "metadata.name is required"
		return resp, nil
	}

	p, err := h.registry.For(manifest.APIVersion)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// Spec-level diff vs the last applied manifest. Empty when this would
	// be the first apply.
	if h.manifests != nil {
		desired, _, lerr := h.manifests.LoadWithTime(ctx,
			manifest.APIVersion, manifest.Kind, manifest.Metadata.Name)
		if lerr != nil {
			return nil, status.Errorf(codes.Internal, "load applied manifest: %v", lerr)
		}
		if desired != nil {
			resp.Diff = computeDrift(desired.Spec, manifest.Spec)
		}
	}

	// Provider-side plan for composites. Atomic providers don't implement
	// DryRunner; the spec-diff above is all the editor gets.
	if dr, ok := p.(providers.DryRunner); ok {
		plan, derr := dr.DryRun(ctx, manifest)
		if derr != nil {
			return nil, status.Errorf(codes.Internal, "dry-run: %v", derr)
		}
		if plan != nil {
			for _, c := range plan.Children {
				resp.Children = append(resp.Children, &apiv1.ChildAction{
					Verb: c.Verb, Kind: c.Kind, Name: c.Name, Detail: c.Detail,
				})
			}
			resp.RequiredGates = append(resp.RequiredGates, plan.RequiredGates...)
			if plan.Summary != "" {
				resp.Summary = plan.Summary
			}
		}
	}

	if resp.Summary == "" {
		resp.Summary = summarizeDryRun(resp)
	}

	return resp, nil
}

// ListActions returns the runtime action names the responsible provider
// supports for (apiVersion, kind). Empty response when the provider has
// no Actioner interface. Never errors on "no actions" — the UI treats
// that as "hide the action bar" rather than a failure.
func (h *resourceHandler) ListActions(_ context.Context, req *apiv1.ListActionsRequest) (*apiv1.ListActionsResponse, error) {
	if req.GetApiVersion() == "" || req.GetKind() == "" {
		return nil, status.Error(codes.InvalidArgument, "api_version and kind are required")
	}
	return &apiv1.ListActionsResponse{
		Actions: h.registry.ActionsFor(req.GetApiVersion(), req.GetKind()),
	}, nil
}

// InvokeAction runs a runtime action against an existing resource.
// FailedPrecondition when the provider doesn't support actions or the
// action isn't in its supported list; Internal for provider-side
// failures (Proxmox API errors, etc). Success returns the provider's
// short text — typically a task UPID for Proxmox.
func (h *resourceHandler) InvokeAction(ctx context.Context, req *apiv1.InvokeActionRequest) (*apiv1.InvokeActionResponse, error) {
	if req.GetApiVersion() == "" || req.GetKind() == "" || req.GetResourceName() == "" || req.GetAction() == "" {
		return nil, status.Error(codes.InvalidArgument, "api_version, kind, resource_name, and action are required")
	}
	msg, err := h.registry.DoAction(ctx, req.GetApiVersion(), req.GetKind(), req.GetResourceName(), req.GetAction())
	if err != nil {
		// Route "not supported" errors to FailedPrecondition so the UI can
		// distinguish user error (button shouldn't have been shown) from
		// provider failure (real problem the user should see).
		if strings.Contains(err.Error(), "not supported") || strings.Contains(err.Error(), "does not support") {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		return nil, status.Errorf(codes.Internal, "action %q on %s/%s: %v", req.GetAction(), req.GetKind(), req.GetResourceName(), err)
	}
	return &apiv1.InvokeActionResponse{Message: msg}, nil
}

// summarizeDryRun builds a default one-line summary when the provider
// didn't supply one. Used for atomic providers: "no-op" when nothing
// would change, "would update N field(s)" otherwise.
func summarizeDryRun(r *apiv1.DryRunApplyResponse) string {
	if len(r.GetDiff()) == 0 && len(r.GetChildren()) == 0 {
		return "no-op"
	}
	if len(r.GetDiff()) > 0 {
		if len(r.GetDiff()) == 1 {
			return "would update 1 field"
		}
		return fmt.Sprintf("would update %d fields", len(r.GetDiff()))
	}
	return ""
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
	appliedNames, ownerCache, err := h.managedScope(ctx, req.GetApiVersion(), req.GetKind())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "managed scope: %v", err)
	}
	out := make([]*apiv1.Resource, 0, len(rs))
	for _, r := range rs {
		managed, mErr := h.isManaged(ctx, r.APIVersion, r.Kind, r.Metadata.Name, appliedNames, ownerCache)
		if mErr != nil {
			return nil, status.Errorf(codes.Internal, "managed check: %v", mErr)
		}
		if !managed {
			continue
		}
		pr, err := resourceToProto(r)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "encode: %v", err)
		}
		if err := h.attachDrift(ctx, pr, r); err != nil {
			return nil, status.Errorf(codes.Internal, "compute drift: %v", err)
		}
		attachRelationships(h.registry, pr)
		out = append(out, pr)
	}
	return &apiv1.ListResponse{Resources: out}, nil
}

// managedScope prepares the inputs for a batch of isManaged checks over a
// single List call: one DB round-trip to pull the applied names for the
// listed (apiVersion, kind), and an empty cache for parent-owner lookups
// that isManaged populates as it walks the result set. Returns (nil, nil,
// nil) when manifests aren't wired (test mode); isManaged treats nil maps
// as "no filtering".
func (h *resourceHandler) managedScope(ctx context.Context, apiVersion, kind string) (map[string]bool, map[string]bool, error) {
	if h.manifests == nil {
		return nil, nil, nil
	}
	names, err := h.manifests.ListNames(ctx, apiVersion, kind)
	if err != nil {
		return nil, nil, err
	}
	return names, map[string]bool{}, nil
}

// isManaged reports whether the controller should surface (apiVersion, kind,
// name) to clients. A resource is managed if any of:
//   - the manifests store isn't wired (test mode — no filtering)
//   - the kind is observed-only (e.g. ProxmoxNode discovered from the API)
//   - the name appears in applied_manifests for its kind
//   - its owner appears in applied_manifests (children inherit visibility
//     so the k3s cluster's member VMs aren't hidden even though the
//     dispatcher records only the parent Cluster's manifest)
//
// appliedNames/ownerCache are batch-call optimizations from managedScope;
// pass nil for one-shot checks (Get/Watch-by-name) — the function falls
// back to a per-call DB lookup.
func (h *resourceHandler) isManaged(ctx context.Context, apiVersion, kind, name string, appliedNames, ownerCache map[string]bool) (bool, error) {
	if h.manifests == nil {
		return true, nil
	}
	if h.registry.IsObservedOnly(apiVersion, kind) {
		return true, nil
	}
	if appliedNames != nil {
		if appliedNames[name] {
			return true, nil
		}
	} else {
		desired, err := h.manifests.Load(ctx, apiVersion, kind, name)
		if err != nil {
			return false, err
		}
		if desired != nil {
			return true, nil
		}
	}
	// Owner promotion: the k3s provider's child VMs aren't directly written
	// to applied_manifests, so check whether their owner is.
	owner, ok := h.registry.OwnerRefOf(kind, name)
	if !ok {
		return false, nil
	}
	cacheKey := owner.APIVersion + "/" + owner.Kind + "/" + owner.Name
	if ownerCache != nil {
		if v, hit := ownerCache[cacheKey]; hit {
			return v, nil
		}
	}
	desired, err := h.manifests.Load(ctx, owner.APIVersion, owner.Kind, owner.Name)
	if err != nil {
		return false, err
	}
	managed := desired != nil
	if ownerCache != nil {
		ownerCache[cacheKey] = managed
	}
	return managed, nil
}

// attachDrift looks up the resource's persisted manifest and populates
// out.Drift with the differences between desired and observed specs. No-op
// if the manifest store isn't wired or no manifest is on file (resource was
// created out-of-band).
func (h *resourceHandler) attachDrift(ctx context.Context, out *apiv1.Resource, observed *protocol.Resource) error {
	if h.manifests == nil || observed == nil {
		return nil
	}
	desired, err := h.manifests.Load(ctx, observed.APIVersion, observed.Kind, observed.Metadata.Name)
	if err != nil {
		return err
	}
	if desired == nil {
		return nil
	}
	out.Drift = computeDrift(desired.Spec, observed.Spec)
	return nil
}

func (h *resourceHandler) Delete(ctx context.Context, req *apiv1.DeleteRequest) (*apiv1.DeleteResponse, error) {
	if _, err := h.registry.For(req.GetApiVersion()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// Block-on-references: refuse to delete a resource owned by another
	// resource. Caller must delete the owner instead. (Architectural
	// decision from CONTROLLER.md "Resource semantics: Delete".)
	if ownerKind, ownerName, owned := h.registry.OwnerOf(req.GetKind(), req.GetName()); owned {
		return nil, status.Errorf(codes.FailedPrecondition,
			"%s %q is owned by %s %q; delete the owner instead",
			req.GetKind(), req.GetName(), ownerKind, ownerName)
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
		Source:       sourceFromContext(ctx),
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
