package proxmox

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/openctl/openctl/internal/controller/provideridentity"
	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/pkg/protocol"
	pmclient "github.com/openctl/openctl/pkg/proxmox/client"
	pmhandler "github.com/openctl/openctl/pkg/proxmox/handler"
	pmresources "github.com/openctl/openctl/pkg/proxmox/resources"
)

type IdentityStore interface {
	LoadIdentity(context.Context, string) (*provideridentity.Identity, error)
	SaveIdentity(context.Context, string, *provideridentity.Identity) error
	DeleteIdentity(context.Context, string) error
}

// SetIdentityStore is startup-only, before any provider operations begin.
func (p *Provider) SetIdentityStore(store IdentityStore) { p.identities = store }

func isMissingVM(err error) bool {
	var missing *providers.NotFoundError
	return errors.As(err, &missing)
}

func normalizedEndpoint(h *pmhandler.Handler) string {
	return strings.TrimRight(h.Config().Endpoint, "/")
}

func vmIdentity(vm *pmclient.VM, raw map[string]any) (string, error) {
	if vm.Template != 0 || vm.Node == "" || vm.VMID <= 0 {
		return "", fmt.Errorf("identity requires a non-template VM with node and VMID")
	}
	smbios, _ := raw["smbios1"].(string)
	var value string
	for part := range strings.SplitSeq(smbios, ",") {
		key, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && key == "uuid" {
			if value != "" {
				return "", fmt.Errorf("duplicate SMBIOS UUID")
			}
			value = v
		}
	}
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		return "", fmt.Errorf("missing or invalid SMBIOS UUID")
	}
	return fmt.Sprintf("proxmox:%s:%d:%s", vm.Node, vm.VMID, id.String()), nil
}

func uniqueVM(ctx context.Context, h *pmhandler.Handler, name string) (*pmclient.VM, error) {
	vms, err := h.Client().ListVMs(ctx)
	if err != nil {
		return nil, err
	}
	var found *pmclient.VM
	for _, vm := range vms {
		if vm.Name != name {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("VM name %q is ambiguous on the selected endpoint", name)
		}
		found = vm
	}
	return found, nil
}

func observeVM(ctx context.Context, h *pmhandler.Handler, contextName string, vm *pmclient.VM) (*protocol.Resource, error) {
	cfg, err := h.Client().GetVMConfig(ctx, vm.Node, vm.VMID)
	if err != nil {
		return nil, err
	}
	var ip string
	if vm.Status == "running" {
		ip, _ = h.Client().GetVMIPAddress(ctx, vm.Node, vm.VMID)
	}
	res := pmresources.VMToResourceWithIP(vm, cfg, ip)
	res.Spec["context"] = contextName
	return res, nil
}

func (p *Provider) boundVM(ctx context.Context, name string) (*pmhandler.Handler, *pmclient.VM, *provideridentity.Identity, error) {
	if p.identities == nil {
		return nil, nil, nil, nil
	}
	id, err := p.identities.LoadIdentity(ctx, name)
	if err != nil || id == nil {
		return nil, nil, id, err
	}
	h, ok := p.handlers[id.Context]
	if !ok || strings.TrimRight(h.Config().Endpoint, "/") != id.Endpoint {
		return nil, nil, id, fmt.Errorf("VM %q recorded provider context/endpoint has changed; explicit adoption is required", name)
	}
	vms, err := h.Client().ListNodeVMs(ctx, id.Node)
	if err != nil {
		return nil, nil, id, err
	}
	var found *pmclient.VM
	for _, vm := range vms {
		if vm.Node == id.Node && vm.VMID == id.VMID {
			found = vm
			break
		}
	}
	if found == nil {
		return h, nil, id, providers.NotFound(kindVM, name)
	}
	if found.Name != name || found.Template != 0 {
		return nil, nil, id, fmt.Errorf("VM %q recorded instance was renamed or replaced; refusing name replacement", name)
	}
	raw, err := h.Client().GetVMConfigRaw(ctx, id.Node, id.VMID)
	if err != nil {
		return nil, nil, id, err
	}
	actual, err := vmIdentity(found, raw)
	if err != nil {
		return nil, nil, id, err
	}
	if actual != id.Identity {
		return nil, nil, id, fmt.Errorf("VM %q immutable identity changed; refusing replacement", name)
	}
	return h, found, id, nil
}

func (p *Provider) BindIdentity(ctx context.Context, manifest *protocol.Resource, expected string) error {
	if p.identities == nil {
		return nil
	}
	id, err := p.readBinding(ctx, manifest, expected)
	if err != nil {
		return err
	}
	return p.identities.SaveIdentity(ctx, manifest.Metadata.Name, id)
}

func (p *Provider) ConfirmIdentity(ctx context.Context, manifest *protocol.Resource, expected string) error {
	p.mutationMu.Lock()
	defer p.mutationMu.Unlock()
	store, ok := p.identities.(interface {
		ConfirmIdentity(context.Context, string, *provideridentity.Identity) error
	})
	if !ok {
		return fmt.Errorf("identity confirmation requires an idle-aware identity store")
	}
	id, err := p.readBinding(ctx, manifest, expected)
	if err != nil {
		return err
	}
	return store.ConfirmIdentity(ctx, manifest.Metadata.Name, id)
}

func (p *Provider) readBinding(ctx context.Context, manifest *protocol.Resource, expected string) (*provideridentity.Identity, error) {
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
	node, _ := manifest.Spec["node"].(string)
	if node != vm.Node {
		return nil, fmt.Errorf("VM node differs from canonical placement")
	}
	raw, err := h.Client().GetVMConfigRaw(ctx, vm.Node, vm.VMID)
	if err != nil {
		return nil, err
	}
	identity, err := vmIdentity(vm, raw)
	if err != nil {
		return nil, err
	}
	if identity != expected {
		return nil, fmt.Errorf("VM identity changed before confirmation")
	}
	return &provideridentity.Identity{Context: resolved, Endpoint: normalizedEndpoint(h), Node: vm.Node, VMID: vm.VMID, Identity: identity}, nil
}

func (p *Provider) ReleaseIdentity(ctx context.Context, name string) error {
	if p.identities == nil {
		return nil
	}
	return p.identities.DeleteIdentity(ctx, name)
}

func (p *Provider) ensureNameAbsent(ctx context.Context, name, exceptContext string, skipContext bool) error {
	for _, contextName := range p.order {
		if skipContext && contextName == exceptContext {
			continue
		}
		vm, err := uniqueVM(ctx, p.handlers[contextName], name)
		if err != nil {
			return err
		}
		if vm != nil {
			return fmt.Errorf("VM name %q already exists in context %q; explicit adoption is required", name, contextName)
		}
	}
	return nil
}
