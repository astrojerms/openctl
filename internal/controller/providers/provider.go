// Package providers defines the in-process Provider interface and registry
// that the controller uses to route resource operations to vendor-specific
// implementations. From Phase 2 onward, providers are Go packages compiled
// into the controller binary (option C from the design discussion); the
// existing exec-plugin model in plugins/ is being phased out.
package providers

import (
	"context"
	"fmt"
	"slices"
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

// ResourceRef is a fully-qualified (apiVersion, kind, name) reference to
// another resource. Used by Registry.ChildrenOf / OwnerRefOf to surface
// composition relationships through the gRPC API (arch Phase 8 scoped
// owner-ref plumbing) without forcing the caller to know provider
// apiVersion conventions.
type ResourceRef struct {
	APIVersion string
	Kind       string
	Name       string
}

// ChildrenLister is an optional provider interface that exposes the
// resources composed by (kind, name). Providers that don't compose other
// resources don't need to implement it. The k3s provider returns the
// VirtualMachine refs belonging to a Cluster; atomic providers return
// nothing.
type ChildrenLister interface {
	ChildrenOf(kind, name string) []ResourceRef
}

// ObservedOnly is an optional provider capability. Kinds returned by
// ObservedOnlyKinds() bypass the managed-only filter that ResourceService.List
// / Get / Watch apply — i.e. they show up regardless of whether openctl ever
// applied them. Used for kinds the controller discovers from the underlying
// platform (Proxmox hosts via /api2/json/nodes) and can never own.
type ObservedOnly interface {
	ObservedOnlyKinds() []string
}

// DryRunner is an optional provider capability for previewing what an
// Apply would do without performing it. Composite providers (k3s Cluster
// → VMs) implement this to surface per-child actions and the gates the
// user would need to enable. Atomic providers don't need to; the
// resource handler computes spec-level drift on its own.
//
// Returning a nil result with nil error means "no extra planning info";
// the handler proceeds with just the spec-level diff.
type DryRunner interface {
	DryRun(ctx context.Context, manifest *protocol.Resource) (*DryRunResult, error)
}

// DryRunResult carries provider-side planning info for DryRunApply. Maps
// 1:1 onto apiv1.DryRunApplyResponse.{children,required_gates,summary} —
// kept in this package so providers don't need to import the proto.
type DryRunResult struct {
	Children      []ChildAction
	RequiredGates []string
	Summary       string
}

// ChildAction mirrors apiv1.ChildAction. Verb is one of:
//   - "create" — child will be added
//   - "destroy" — child will be removed
//   - "respec" — child will be destroyed and recreated with new spec
//   - "no-op" — no change to this child
type ChildAction struct {
	Verb   string
	Kind   string
	Name   string
	Detail string
}

// Gate string constants. Use these in DryRunResult.RequiredGates to keep
// providers in sync with the proto/UI surface.
const (
	GateAllowDestructive = "allow_destructive"
	GateIKnowThisBreaks  = "i_know_this_breaks_the_cluster"
)

// Actioner is an optional provider capability for runtime actions on
// existing resources — start/stop/reboot for VMs, get-kubeconfig for
// clusters, console URL for VMs, etc. Distinct from Apply/Delete
// because these don't change the desired state; they operate on the
// live resource. Providers that don't expose any actions don't need
// to implement it.
type Actioner interface {
	// Actions returns the action names this provider supports for the
	// given kind. Return an empty slice for kinds that have no actions.
	Actions(kind string) []string

	// DoAction invokes the named action against a specific resource
	// and returns a structured result. Common shapes:
	//   - Simple fire-and-forget (start/stop): Message = task upid.
	//   - File-shaped (get-kubeconfig): DownloadContent + DownloadFilename.
	//   - Link-shaped (console): URL to open externally.
	// Only one of Message/URL/DownloadContent needs to be populated;
	// the UI dispatches on which field is non-empty.
	DoAction(ctx context.Context, kind, name, action string) (*ActionResult, error)
}

// ActionResult carries the structured output of a runtime action.
// Providers populate whichever fields fit — message for short text,
// url for external navigation, download_content+filename for file
// payloads. Empty struct is legal ("action ran, nothing to show").
type ActionResult struct {
	Message          string
	URL              string
	DownloadContent  string
	DownloadFilename string
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

// OwnerRefOf returns the typed ResourceRef of the owner of (kind, name),
// or owned=false if nothing claims it. The apiVersion is derived from the
// responding provider's Name() using the documented `<name>.openctl.io/v1`
// convention — Registry-level callers (the UI/HTTP layer) want full refs
// for navigation, OwnerOf's three-tuple is preserved for the delete-block
// call site.
func (r *Registry) OwnerRefOf(kind, name string) (ResourceRef, bool) {
	for _, p := range r.providers {
		checker, ok := p.(OwnershipChecker)
		if !ok {
			continue
		}
		ownerKind, ownerName, owns := checker.OwnerOf(kind, name)
		if !owns {
			continue
		}
		return ResourceRef{
			APIVersion: providerAPIVersion(p),
			Kind:       ownerKind,
			Name:       ownerName,
		}, true
	}
	return ResourceRef{}, false
}

// ChildrenOf aggregates ChildrenLister results across all registered
// providers. Empty if nothing composes (kind, name) or no provider
// implements the interface. Caller deduplicates if needed; today providers
// own disjoint child sets so duplicates are unexpected.
func (r *Registry) ChildrenOf(kind, name string) []ResourceRef {
	var out []ResourceRef
	for _, p := range r.providers {
		if lister, ok := p.(ChildrenLister); ok {
			out = append(out, lister.ChildrenOf(kind, name)...)
		}
	}
	return out
}

// ActionsFor returns the supported action names for (apiVersion, kind).
// Empty when no provider matches or the provider doesn't implement
// Actioner. Callers should treat an empty list as "no actions"; UI hides
// the action bar in that case.
func (r *Registry) ActionsFor(apiVersion, kind string) []string {
	p, err := r.For(apiVersion)
	if err != nil {
		return nil
	}
	a, ok := p.(Actioner)
	if !ok {
		return nil
	}
	return a.Actions(kind)
}

// DoAction routes an action invocation to the responsible provider.
// Returns FailedPrecondition-flavored errors when the provider doesn't
// implement Actioner or the action isn't in its supported list, so the
// server layer can map cleanly to gRPC status codes.
func (r *Registry) DoAction(ctx context.Context, apiVersion, kind, name, action string) (*ActionResult, error) {
	p, err := r.For(apiVersion)
	if err != nil {
		return nil, err
	}
	a, ok := p.(Actioner)
	if !ok {
		return nil, fmt.Errorf("provider %q does not support runtime actions", p.Name())
	}
	if !slices.Contains(a.Actions(kind), action) {
		return nil, fmt.Errorf("action %q not supported for kind %q", action, kind)
	}
	return a.DoAction(ctx, kind, name, action)
}

// Get is a Registry-level convenience that dispatches to the right
// provider's Get method. Implements the refs.Getter interface so the
// ResourceRef resolver can look up any resource without knowing which
// provider owns it. Returns the wrapped provider error unchanged so
// callers can still errors.As it into a *NotFoundError.
func (r *Registry) Get(ctx context.Context, apiVersion, kind, name string) (*protocol.Resource, error) {
	p, err := r.For(apiVersion)
	if err != nil {
		return nil, err
	}
	return p.Get(ctx, kind, name)
}

// IsObservedOnly reports whether (apiVersion, kind) belongs to a provider
// that declared the kind observed-only. False when no provider matches the
// apiVersion or the provider doesn't implement ObservedOnly.
func (r *Registry) IsObservedOnly(apiVersion, kind string) bool {
	p, err := r.For(apiVersion)
	if err != nil {
		return false
	}
	oo, ok := p.(ObservedOnly)
	if !ok {
		return false
	}
	return slices.Contains(oo.ObservedOnlyKinds(), kind)
}

// providerAPIVersion returns the canonical apiVersion for a provider,
// derived from its short Name() and the documented openctl convention
// `<name>.openctl.io/v1`. Mirrors providerNameFromAPIVersion in the
// other direction.
func providerAPIVersion(p Provider) string {
	return p.Name() + ".openctl.io/v1"
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
