// Package proxmox is the in-process Provider implementation for Proxmox VE
// resources. Phase 2 supports VirtualMachine; Template support follows.
//
// The implementation is a thin adapter over pkg/proxmox/handler.Handler,
// which holds the bulk of the proxmox business logic. The same handler is
// used today by the legacy exec'd `openctl-proxmox` plugin; this provider
// is what supersedes that plugin once the controller takes over (Phase 6).
package proxmox

import (
	"context"
	"fmt"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/pkg/protocol"
	pmhandler "github.com/openctl/openctl/pkg/proxmox/handler"
)

const (
	providerName = "proxmox"
	kindVM       = "VirtualMachine"
	kindNode     = "ProxmoxNode"
)

// Config holds the per-provider configuration the controller passes through.
// In Phase 2 the controller doesn't yet load this from anywhere — tests and
// callers pass it in directly. A later phase will surface it via the API
// (per-context credentials, etc).
type Config = protocol.ProviderConfig

// Provider implements providers.Provider for Proxmox.
type Provider struct {
	handler *pmhandler.Handler
}

// New constructs a Provider with the given Proxmox endpoint configuration.
func New(cfg *Config) *Provider {
	return &Provider{handler: pmhandler.New(cfg)}
}

func (p *Provider) Name() string    { return providerName }
func (p *Provider) Kinds() []string { return []string{kindVM, kindNode} }

// Apply creates a VM if missing; otherwise returns the observed state
// without mutating (per the no-op-on-existing rule). ProxmoxNode is
// observed-only and rejects Apply.
func (p *Provider) Apply(_ context.Context, manifest *protocol.Resource) (*protocol.Resource, error) {
	if manifest.Kind == kindNode {
		return nil, fmt.Errorf("%s is observed-only; cannot be applied", kindNode)
	}
	if err := requireKindVM(manifest.Kind); err != nil {
		return nil, err
	}
	resp, err := p.handler.Handle(&protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionApply,
		ResourceType: manifest.Kind,
		ResourceName: manifest.Metadata.Name,
		Manifest:     manifest,
	})
	if err != nil {
		return nil, err
	}
	if resp.Status == protocol.StatusError {
		return nil, fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Resource, nil
}

// Get returns the current observed state of a VM or ProxmoxNode. Returns
// providers.NotFound when Proxmox has no resource with the given name, so
// the gRPC layer can map to codes.NotFound.
func (p *Provider) Get(_ context.Context, kind, name string) (*protocol.Resource, error) {
	if err := requireKnownKind(kind); err != nil {
		return nil, err
	}
	resp, err := p.handler.Handle(&protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionGet,
		ResourceType: kind,
		ResourceName: name,
	})
	if err != nil {
		return nil, err
	}
	if resp.Status == protocol.StatusError {
		if resp.Error.Code == protocol.ErrorCodeNotFound {
			return nil, providers.NotFound(kind, name)
		}
		return nil, fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Resource, nil
}

// List returns all observed resources of the given kind.
func (p *Provider) List(_ context.Context, kind string) ([]*protocol.Resource, error) {
	if err := requireKnownKind(kind); err != nil {
		return nil, err
	}
	resp, err := p.handler.Handle(&protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionList,
		ResourceType: kind,
	})
	if err != nil {
		return nil, err
	}
	if resp.Status == protocol.StatusError {
		return nil, fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Resources, nil
}

// Delete removes a VM. Idempotent — delete on a missing VM returns nil.
// ProxmoxNode is observed-only and rejects Delete.
func (p *Provider) Delete(_ context.Context, kind, name string) error {
	if kind == kindNode {
		return fmt.Errorf("%s is observed-only; cannot be deleted", kindNode)
	}
	if err := requireKindVM(kind); err != nil {
		return err
	}
	resp, err := p.handler.Handle(&protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionDelete,
		ResourceType: kind,
		ResourceName: name,
	})
	if err != nil {
		return err
	}
	if resp.Status == protocol.StatusError {
		if resp.Error.Code == protocol.ErrorCodeNotFound {
			return nil // idempotent
		}
		return fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}
	return nil
}

// ChildrenOf implements providers.ChildrenLister: returns the VirtualMachine
// resources hosted on the named ProxmoxNode. Empty when kind isn't
// ProxmoxNode, the node doesn't exist, or no VMs live there. Each ref
// carries the proxmox apiVersion so callers can navigate directly.
func (p *Provider) ChildrenOf(kind, name string) []providers.ResourceRef {
	if kind != kindNode {
		return nil
	}
	vms, err := p.List(context.Background(), kindVM)
	if err != nil {
		return nil
	}
	var out []providers.ResourceRef
	for _, vm := range vms {
		nodeName, _ := vm.Spec["node"].(string)
		if nodeName != name {
			continue
		}
		out = append(out, providers.ResourceRef{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       kindVM,
			Name:       vm.Metadata.Name,
		})
	}
	return out
}

// requireKindVM rejects anything that isn't VirtualMachine. Used by code
// paths that only handle VMs (Apply for non-Node kinds, Delete).
func requireKindVM(got string) error {
	if got != kindVM {
		return fmt.Errorf("proxmox provider does not handle kind %q for this operation (only %q)", got, kindVM)
	}
	return nil
}

// requireKnownKind rejects anything outside the provider's Kinds() set.
// Used by read-only paths (Get, List) that serve both VMs and Nodes.
func requireKnownKind(got string) error {
	if got != kindVM && got != kindNode {
		return fmt.Errorf("proxmox provider does not handle kind %q (only %q, %q)", got, kindVM, kindNode)
	}
	return nil
}
