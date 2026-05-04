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
func (p *Provider) Kinds() []string { return []string{kindVM} }

// Apply creates a VM if missing; otherwise returns the observed state
// without mutating (per the no-op-on-existing rule).
func (p *Provider) Apply(_ context.Context, manifest *protocol.Resource) (*protocol.Resource, error) {
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

// Get returns the current observed state of a VM. Returns providers.NotFound
// when Proxmox has no VM with the given name, so the gRPC layer can map to
// codes.NotFound.
func (p *Provider) Get(_ context.Context, kind, name string) (*protocol.Resource, error) {
	if err := requireKindVM(kind); err != nil {
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

// List returns all observed VMs.
func (p *Provider) List(_ context.Context, kind string) ([]*protocol.Resource, error) {
	if err := requireKindVM(kind); err != nil {
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
func (p *Provider) Delete(_ context.Context, kind, name string) error {
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

// requireKindVM rejects unknown kinds. Phase 2 only handles VirtualMachine;
// later phases will accept Template etc.
func requireKindVM(got string) error {
	if got != kindVM {
		return fmt.Errorf("proxmox provider does not handle kind %q (only %q in Phase 2)", got, kindVM)
	}
	return nil
}
