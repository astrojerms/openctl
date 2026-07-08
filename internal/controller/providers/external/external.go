// Package external adapts an out-of-process plugin binary (speaking the v2
// pluginproto protocol) to the in-process providers.Provider contract. To the
// rest of the controller — dispatcher, reconciler, UI, git mirror — an
// external provider is indistinguishable from a compiled-in one: the same
// door (Apply/Get/List/Delete + optional capabilities), a different
// implementer behind it.
//
// Capability exposure: the base adapter unconditionally implements every
// optional providers.* interface EXCEPT Planner, because those five degrade
// safely (empty/nil) and their mere presence never changes control flow.
// Planner is different — a Planner's presence can route dispatch through
// composite expansion — so it is added via a wrapper type only when the
// plugin advertises CapabilityPlan. See docs/plugin-architecture.md.
package external

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/pkg/pluginproto"
	"github.com/openctl/openctl/pkg/protocol"
)

// metaCallTimeout bounds the ctx-less metadata round-trips (OwnerOf,
// ChildrenOf) so a hung plugin can't wedge a delete or a children-graph
// request forever. These calls are cheap; 30s is generous.
const metaCallTimeout = 30 * time.Second

// StateStore persists opaque per-resource provider state for stateful external
// plugins (those advertising CapabilityState). Satisfied by
// internal/controller/providerstate.Store. When a plugin is stateful and a
// store is present, the adapter loads prior state before each Apply/Get/Delete
// and saves the returned state after — so openctl holds the blob the plugin
// hands back and replays it verbatim next call.
type StateStore interface {
	LoadState(ctx context.Context, apiVersion, kind, name string) (state, private []byte, schemaVersion int, err error)
	SaveState(ctx context.Context, apiVersion, kind, name string, state, private []byte, schemaVersion int) error
	DeleteState(ctx context.Context, apiVersion, kind, name string) error
}

// Provider is the base adapter. It owns one long-lived plugin Client.
type Provider struct {
	client   *pluginproto.Client
	name     string
	kinds    []string
	observed []string                 // kinds flagged observed-only in the handshake
	actions  map[string][]string      // kind -> supported action names
	advanced []providers.AdvancedKind // composite-child kinds declared in the handshake
	caps     map[string]bool          // advertised capability set
	store    StateStore               // nil for stateless plugins / when unset
}

// plannerProvider is the Planner-capable variant, returned by New only when
// the plugin advertises CapabilityPlan.
type plannerProvider struct{ *Provider }

// New builds a providers.Provider from a handshaked, configured Client and
// its HandshakeResult. store persists opaque provider state for stateful
// plugins (CapabilityState); pass nil for stateless plugins or when no store
// is available. Returns a plannerProvider when the plugin advertises
// CapabilityPlan, otherwise the base Provider.
func New(client *pluginproto.Client, hs *pluginproto.HandshakeResult, store StateStore) providers.Provider {
	p := &Provider{
		client:  client,
		name:    hs.ProviderName,
		caps:    make(map[string]bool, len(hs.Capabilities)),
		actions: map[string][]string{},
		store:   store,
	}
	for _, c := range hs.Capabilities {
		p.caps[c] = true
	}
	for _, k := range hs.Kinds {
		p.kinds = append(p.kinds, k.Kind)
		if k.Observed {
			p.observed = append(p.observed, k.Kind)
		}
		if len(k.Actions) > 0 {
			p.actions[k.Kind] = k.Actions
		}
		if k.OwnerKind != "" {
			p.advanced = append(p.advanced, providers.AdvancedKind{
				Kind:      k.Kind,
				OwnerKind: k.OwnerKind,
				Note:      k.AdvancedNote,
			})
		}
	}
	if p.caps[pluginproto.CapabilityPlan] {
		return &plannerProvider{p}
	}
	return p
}

// --- required providers.Provider methods ---

func (p *Provider) Name() string    { return p.name }
func (p *Provider) Kinds() []string { return p.kinds }

// AdvancedKinds implements providers.AdvancedKindDescriber, forwarding the
// composite-child declarations a plugin made in its handshake (KindInfo with a
// non-empty OwnerKind). Empty for plugins that declare no composites, so the
// interface is satisfied without changing their behavior.
func (p *Provider) AdvancedKinds() []providers.AdvancedKind { return p.advanced }

// apiVersion is the canonical apiVersion this provider's kinds live under,
// used to key the state store.
func (p *Provider) apiVersion() string { return p.name + ".openctl.io/v1" }

// stateful reports whether the adapter should round-trip opaque state through
// the store — i.e. the plugin advertised CapabilityState and a store is set.
func (p *Provider) stateful() bool {
	return p.store != nil && p.caps[pluginproto.CapabilityState]
}

func (p *Provider) Apply(ctx context.Context, manifest *protocol.Resource) (*protocol.Resource, error) {
	params := pluginproto.ApplyParams{Manifest: manifest}
	if p.stateful() {
		state, private, _, err := p.store.LoadState(ctx, p.apiVersion(), manifest.Kind, manifest.Metadata.Name)
		if err != nil {
			return nil, err
		}
		params.State, params.Private = state, private
	}
	res, err := p.client.Apply(ctx, params)
	if err != nil {
		return nil, err
	}
	if p.stateful() {
		if err := p.store.SaveState(ctx, p.apiVersion(), manifest.Kind, manifest.Metadata.Name, res.State, res.Private, 0); err != nil {
			return nil, fmt.Errorf("persist provider state: %w", err)
		}
	}
	return res.Resource, nil
}

