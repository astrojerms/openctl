package proxmox

import (
	"context"
	"fmt"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/schema"
	"github.com/openctl/openctl/pkg/protocol"
)

func (p *Provider) DiscoveryContexts() []providers.DiscoveryContext {
	out := make([]providers.DiscoveryContext, 0, len(p.order))
	for _, name := range p.order {
		out = append(out, providers.DiscoveryContext{Name: name, Endpoint: p.handlers[name].Config().Endpoint, Default: name == p.defaultCtx})
	}
	return out
}

func (p *Provider) TestConnection(ctx context.Context, contextName string) error {
	h, _, err := p.handlerForContext(contextName)
	if err != nil {
		return err
	}
	if _, err := h.Client().ListNodes(ctx); err != nil {
		return err
	}
	_, err = h.Client().ListVMs(ctx)
	return err
}

func (p *Provider) Discover(ctx context.Context, contextName string) ([]providers.DiscoveredResource, error) {
	h, resolved, err := p.handlerForContext(contextName)
	if err != nil {
		return nil, err
	}
	vms, err := h.Client().ListVMs(ctx)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(vms))
	for _, vm := range vms {
		counts[vm.Name]++
	}
	out := make([]providers.DiscoveredResource, 0, len(vms))
	for _, vm := range vms {
		if vm.Template != 0 {
			continue
		}
		res, err := observeVM(ctx, h, resolved, vm)
		if err != nil {
			return nil, fmt.Errorf("observe inventory VM %d: %w", vm.VMID, err)
		}
		// Proxmox observes an absent VLAN tag as zero; declarative intent
		// represents an untagged NIC by omitting the optional tag.
		networks, _ := res.Spec["networks"].([]any)
		for _, value := range networks {
			if nic, ok := value.(map[string]any); ok && nic["vlan"] == 0 {
				delete(nic, "vlan")
			}
		}
		raw, err := h.Client().GetVMConfigRaw(ctx, vm.Node, vm.VMID)
		if err != nil {
			return nil, fmt.Errorf("read inventory identity VM %d: %w", vm.VMID, err)
		}
		identity, identityErr := vmIdentity(vm, raw)
		item := providers.DiscoveredResource{Context: resolved, Node: vm.Node, VMID: vm.VMID, Identity: identity, Resource: res, Adoptable: true}
		if err := schema.Validate(res); err != nil {
			item.Adoptable = false
			item.Reason = "observed VM settings cannot be represented by the current resource schema"
		}
		if identityErr != nil {
			item.Adoptable = false
			item.Reason = identityErr.Error()
		}
		if counts[vm.Name] != 1 || vm.Name == "" {
			item.Adoptable = false
			item.Reason = "VM name is empty or ambiguous on this endpoint"
		}
		if p.identities != nil {
			id, err := p.identities.LoadIdentity(ctx, vm.Name)
			if err != nil {
				return nil, err
			}
			if id != nil {
				item.IdentityRecorded = true
				item.Managed = id.Context == resolved && id.Node == vm.Node && id.VMID == vm.VMID && id.Identity == identity && id.Endpoint == normalizedEndpoint(h)
				item.Adoptable = false
				if item.Managed {
					item.Reason = "already managed"
				} else {
					item.Reason = "resource name is bound to a different immutable VM identity"
				}
			}
		}
		out = append(out, item)
	}
	return out, nil
}

// ObserveIdentity reads the explicitly selected adoption target, not global name routing.
func (p *Provider) ObserveIdentity(ctx context.Context, manifest *protocol.Resource, expected string) (*protocol.Resource, error) {
	actual, err := p.MigrationIdentity(ctx, manifest)
	if err != nil {
		return nil, err
	}
	if actual != expected {
		return nil, fmt.Errorf("VM identity changed during observation")
	}
	contextName, _ := manifest.Spec["context"].(string)
	h, resolved, err := p.handlerForContext(contextName)
	if err != nil {
		return nil, err
	}
	vm, err := uniqueVM(ctx, h, manifest.Metadata.Name)
	if err != nil {
		return nil, err
	}
	if vm == nil {
		return nil, providers.NotFound(kindVM, manifest.Metadata.Name)
	}
	raw, err := h.Client().GetVMConfigRaw(ctx, vm.Node, vm.VMID)
	if err != nil {
		return nil, err
	}
	actual, err = vmIdentity(vm, raw)
	if err != nil {
		return nil, err
	}
	if actual != expected {
		return nil, fmt.Errorf("VM identity changed during observation")
	}
	return observeVM(ctx, h, resolved, vm)
}
