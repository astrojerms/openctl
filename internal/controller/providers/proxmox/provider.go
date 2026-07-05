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
	"sort"
	"strings"
	"sync"

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

// Provider implements providers.Provider for Proxmox. It can route resources
// to one of several Proxmox endpoints ("contexts"): a resource's
// spec.context selects the endpoint, and reads that arrive without a context
// (Get/List/DoAction by name) resolve the owning endpoint by consulting an
// in-process index populated on Apply/List, falling back to querying every
// endpoint. With a single configured context this collapses to the original
// single-endpoint behavior.
type Provider struct {
	handlers   map[string]*pmhandler.Handler // context name -> handler
	order      []string                      // context names, sorted, for stable read scans
	defaultCtx string                        // context used when a manifest omits spec.context
	index      sync.Map                      // VM name -> context name (read-routing cache)
}

// New constructs a single-context Provider from one endpoint configuration.
// Kept for the common single-endpoint case, tests, and CLI direct-apply.
func New(cfg *Config) *Provider {
	return NewMulti(map[string]*Config{"": cfg}, "")
}

// NewMulti constructs a Provider spanning several Proxmox endpoints, keyed by
// context name. A resource's spec.context selects its endpoint; an empty
// context selects defaultContext (or the sole context when only one is
// configured). Reads by name resolve the endpoint via the index/full-scan.
func NewMulti(configs map[string]*Config, defaultContext string) *Provider {
	handlers := make(map[string]*pmhandler.Handler, len(configs))
	order := make([]string, 0, len(configs))
	for name, cfg := range configs {
		handlers[name] = pmhandler.New(cfg)
		order = append(order, name)
	}
	sort.Strings(order)
	// A single configured context is unambiguously the default.
	if defaultContext == "" && len(order) == 1 {
		defaultContext = order[0]
	}
	return &Provider{handlers: handlers, order: order, defaultCtx: defaultContext}
}

// handlerForContext resolves the handler for a manifest's spec.context. An
// empty name selects the default context. Errors when the named context
// isn't configured, so a typo'd or missing endpoint fails fast at apply.
func (p *Provider) handlerForContext(name string) (*pmhandler.Handler, string, error) {
	if name == "" {
		name = p.defaultCtx
	}
	h, ok := p.handlers[name]
	if !ok {
		return nil, "", fmt.Errorf("proxmox: no configured context %q (have: %s)", name, strings.Join(p.order, ", "))
	}
	return h, name, nil
}

