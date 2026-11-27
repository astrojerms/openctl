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
	"time"

	"github.com/openctl/openctl/internal/controller/provideridentity"
	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/pkg/protocol"
	pmclient "github.com/openctl/openctl/pkg/proxmox/client"
	pmhandler "github.com/openctl/openctl/pkg/proxmox/handler"
	pmresources "github.com/openctl/openctl/pkg/proxmox/resources"
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

// Provider routes managed VMs through durable immutable instance bindings.
// Unbound read-only name lookups scan every endpoint and reject ambiguity.
type Provider struct {
	handlers   map[string]*pmhandler.Handler // context name -> handler
	order      []string                      // context names, sorted, for stable read scans
	defaultCtx string                        // context used when a manifest omits spec.context
	identities IdentityStore
	mutationMu sync.Mutex
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
	if kind == kindVM {
		vm, err := uniqueVM(ctx, h, name)
		if err != nil || vm == nil {
			return nil, err
		}
		return observeVM(ctx, h, "", vm)
	}
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

// locate uses immutable management bindings when available. Otherwise it scans
// every endpoint, failing closed on errors or ambiguous names.
func (p *Provider) locate(ctx context.Context, kind, name string) (h *pmhandler.Handler, res *protocol.Resource, found bool, err error) {
	if kind == kindVM {
		hh, vm, id, bindErr := p.boundVM(ctx, name)
		if bindErr != nil {
			return nil, nil, false, bindErr
		}
		if id != nil {
			r, observeErr := observeVM(ctx, hh, id.Context, vm)
			return hh, r, observeErr == nil, observeErr
		}
	}
	for _, c := range p.order {
		hh := p.handlers[c]
		r, gErr := getFrom(ctx, hh, kind, name)
		if gErr != nil {
			return nil, nil, false, gErr
		}
		if r == nil {
			continue
		}
		if found {
			return nil, nil, false, fmt.Errorf("%s name %q is ambiguous across contexts", kind, name)
		}
		if r.Spec != nil {
			r.Spec["context"] = c
		}
		h, res, found = hh, r, true
	}
	return h, res, found, nil
}

func (p *Provider) Name() string    { return providerName }
func (p *Provider) Kinds() []string { return []string{kindVM, kindNode} }

// ObservedOnlyKinds implements providers.ObservedOnly: ProxmoxNode bypasses
// the managed-only filter because openctl discovers Proxmox hosts from the
// API rather than provisioning them — they can never be in applied_manifests.
func (p *Provider) ObservedOnlyKinds() []string { return []string{kindNode} }

// DriftSpecs compares observable VM settings, excluding creation-only inputs.
func (p *Provider) DriftSpecs(kind string, desired, observed map[string]any) (map[string]any, map[string]any) {
	if kind == kindVM {
		return pmresources.ComparableVMSpecs(desired, observed)
	}
	return desired, observed
}

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
	h, vm, id, err := p.boundVM(ctx, name)
	if err != nil {
		return nil, err
	}
	if id == nil {
		if p.identities != nil {
			return nil, fmt.Errorf("VM %q has no recorded management identity; adopt it first", name)
		}
		var found bool
		h, _, found, err = p.locate(ctx, kindVM, name)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, providers.NotFound(kindVM, name)
		}
		vm, err = uniqueVM(ctx, h, name)
		if err != nil {
			return nil, err
		}
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

// Apply creates a missing VM or resizes an existing VM's CPU, memory, and disks,
// then returns observed state. ProxmoxNode is observed-only and rejects Apply.
func (p *Provider) Apply(ctx context.Context, manifest *protocol.Resource) (*protocol.Resource, error) {
	if manifest.Kind == kindNode {
		return nil, fmt.Errorf("%s is observed-only; cannot be applied", kindNode)
	}
	if err := requireKindVM(manifest.Kind); err != nil {
		return nil, err
	}
	p.mutationMu.Lock()
	defer p.mutationMu.Unlock()
	ctxName, _ := manifest.Spec["context"].(string)
	h, resolved, err := p.handlerForContext(ctxName)
	if err != nil {
		return nil, err
	}
	boundHandler, vm, id, err := p.boundVM(ctx, manifest.Metadata.Name)
	if err != nil {
		return nil, err
	}
	if id != nil {
		node, _ := manifest.Spec["node"].(string)
		if resolved != id.Context || (node != "" && node != id.Node) {
			return nil, fmt.Errorf("VM placement is immutable; explicit migration is required")
		}
		if err := boundHandler.UpdateVM(ctx, vm, manifest); err != nil {
			return nil, err
		}
		return observeVM(ctx, boundHandler, id.Context, vm)
	}
	vm, err = uniqueVM(ctx, h, manifest.Metadata.Name)
	if err != nil {
		return nil, err
	}
	if vm != nil {
		if p.identities != nil {
			return nil, fmt.Errorf("VM %q already exists without a recorded identity; use adoption, not Apply", manifest.Metadata.Name)
		}
		if err := h.UpdateVM(ctx, vm, manifest); err != nil {
			return nil, err
		}
		return observeVM(ctx, h, resolved, vm)
	}
	if p.identities != nil {
		if err := p.ensureNameAbsent(ctx, manifest.Metadata.Name, resolved, true); err != nil {
			return nil, err
		}
	}
	// Explicit create avoids a second name lookup turning creation into adoption.
	resp, err := h.Handle(ctx, &protocol.Request{
		Version: protocol.ProtocolVersion, Action: protocol.ActionCreate,
		ResourceType: manifest.Kind, ResourceName: manifest.Metadata.Name, Manifest: manifest,
	})
	if err != nil {
		return nil, err
	}
	if resp.Status == protocol.StatusError {
		return nil, fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}
	if resp.Resource == nil {
		return nil, fmt.Errorf("created VM response omitted its allocated identity")
	}
	vmid, _ := resp.Resource.Status["vmid"].(int)
	node, _ := resp.Resource.Spec["node"].(string)
	if vmid <= 0 || node == "" {
		return nil, fmt.Errorf("created VM response omitted node or VMID")
	}
	vm = &pmclient.VM{Name: manifest.Metadata.Name, VMID: vmid, Node: node}
	if p.identities != nil {
		raw, err := h.Client().GetVMConfigRaw(ctx, node, vmid)
		if err != nil {
			return nil, fmt.Errorf("created VM identity could not be read: %w", err)
		}
		identity, err := vmIdentity(vm, raw)
		if err != nil {
			return nil, fmt.Errorf("created VM identity could not be verified: %w", err)
		}
		if err := p.identities.SaveIdentity(ctx, manifest.Metadata.Name, &provideridentity.Identity{Context: resolved, Endpoint: strings.TrimRight(h.Config().Endpoint, "/"), Node: node, VMID: vmid, Identity: identity}); err != nil {
			return nil, err
		}
	}
	// Read live status by the allocated node/VMID, never by the submitted name.
	vms, err := h.Client().ListVMs(ctx)
	if err != nil {
		return nil, fmt.Errorf("created VM status could not be observed: %w", err)
	}
	vm = nil
	for _, candidate := range vms {
		if candidate.Node == node && candidate.VMID == vmid {
			vm = candidate
			break
		}
	}
	if vm == nil || vm.Name != manifest.Metadata.Name {
		return nil, fmt.Errorf("created VM identity could not be observed")
	}
	return observeVM(ctx, h, resolved, vm)
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

// List returns observed resources across every endpoint, rejecting ambiguous
// names and verifying managed bindings rather than substituting replacements.
func (p *Provider) List(ctx context.Context, kind string) ([]*protocol.Resource, error) {
	if err := requireKnownKind(kind); err != nil {
		return nil, err
	}
	var out []*protocol.Resource
	seen := make(map[string]bool)
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
			if r == nil {
				continue
			}
			if seen[r.Metadata.Name] {
				return nil, fmt.Errorf("%s name %q is ambiguous across contexts", kind, r.Metadata.Name)
			}
			seen[r.Metadata.Name] = true
			r.Spec["context"] = c
			if kind == kindVM && p.identities != nil {
				_, _, id, err := p.boundVM(ctx, r.Metadata.Name)
				if err != nil {
					return nil, err
				}
				if id != nil && id.Context != c {
					return nil, fmt.Errorf("VM %q inventory conflicts with its recorded context", r.Metadata.Name)
				}
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
	p.mutationMu.Lock()
	defer p.mutationMu.Unlock()
	h, vm, id, err := p.boundVM(ctx, name)
	if err != nil {
		if !isMissingVM(err) || id == nil {
			return err
		}
		return p.ReleaseIdentity(ctx, name)
	}
	if id == nil {
		if p.identities != nil {
			// A retry after provider deletion and a failed manifest cleanup may
			// have no binding. Only proven global absence is an idempotent success.
			return p.ensureNameAbsent(ctx, name, "", false)
		}
		var found bool
		h, _, found, err = p.locate(ctx, kindVM, name)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		vm, err = uniqueVM(ctx, h, name)
		if err != nil {
			return err
		}
	}
	if vm.Status == "running" {
		upid, err := h.Client().StopVM(ctx, vm.Node, vm.VMID)
		if err != nil {
			return err
		}
		if upid != "" {
			if err := h.Client().WaitForTask(ctx, vm.Node, upid, 5*time.Minute); err != nil {
				return err
			}
		}
	}
	if err := h.Client().DeleteVM(ctx, vm.Node, vm.VMID); err != nil {
		return err
	}
	if err := p.ReleaseIdentity(ctx, name); err != nil {
		return err
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
