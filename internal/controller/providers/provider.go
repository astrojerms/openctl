// Package providers defines the in-process Provider interface and registry
// that the controller uses to route resource operations to vendor-specific
// implementations. From Phase 2 onward, providers are Go packages compiled
// into the controller binary (option C from the design discussion); the
// existing exec-plugin model in plugins/ is being phased out.
package providers

import (
	"context"
	"fmt"
	"strings"

	"github.com/openctl/openctl/pkg/protocol"
)

// OwnershipChecker is implemented by providers that own resources managed
// by other providers — e.g. the k3s provider owns proxmox VMs that belong
// to a Cluster. Before deleting any resource, the resource handler asks
// every registered provider whether it owns the target; if any does, the
// delete is rejected with FailedPrecondition.
//
// This is an optional interface — providers without children don't need
// to implement it.
type OwnershipChecker interface {
	// OwnerOf reports whether this provider owns the given (kind, name).
	// Returns the owner's (kind, name) when owned=true.
	OwnerOf(kind, name string) (ownerKind, ownerName string, owned bool)
}

// Provider implements operations for resources of a particular vendor
// (proxmox, k3s, etc). Phase 2 semantics are synchronous; Phase 3 layers
// async operations + persisted state on top.
type Provider interface {
	// Name returns the short provider identifier, matching the prefix in
	// the resource's apiVersion. e.g. for `proxmox.openctl.io/v1`, Name
	// returns "proxmox".
	Name() string

	// Kinds returns the resource kinds this provider handles.
	Kinds() []string

	// Apply creates a resource. Phase 2 is no-op-on-existing per the
	// architectural decision: if a resource with the manifest's name
	// already exists, return the observed state without mutating it.
	Apply(ctx context.Context, manifest *protocol.Resource) (*protocol.Resource, error)

	// Get returns the observed state of a resource by name.
	Get(ctx context.Context, kind, name string) (*protocol.Resource, error)

	// List returns all observed resources of a kind.
	List(ctx context.Context, kind string) ([]*protocol.Resource, error)

	// Delete removes a resource by name. Idempotent: delete-on-missing
	// returns nil, not an error.
	Delete(ctx context.Context, kind, name string) error
}

// Registry maps an apiVersion prefix to a Provider implementation.
// Lookup splits apiVersion on the first dot — `proxmox.openctl.io/v1` →
// "proxmox" → Provider.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{providers: map[string]Provider{}}
}

// Register adds a provider. Panics on duplicate registration so config
// errors surface at startup, not on first request.
func (r *Registry) Register(p Provider) {
	name := p.Name()
	if _, exists := r.providers[name]; exists {
		panic(fmt.Sprintf("providers: duplicate registration for %q", name))
	}
	r.providers[name] = p
}

// For returns the Provider matching the given apiVersion. Returns an error
// if no provider is registered.
func (r *Registry) For(apiVersion string) (Provider, error) {
	name := providerNameFromAPIVersion(apiVersion)
	if name == "" {
		return nil, fmt.Errorf("invalid apiVersion %q (expected `<provider>.openctl.io/<version>`)", apiVersion)
	}
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("no provider registered for apiVersion %q (have: %s)",
			apiVersion, strings.Join(r.knownNames(), ", "))
	}
	return p, nil
}

func (r *Registry) knownNames() []string {
	out := make([]string, 0, len(r.providers))
	for name := range r.providers {
		out = append(out, name)
	}
	return out
}

// OwnerOf asks every registered OwnershipChecker whether it owns the given
// resource. The first hit wins; returns owned=false if no provider claims it.
func (r *Registry) OwnerOf(kind, name string) (ownerKind, ownerName string, owned bool) {
	for _, p := range r.providers {
		if checker, ok := p.(OwnershipChecker); ok {
			if k, n, owns := checker.OwnerOf(kind, name); owns {
				return k, n, true
			}
		}
	}
	return "", "", false
}

// providerNameFromAPIVersion extracts "proxmox" from `proxmox.openctl.io/v1`.
func providerNameFromAPIVersion(apiVersion string) string {
	if apiVersion == "" {
		return ""
	}
	dot := strings.IndexByte(apiVersion, '.')
	if dot <= 0 {
		return ""
	}
	return apiVersion[:dot]
}
