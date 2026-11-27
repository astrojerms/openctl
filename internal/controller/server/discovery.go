package server

import (
	"context"

	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/internal/controller/providers"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (h *resourceHandler) discoverer(apiVersion string) (providers.Discoverer, error) {
	if apiVersion == "" {
		apiVersion = "proxmox.openctl.io/v1"
	}
	p, err := h.registry.For(apiVersion)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "discovery provider is not configured; provider configuration changes require controller restart")
	}
	d, ok := p.(providers.Discoverer)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "provider does not support read-only discovery")
	}
	return d, nil
}

func (h *resourceHandler) ListDiscoveryContexts(ctx context.Context, req *apiv1.ListDiscoveryContextsRequest) (*apiv1.ListDiscoveryContextsResponse, error) {
	if err := authorize(ctx, auth.RoleViewer); err != nil {
		return nil, err
	}
	d, err := h.discoverer(req.GetApiVersion())
	if err != nil {
		return nil, err
	}
	out := &apiv1.ListDiscoveryContextsResponse{}
	for _, c := range d.DiscoveryContexts() {
		out.Contexts = append(out.Contexts, &apiv1.DiscoveryContext{Name: c.Name, Endpoint: c.Endpoint, DefaultContext: c.Default})
	}
	return out, nil
}

func (h *resourceHandler) TestDiscoveryConnection(ctx context.Context, req *apiv1.TestDiscoveryConnectionRequest) (*apiv1.TestDiscoveryConnectionResponse, error) {
	if err := authorize(ctx, auth.RoleViewer); err != nil {
		return nil, err
	}
	d, err := h.discoverer(req.GetApiVersion())
	if err != nil {
		return nil, err
	}
	if err := d.TestConnection(ctx, req.GetContext()); err != nil {
		// Do not forward upstream errors: these may contain credential-bearing URLs.
		return &apiv1.TestDiscoveryConnectionResponse{Message: "connection failed; check the controller's endpoint certificate trust, provider context, credentials and permissions"}, nil
	}
	return &apiv1.TestDiscoveryConnectionResponse{Connected: true, Message: "authenticated read-only node and VM inventory succeeded"}, nil
}

func (h *resourceHandler) DiscoverResources(ctx context.Context, req *apiv1.DiscoverResourcesRequest) (*apiv1.DiscoverResourcesResponse, error) {
	if err := authorize(ctx, auth.RoleViewer); err != nil {
		return nil, err
	}
	d, err := h.discoverer(req.GetApiVersion())
	if err != nil {
		return nil, err
	}
	inventory, err := d.Discover(ctx, req.GetContext())
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "cannot read complete provider inventory; check connection and read permissions")
	}
	busy := false
	if h.ops != nil {
		for _, state := range []string{operations.StatusPending, operations.StatusRunning} {
			ops, err := h.ops.List(ctx, operations.ListFilter{Status: state, Limit: 1})
			if err != nil {
				return nil, status.Error(codes.FailedPrecondition, "cannot verify controller operation state")
			}
			busy = busy || len(ops) != 0
		}
	}
	out := &apiv1.DiscoverResourcesResponse{}
	for _, item := range inventory {
		if item.Resource == nil {
			return nil, status.Error(codes.Internal, "provider returned an empty inventory resource")
		}
		wire, err := resourceToProto(item.Resource)
		if err != nil {
			return nil, status.Error(codes.Internal, "cannot encode inventory resource")
		}
		entry := &apiv1.DiscoveredResource{Context: item.Context, Node: item.Node, Vmid: int64(item.VMID), Identity: item.Identity, Resource: wire, Managed: item.Managed, Adoptable: item.Adoptable, Reason: item.Reason}
		_, _, owned, ownerErr := h.registry.CheckedOwnerOf(ctx, item.Resource.Kind, item.Resource.Metadata.Name)
		if ownerErr != nil {
			entry.Adoptable = false
			entry.Reason = "cannot verify standalone ownership"
		} else if owned {
			entry.Managed = true
			entry.Adoptable = false
			entry.Reason = "resource belongs to a composite owner; manage the owner instead"
		}
		if h.manifests == nil {
			entry.Adoptable = false
			entry.Reason = "adoption requires a configured manifest store"
		} else {
			desired, err := h.manifests.Load(ctx, item.Resource.APIVersion, item.Resource.Kind, item.Resource.Metadata.Name)
			if err != nil {
				return nil, status.Error(codes.FailedPrecondition, "cannot verify existing resource tracking")
			}
			if desired != nil {
				entry.Adoptable = false
				if entry.Reason == "" {
					entry.Reason = "resource name already has saved desired intent; inspect management tracking"
				}
			}
		}
		if !item.IdentityRecorded && item.Adoptable && item.Identity != "" {
			canonical, canonicalErr := h.canonicalManagedVM(ctx, item.Resource.APIVersion, item.Resource.Kind, item.Resource.Metadata.Name)
			if canonicalErr == nil {
				provider, _ := h.registry.For(canonical.APIVersion)
				node, _ := canonical.Spec["node"].(string)
				if node == item.Node && canonicalContext(provider, canonical) == item.Context {
					entry.Managed = true
					entry.Adoptable = false
					entry.Reason = "managed VM requires explicit immutable identity confirmation; provider VM is unchanged"
					_, capable := provider.(providers.IdentityManager)
					entry.IdentityConfirmationRequired = capable && !busy && !h.gitopsEnabled && h.manifestMode != "gitops"
					if !entry.IdentityConfirmationRequired {
						entry.Reason = "identity confirmation requires an idle controller with GitOps disabled and an identity-capable provider"
					}
				} else {
					entry.Adoptable = false
					entry.Reason = "observed VM does not match canonical saved placement; identity cannot be confirmed"
				}
			}
		}
		if entry.Adoptable && h.manifests != nil {
			if err := h.checkManagedDependents(ctx, item.Resource.APIVersion, item.Resource.Kind, item.Resource.Metadata.Name); err != nil {
				entry.Adoptable = false
				entry.Reason = status.Convert(err).Message()
			}
		}
		if entry.Adoptable && (h.gitopsEnabled || h.manifestMode == "gitops") {
			entry.Adoptable = false
			entry.Reason = "disable GitOps automation and restart the controller before adoption"
		}
		if entry.Adoptable && busy {
			entry.Adoptable = false
			entry.Reason = "adoption requires an idle controller"
		}
		out.Resources = append(out.Resources, entry)
	}
	return out, nil
}