// getFrom performs a single-endpoint Get, translating a not-found response
// into (nil, nil) so callers can distinguish "absent here" from a real error.
func getFrom(ctx context.Context, h *pmhandler.Handler, kind, name string) (*protocol.Resource, error) {
	resp, err := h.Handle(ctx, &protocol.Request{
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
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Resource, nil
}

// locate finds the endpoint owning a named resource: it tries the cached
// context first, then scans every endpoint. Returns found=false (nil error)
// when no endpoint has the resource. A per-endpoint error doesn't abort the
// scan — a healthy endpoint can still resolve the resource — but is surfaced
// if no endpoint ultimately has it.
func (p *Provider) locate(ctx context.Context, kind, name string) (h *pmhandler.Handler, res *protocol.Resource, found bool, err error) {
	if v, ok := p.index.Load(name); ok {
		cached := v.(string)
		if hh, present := p.handlers[cached]; present {
			r, gErr := getFrom(ctx, hh, kind, name)
			if gErr == nil && r != nil {
				return hh, r, true, nil
			}
			// Stale or transient — fall through to a full scan.
		}
	}
	var firstErr error
	for _, c := range p.order {
		hh := p.handlers[c]
		r, gErr := getFrom(ctx, hh, kind, name)
		if gErr != nil {
			if firstErr == nil {
				firstErr = gErr
			}
			continue
		}
		if r != nil {
			p.index.Store(name, c)
			return hh, r, true, nil
		}
	}
	return nil, nil, false, firstErr
}

func (p *Provider) Name() string    { return providerName }
func (p *Provider) Kinds() []string { return []string{kindVM, kindNode} }

// ObservedOnlyKinds implements providers.ObservedOnly: ProxmoxNode bypasses
// the managed-only filter because openctl discovers Proxmox hosts from the
// API rather than provisioning them — they can never be in applied_manifests.
func (p *Provider) ObservedOnlyKinds() []string { return []string{kindNode} }

// Actions implements providers.Actioner. VirtualMachine supports the
// standard power-lifecycle set plus "console" (opens Proxmox noVNC in
// a new tab); ProxmoxNode has no runtime actions.
func (p *Provider) Actions(kind string) []string {
	if kind != kindVM {
		return nil
	}
	// Ordering matters for the UI — buttons render in this order.
	return []string{"start", "shutdown", "stop", "reboot", "console"}
}

// DoAction implements providers.Actioner. Looks up the VM to get its
// node+vmid, then either dispatches to the appropriate client method
// (lifecycle actions return a Proxmox task UPID as the message) or
// constructs an external URL (console — returns a noVNC link that
// opens in a new tab).
func (p *Provider) DoAction(ctx context.Context, kind, name, action string) (*providers.ActionResult, error) {
	if kind != kindVM {
		return nil, fmt.Errorf("no actions for kind %q", kind)
	}
	h, _, found, err := p.locate(ctx, kindVM, name)
	if err != nil {
		return nil, fmt.Errorf("locate VM %q: %w", name, err)
	}
	if !found {
		return nil, fmt.Errorf("get VM %q: %w", name, providers.NotFound(kindVM, name))
	}
	vm, err := h.Client().GetVM(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("get VM %q: %w", name, err)
	}
	client := h.Client()
	switch action {
	case "start":
		upid, err := client.StartVM(ctx, vm.Node, vm.VMID)
		if err != nil {
			return nil, err
		}
		return &providers.ActionResult{Message: upid}, nil
	case "stop":
		upid, err := client.StopVM(ctx, vm.Node, vm.VMID)
		if err != nil {
			return nil, err
		}
		return &providers.ActionResult{Message: upid}, nil
	case "shutdown":
		upid, err := client.ShutdownVM(ctx, vm.Node, vm.VMID)
		if err != nil {
			return nil, err
		}
		return &providers.ActionResult{Message: upid}, nil
	case "reboot":
		upid, err := client.RebootVM(ctx, vm.Node, vm.VMID)
		if err != nil {
			return nil, err
		}
		return &providers.ActionResult{Message: upid}, nil
	case "console":
		// Proxmox noVNC URL. User must already be logged into the
		// Proxmox web UI (openctl doesn't proxy the session). The URL
		// is what Proxmox's own web UI generates when you click Console.
		endpoint := h.Config().Endpoint
		url := fmt.Sprintf("%s/?console=kvm&novnc=1&vmid=%d&node=%s", endpoint, vm.VMID, vm.Node)
		return &providers.ActionResult{URL: url, Message: "Opening Proxmox noVNC console…"}, nil
	default:
		return nil, fmt.Errorf("unknown action %q", action)
	}
}

// Apply creates a VM if missing; otherwise returns the observed state
// without mutating (per the no-op-on-existing rule). ProxmoxNode is
// observed-only and rejects Apply.
func (p *Provider) Apply(ctx context.Context, manifest *protocol.Resource) (*protocol.Resource, error) {
	if manifest.Kind == kindNode {
		return nil, fmt.Errorf("%s is observed-only; cannot be applied", kindNode)
	}
	if err := requireKindVM(manifest.Kind); err != nil {
		return nil, err
	}
	// spec.context selects the endpoint this VM lands on. Empty = default.
	ctxName, _ := manifest.Spec["context"].(string)
	h, resolved, err := p.handlerForContext(ctxName)
	if err != nil {
		return nil, err
	}
	resp, err := h.Handle(ctx, &protocol.Request{
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
	// Remember where this VM lives so later reads route directly.
	p.index.Store(manifest.Metadata.Name, resolved)
	return resp.Resource, nil
}

// Get returns the current observed state of a VM or ProxmoxNode. Returns
// providers.NotFound when Proxmox has no resource with the given name, so
// the gRPC layer can map to codes.NotFound.
func (p *Provider) Get(ctx context.Context, kind, name string) (*protocol.Resource, error) {
	if err := requireKnownKind(kind); err != nil {
		return nil, err
	}
	_, res, found, err := p.locate(ctx, kind, name)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, providers.NotFound(kind, name)
	}
	return res, nil
}

// List returns all observed resources of the given kind across every
// configured endpoint, merged. Listing is a completeness-sensitive query, so
// a failing endpoint aborts the call rather than silently dropping its
// resources. Each VM found also refreshes the read-routing index.
func (p *Provider) List(ctx context.Context, kind string) ([]*protocol.Resource, error) {
	if err := requireKnownKind(kind); err != nil {
		return nil, err
	}
	var out []*protocol.Resource
	for _, c := range p.order {
		resp, err := p.handlers[c].Handle(ctx, &protocol.Request{
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
		for _, r := range resp.Resources {
			if kind == kindVM && r != nil {
				p.index.Store(r.Metadata.Name, c)
			}
			out = append(out, r)
		}
	}
	return out, nil
}

// Delete removes a VM. Idempotent — delete on a missing VM returns nil.
// ProxmoxNode is observed-only and rejects Delete.
func (p *Provider) Delete(ctx context.Context, kind, name string) error {
	if kind == kindNode {
		return fmt.Errorf("%s is observed-only; cannot be deleted", kindNode)
	}
	if err := requireKindVM(kind); err != nil {
		return err
	}
	// Find which endpoint owns the VM, then delete there. Absent from every
	// endpoint → nothing to do (idempotent).
	h, _, found, err := p.locate(ctx, kindVM, name)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	resp, err := h.Handle(ctx, &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionDelete,
		ResourceType: kind,
		ResourceName: name,
	})
	if err != nil {
		return err
	}
	p.index.Delete(name)
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