func (p *Provider) Get(ctx context.Context, kind, name string) (*protocol.Resource, error) {
	params := pluginproto.GetParams{Kind: kind, Name: name}
	if p.stateful() {
		state, _, _, err := p.store.LoadState(ctx, p.apiVersion(), kind, name)
		if err != nil {
			return nil, err
		}
		params.State = state
	}
	res, err := p.client.Get(ctx, params)
	if err != nil {
		return nil, mapErr(kind, name, err)
	}
	// Persist refreshed state (Terraform ReadResource semantics) so drift
	// checks compare against the provider's latest view. Only when the plugin
	// actually returned refreshed state.
	if p.stateful() && len(res.State) > 0 {
		if err := p.store.SaveState(ctx, p.apiVersion(), kind, name, res.State, nil, 0); err != nil {
			return nil, fmt.Errorf("persist refreshed provider state: %w", err)
		}
	}
	return res.Resource, nil
}

func (p *Provider) List(ctx context.Context, kind string) ([]*protocol.Resource, error) {
	return p.client.List(ctx, kind)
}

func (p *Provider) Delete(ctx context.Context, kind, name string) error {
	params := pluginproto.DeleteParams{Kind: kind, Name: name}
	if p.stateful() {
		state, private, _, err := p.store.LoadState(ctx, p.apiVersion(), kind, name)
		if err != nil {
			return err
		}
		params.State, params.Private = state, private
	}
	if err := p.client.Delete(ctx, params); err != nil {
		return err
	}
	if p.stateful() {
		if err := p.store.DeleteState(ctx, p.apiVersion(), kind, name); err != nil {
			return fmt.Errorf("delete provider state: %w", err)
		}
	}
	return nil
}

// mapErr translates a plugin CodeNotFound error into providers.NotFoundError
// so the server layer maps it to gRPC NotFound, matching the in-process
// providers' contract. Any other error passes through unchanged.
func mapErr(kind, name string, err error) error {
	var e *pluginproto.Error
	if errors.As(err, &e) && e.Code == pluginproto.CodeNotFound {
		return providers.NotFound(kind, name)
	}
	return err
}

// --- optional capabilities (base, always implemented) ---

// OwnerOf implements providers.OwnershipChecker. No-op (unowned) unless the
// plugin advertised ownership. A failed probe returns unowned rather than
// blocking a delete — the in-process interface has no error channel, and a
// plugin that can't answer shouldn't wedge deletes.
func (p *Provider) OwnerOf(kind, name string) (ownerKind, ownerName string, owned bool) {
	if !p.caps[pluginproto.CapabilityOwnership] {
		return "", "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), metaCallTimeout)
	defer cancel()
	res, err := p.client.OwnerOf(ctx, pluginproto.RefParams{Kind: kind, Name: name})
	if err != nil {
		return "", "", false
	}
	return res.OwnerKind, res.OwnerName, res.Owned
}

// ChildrenOf implements providers.ChildrenLister. Empty unless the plugin
// advertised children composition.
func (p *Provider) ChildrenOf(kind, name string) []providers.ResourceRef {
	if !p.caps[pluginproto.CapabilityChildren] {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), metaCallTimeout)
	defer cancel()
	refs, err := p.client.ChildrenOf(ctx, pluginproto.RefParams{Kind: kind, Name: name})
	if err != nil {
		return nil
	}
	out := make([]providers.ResourceRef, 0, len(refs))
	for _, r := range refs {
		out = append(out, providers.ResourceRef{APIVersion: r.APIVersion, Kind: r.Kind, Name: r.Name})
	}
	return out
}

// ObservedOnlyKinds implements providers.ObservedOnly, sourced from the
// handshake (no round-trip).
func (p *Provider) ObservedOnlyKinds() []string { return p.observed }

// Actions implements the query half of providers.Actioner, sourced from the
// handshake (no round-trip).
func (p *Provider) Actions(kind string) []string { return p.actions[kind] }

// DoAction implements the invoke half of providers.Actioner.
func (p *Provider) DoAction(ctx context.Context, kind, name, action string) (*providers.ActionResult, error) {
	res, err := p.client.DoAction(ctx, pluginproto.DoActionParams{Kind: kind, Name: name, Action: action})
	if err != nil {
		return nil, err
	}
	return &providers.ActionResult{
		Message:          res.Message,
		URL:              res.URL,
		DownloadContent:  res.DownloadContent,
		DownloadFilename: res.DownloadFilename,
	}, nil
}

// DryRun implements providers.DryRunner. When the plugin doesn't advertise
// dryRun, returns (nil, nil) — the documented "no extra planning info" signal
// that makes the handler fall back to its own spec-level diff.
func (p *Provider) DryRun(ctx context.Context, manifest *protocol.Resource) (*providers.DryRunResult, error) {
	if !p.caps[pluginproto.CapabilityDryRun] {
		return nil, nil
	}
	res, err := p.client.DryRun(ctx, manifest)
	if err != nil {
		return nil, err
	}
	out := &providers.DryRunResult{RequiredGates: res.RequiredGates, Summary: res.Summary}
	for _, c := range res.Children {
		out.Children = append(out.Children, providers.ChildAction{
			Verb: c.Verb, Kind: c.Kind, Name: c.Name, Detail: c.Detail,
		})
	}
	return out, nil
}

// Plan implements providers.Planner on the planner variant only.
func (pp *plannerProvider) Plan(ctx context.Context, manifest *protocol.Resource) (*providers.PlanResult, error) {
	res, err := pp.client.Plan(ctx, manifest)
	if err != nil {
		return nil, err
	}
	return &providers.PlanResult{Children: res.Children}, nil
}
