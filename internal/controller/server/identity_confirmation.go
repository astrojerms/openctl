package server

import (
	"context"

	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/refs"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (h *resourceHandler) ConfirmResourceIdentity(ctx context.Context, req *apiv1.ConfirmResourceIdentityRequest) (*apiv1.ConfirmResourceIdentityResponse, error) {
	if err := authorize(ctx, auth.RoleAdmin); err != nil {
		return nil, err
	}
	h.managementMu.Lock()
	defer h.managementMu.Unlock()
	if err := h.migrationReady(); err != nil {
		return nil, err
	}
	if h.manifestMode == "gitops" {
		return nil, status.Error(codes.FailedPrecondition, "disable GitOps automation and restart before identity confirmation")
	}
	if req.GetIdentity() == "" {
		return nil, status.Error(codes.InvalidArgument, "explicit observed identity is required")
	}
	if err := h.checkNotForgotten(ctx, req.GetApiVersion(), req.GetKind(), req.GetName()); err != nil {
		return nil, err
	}
	if err := h.confirmationIdle(ctx); err != nil {
		return nil, err
	}
	manifest, err := h.canonicalManagedVM(ctx, req.GetApiVersion(), req.GetKind(), req.GetName())
	if err != nil {
		return nil, err
	}
	p, err := h.registry.For(manifest.APIVersion)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "managed VM provider is not configured")
	}
	manager, ok := p.(providers.IdentityManager)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "provider cannot confirm immutable identity")
	}
	if req.GetProviderContext() != "" && req.GetProviderContext() != canonicalContext(p, manifest) {
		return nil, status.Error(codes.FailedPrecondition, "provider context does not match canonical saved placement; confirmation cannot move a VM")
	}
	observed, err := manager.ObserveIdentity(ctx, manifest, req.GetIdentity())
	if err != nil || observed == nil {
		return nil, status.Error(codes.FailedPrecondition, "observed VM identity or placement does not match canonical saved intent")
	}
	if err := manager.ConfirmIdentity(ctx, manifest, req.GetIdentity()); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "identity was not recorded; verify the controller is idle and the instance binding has not changed")
	}
	wire, err := resourceToProto(observed)
	if err != nil {
		return nil, status.Error(codes.Internal, "identity recorded but observed VM could not be encoded")
	}
	return &apiv1.ConfirmResourceIdentityResponse{Resource: wire, Message: "immutable identity confirmed; desired intent, ownership, and provider VM unchanged"}, nil
}

func (h *resourceHandler) confirmationIdle(ctx context.Context) error {
	if h.ops == nil {
		return nil
	} // durable identity-store transaction checks again.
	for _, state := range []string{operations.StatusPending, operations.StatusRunning} {
		ops, err := h.ops.List(ctx, operations.ListFilter{Status: state, Limit: 1})
		if err != nil || len(ops) != 0 {
			return status.Error(codes.FailedPrecondition, "identity confirmation requires an idle controller")
		}
	}
	return nil
}

// canonicalManagedVM never invents placement from discovery. Legacy composite
// children may lack their own row: only their saved owner's plan is authoritative.
func (h *resourceHandler) canonicalManagedVM(ctx context.Context, av, kind, name string) (*protocol.Resource, error) {
	if av != "proxmox.openctl.io/v1" || kind != "VirtualMachine" || name == "" {
		return nil, status.Error(codes.InvalidArgument, "a named Proxmox VirtualMachine is required")
	}
	if h.manifests == nil {
		return nil, status.Error(codes.FailedPrecondition, "identity confirmation requires managed desired intent")
	}
	manifest, err := h.manifests.Load(ctx, av, kind, name)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "cannot load canonical VM intent")
	}
	ownerKind, ownerName, owned, err := h.registry.CheckedOwnerOf(ctx, kind, name)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "cannot verify resource ownership")
	}
	if manifest == nil {
		if !owned {
			return nil, status.Error(codes.FailedPrecondition, "VM is not already managed; use adoption instead")
		}
		owner, ok := h.registry.OwnerRefOf(kind, name)
		if !ok || owner.Kind != ownerKind || owner.Name != ownerName {
			return nil, status.Error(codes.FailedPrecondition, "cannot prove canonical owner identity")
		}
		parent, err := h.manifests.Load(ctx, owner.APIVersion, owner.Kind, owner.Name)
		if err != nil || parent == nil {
			return nil, status.Error(codes.FailedPrecondition, "canonical owner manifest is unavailable")
		}
		provider, err := h.registry.For(owner.APIVersion)
		if err != nil {
			return nil, status.Error(codes.FailedPrecondition, "canonical owner provider is unavailable")
		}
		planner, ok := provider.(providers.Planner)
		if !ok {
			return nil, status.Error(codes.FailedPrecondition, "owner cannot prove canonical child placement without a declarative plan")
		}
		plan, err := planner.Plan(ctx, parent)
		if err != nil || plan == nil {
			return nil, status.Error(codes.FailedPrecondition, "cannot derive canonical child placement")
		}
		for _, child := range plan.Children {
			if child != nil && child.APIVersion == av && child.Kind == kind && child.Metadata.Name == name {
				if manifest != nil {
					return nil, status.Error(codes.FailedPrecondition, "owner plan contains ambiguous child identities")
				}
				manifest = child
			}
		}
		if manifest == nil {
			return nil, status.Error(codes.FailedPrecondition, "owner plan no longer declares this child; confirmation cannot replace desired intent")
		}
	}
	resolved, err := refs.New(h.registry).Resolve(ctx, manifest.Spec)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "cannot resolve canonical child intent")
	}
	manifest.Spec = resolved
	if node, ok := manifest.Spec["node"].(string); !ok || node == "" {
		return nil, status.Error(codes.FailedPrecondition, "canonical VM placement has no explicit node")
	}
	if value, present := manifest.Spec["context"]; present {
		if _, ok := value.(string); !ok {
			return nil, status.Error(codes.FailedPrecondition, "canonical VM context is not explicit")
		}
	}
	return manifest, nil
}

func canonicalContext(p providers.Provider, manifest *protocol.Resource) string {
	name, _ := manifest.Spec["context"].(string)
	if name != "" {
		return name
	}
	if d, ok := p.(providers.Discoverer); ok {
		for _, entry := range d.DiscoveryContexts() {
			if entry.Default {
				return entry.Name
			}
		}
	}
	return ""
}
