package external

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/pkg/pluginproto"
	"github.com/openctl/openctl/pkg/protocol"
)

// testHandler is a configurable in-memory plugin used to drive the adapter
// over an in-process pipe (no subprocess). Capability flags mirror what a real
// plugin would advertise in its handshake.
type testHandler struct {
	pluginproto.UnimplementedHandler
	name string
	caps []string

	store       map[string]*protocol.Resource
	ownerCalls  atomic.Int32
	configBytes atomic.Value // json.RawMessage
}

func newTestHandler(name string, caps ...string) *testHandler {
	return &testHandler{name: name, caps: caps, store: map[string]*protocol.Resource{}}
}

func (h *testHandler) Handshake(context.Context) (*pluginproto.HandshakeResult, error) {
	return &pluginproto.HandshakeResult{
		ProviderName:    h.name,
		ProtocolVersion: pluginproto.ProtocolVersion,
		Capabilities:    h.caps,
		Kinds: []pluginproto.KindInfo{
			{Kind: "Thing", Actions: []string{"restart"}},
			{Kind: "Node", Observed: true},
		},
	}, nil
}

func (h *testHandler) Configure(_ context.Context, config json.RawMessage) error {
	h.configBytes.Store(config)
	return nil
}

func (h *testHandler) Apply(_ context.Context, p pluginproto.ApplyParams) (*pluginproto.ApplyResult, error) {
	r := *p.Manifest
	r.Status = map[string]any{"phase": "Ready"}
	h.store[p.Manifest.Kind+"/"+p.Manifest.Metadata.Name] = &r
	return &pluginproto.ApplyResult{Resource: &r}, nil
}

func (h *testHandler) Get(_ context.Context, p pluginproto.GetParams) (*pluginproto.GetResult, error) {
	r, ok := h.store[p.Kind+"/"+p.Name]
	if !ok {
		return nil, pluginproto.NotFound(p.Kind + "/" + p.Name)
	}
	return &pluginproto.GetResult{Resource: r}, nil
}

func (h *testHandler) List(_ context.Context, kind string) ([]*protocol.Resource, error) {
	var out []*protocol.Resource
	for _, r := range h.store {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out, nil
}

func (h *testHandler) Delete(_ context.Context, p pluginproto.DeleteParams) error {
	delete(h.store, p.Kind+"/"+p.Name)
	return nil
}

func (h *testHandler) Plan(_ context.Context, manifest *protocol.Resource) (*pluginproto.PlanResult, error) {
	child := &protocol.Resource{APIVersion: manifest.APIVersion, Kind: "Thing"}
	child.Metadata.Name = manifest.Metadata.Name + "-child"
	return &pluginproto.PlanResult{Children: []*protocol.Resource{child}}, nil
}

func (h *testHandler) DryRun(_ context.Context, manifest *protocol.Resource) (*pluginproto.DryRunResult, error) {
	return &pluginproto.DryRunResult{
		Children:      []pluginproto.ChildAction{{Verb: "create", Kind: manifest.Kind, Name: manifest.Metadata.Name}},
		RequiredGates: []string{providers.GateAllowDestructive},
		Summary:       "preview",
	}, nil
}

func (h *testHandler) DoAction(_ context.Context, p pluginproto.DoActionParams) (*pluginproto.DoActionResult, error) {
	return &pluginproto.DoActionResult{Message: p.Action + ":" + p.Name}, nil
}

func (h *testHandler) OwnerOf(_ context.Context, p pluginproto.RefParams) (*pluginproto.OwnerOfResult, error) {
	h.ownerCalls.Add(1)
	if p.Name == "owned" {
		return &pluginproto.OwnerOfResult{OwnerKind: "Cluster", OwnerName: "c1", Owned: true}, nil
	}
	return &pluginproto.OwnerOfResult{}, nil
}

func (h *testHandler) ChildrenOf(_ context.Context, p pluginproto.RefParams) ([]pluginproto.ResourceRef, error) {
	return []pluginproto.ResourceRef{{APIVersion: "x.openctl.io/v1", Kind: "Thing", Name: p.Name + "-kid"}}, nil
}

// newAdapter wires a testHandler to an adapter Provider over in-memory pipes
// and returns the Provider plus a teardown func.
func newAdapter(t *testing.T, h *testHandler) (providers.Provider, func()) {
	t.Helper()
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		_ = pluginproto.ServeConn(context.Background(), c2sR, s2cW, h)
		_ = s2cW.Close()
	}()
	client := pluginproto.NewClient(s2cR, c2sW)
	hs, err := client.Handshake(context.Background())
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	prov := New(client, hs, nil)
	teardown := func() {
		_ = client.Close(context.Background())
		_ = c2sW.Close()
		<-serveDone
	}
	return prov, teardown
}

func TestAdapterSatisfiesProviderContract(t *testing.T) {
	prov, done := newAdapter(t, newTestHandler("demo"))
	defer done()

	if prov.Name() != "demo" {
		t.Errorf("Name = %q", prov.Name())
	}
	if len(prov.Kinds()) != 2 {
		t.Errorf("Kinds = %v", prov.Kinds())
	}
	// The base adapter always satisfies the safe-degrading optional interfaces.
	if _, ok := prov.(providers.OwnershipChecker); !ok {
		t.Error("adapter should implement OwnershipChecker")
	}
	if _, ok := prov.(providers.ChildrenLister); !ok {
		t.Error("adapter should implement ChildrenLister")
	}
	if _, ok := prov.(providers.ObservedOnly); !ok {
		t.Error("adapter should implement ObservedOnly")
	}
	if _, ok := prov.(providers.Actioner); !ok {
		t.Error("adapter should implement Actioner")
	}
	if _, ok := prov.(providers.DryRunner); !ok {
		t.Error("adapter should implement DryRunner")
	}
}

func TestAdapterCRUD(t *testing.T) {
	prov, done := newAdapter(t, newTestHandler("demo"))
	defer done()
	ctx := context.Background()

	m := &protocol.Resource{APIVersion: "demo.openctl.io/v1", Kind: "Thing"}
	m.Metadata.Name = "t1"
	applied, err := prov.Apply(ctx, m)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied.Status["phase"] != "Ready" {
		t.Errorf("apply status = %v", applied.Status)
	}

	got, err := prov.Get(ctx, "Thing", "t1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Metadata.Name != "t1" {
		t.Errorf("get name = %q", got.Metadata.Name)
	}

	list, err := prov.List(ctx, "Thing")
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v, %v", list, err)
	}

	if err := prov.Delete(ctx, "Thing", "t1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestAdapterGetNotFoundMapsToNotFoundError(t *testing.T) {
	prov, done := newAdapter(t, newTestHandler("demo"))
	defer done()

	_, err := prov.Get(context.Background(), "Thing", "ghost")
	var nf *providers.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v (%T), want *providers.NotFoundError", err, err)
	}
	if nf.Kind != "Thing" || nf.Name != "ghost" {
		t.Errorf("NotFoundError = %+v", nf)
	}
}

func TestPlannerGatedByCapability(t *testing.T) {
	// Without CapabilityPlan the adapter must NOT be a Planner.
	prov, done := newAdapter(t, newTestHandler("noplan"))
	defer done()
	if _, ok := prov.(providers.Planner); ok {
		t.Error("adapter without plan capability must not implement Planner")
	}
	done()

	// With CapabilityPlan it must be a Planner and Plan must work.
	prov2, done2 := newAdapter(t, newTestHandler("planny", pluginproto.CapabilityPlan))
	defer done2()
	pl, ok := prov2.(providers.Planner)
	if !ok {
		t.Fatal("adapter with plan capability must implement Planner")
	}
	m := &protocol.Resource{APIVersion: "planny.openctl.io/v1", Kind: "Cluster"}
	m.Metadata.Name = "c1"
	res, err := pl.Plan(context.Background(), m)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(res.Children) != 1 || res.Children[0].Metadata.Name != "c1-child" {
		t.Errorf("plan children = %+v", res.Children)
	}
}

func TestOwnershipGatingSkipsRoundTrip(t *testing.T) {
	// No ownership capability: OwnerOf must short-circuit without calling the
	// plugin at all.
	h := newTestHandler("demo")
	prov, done := newAdapter(t, h)
	defer done()
	if _, _, owned := prov.(providers.OwnershipChecker).OwnerOf("Thing", "owned"); owned {
		t.Error("expected unowned when capability absent")
	}
	if n := h.ownerCalls.Load(); n != 0 {
		t.Errorf("expected 0 owner round-trips without capability, got %d", n)
	}

	// With ownership capability: OwnerOf round-trips and reports ownership.
	h2 := newTestHandler("demo2", pluginproto.CapabilityOwnership)
	prov2, done2 := newAdapter(t, h2)
	defer done2()
	ok, on, owned := prov2.(providers.OwnershipChecker).OwnerOf("Thing", "owned")
	if !owned || ok != "Cluster" || on != "c1" {
		t.Errorf("owner = (%q,%q,%v)", ok, on, owned)
	}
	if n := h2.ownerCalls.Load(); n != 1 {
		t.Errorf("expected 1 owner round-trip, got %d", n)
	}
}

func TestObservedAndActionsFromHandshake(t *testing.T) {
	prov, done := newAdapter(t, newTestHandler("demo", pluginproto.CapabilityActions))
	defer done()

	oo := prov.(providers.ObservedOnly).ObservedOnlyKinds()
	if len(oo) != 1 || oo[0] != "Node" {
		t.Errorf("observed = %v", oo)
	}
	act := prov.(providers.Actioner).Actions("Thing")
	if len(act) != 1 || act[0] != "restart" {
		t.Errorf("actions = %v", act)
	}
	res, err := prov.(providers.Actioner).DoAction(context.Background(), "Thing", "t1", "restart")
	if err != nil {
		t.Fatalf("doAction: %v", err)
	}
	if res.Message != "restart:t1" {
		t.Errorf("action message = %q", res.Message)
	}
}

func TestDryRunGatedByCapability(t *testing.T) {
	// Without dryRun capability: returns (nil, nil) so the handler falls back
	// to its own spec-level diff.
	prov, done := newAdapter(t, newTestHandler("demo"))
	defer done()
	m := &protocol.Resource{APIVersion: "demo.openctl.io/v1", Kind: "Thing"}
	m.Metadata.Name = "t1"
	res, err := prov.(providers.DryRunner).DryRun(context.Background(), m)
	if err != nil || res != nil {
		t.Fatalf("expected (nil,nil) without capability, got (%v,%v)", res, err)
	}

	// With dryRun capability: returns the plugin's preview.
	prov2, done2 := newAdapter(t, newTestHandler("demo2", pluginproto.CapabilityDryRun))
	defer done2()
	res2, err := prov2.(providers.DryRunner).DryRun(context.Background(), m)
	if err != nil {
		t.Fatalf("dryRun: %v", err)
	}
	if res2.Summary != "preview" || len(res2.Children) != 1 {
		t.Errorf("dryRun result = %+v", res2)
	}
}

func TestAdapterThroughRegistry(t *testing.T) {
	// The adapter must dispatch correctly when registered in a real Registry
	// keyed by apiVersion prefix.
	prov, done := newAdapter(t, newTestHandler("acme"))
	defer done()
	reg := providers.NewRegistry()
	reg.Register(prov)

	p, err := reg.For("acme.openctl.io/v1")
	if err != nil {
		t.Fatalf("registry.For: %v", err)
	}
	m := &protocol.Resource{APIVersion: "acme.openctl.io/v1", Kind: "Thing"}
	m.Metadata.Name = "r1"
	if _, err := p.Apply(context.Background(), m); err != nil {
		t.Fatalf("apply via registry: %v", err)
	}
	if got, err := reg.Get(context.Background(), "acme.openctl.io/v1", "Thing", "r1"); err != nil || got.Metadata.Name != "r1" {
		t.Fatalf("get via registry = %v, %v", got, err)
	}
}

// --- stateful adapter (CapabilityState) ---

// memStore is an in-memory StateStore for testing state round-tripping.
type memStore struct {
	mu      sync.Mutex
	state   map[string][]byte
	private map[string][]byte
}

func newMemStore() *memStore {
	return &memStore{state: map[string][]byte{}, private: map[string][]byte{}}
}

func (m *memStore) key(a, k, n string) string { return a + "/" + k + "/" + n }

func (m *memStore) LoadState(_ context.Context, a, k, n string) ([]byte, []byte, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state[m.key(a, k, n)], m.private[m.key(a, k, n)], 0, nil
}

func (m *memStore) SaveState(_ context.Context, a, k, n string, state, private []byte, _ int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state[m.key(a, k, n)] = state
	m.private[m.key(a, k, n)] = private
	return nil
}

func (m *memStore) DeleteState(_ context.Context, a, k, n string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.state, m.key(a, k, n))
	delete(m.private, m.key(a, k, n))
	return nil
}

// statefulHandler records the prior state it received on each Apply and echoes
// a new state back, so the test can prove the adapter loaded-before and
// saved-after each call.
type statefulHandler struct {
	pluginproto.UnimplementedHandler
	mu        sync.Mutex
	priorSeen []string // JSON of the state blob seen on each Apply, in order
	applies   int
}

func (h *statefulHandler) Handshake(context.Context) (*pluginproto.HandshakeResult, error) {
	return &pluginproto.HandshakeResult{
		ProviderName:    "stateful",
		ProtocolVersion: pluginproto.ProtocolVersion,
		Capabilities:    []string{pluginproto.CapabilityState},
		Kinds:           []pluginproto.KindInfo{{Kind: "Thing"}},
	}, nil
}

func (h *statefulHandler) Apply(_ context.Context, p pluginproto.ApplyParams) (*pluginproto.ApplyResult, error) {
	h.mu.Lock()
	h.applies++
	n := h.applies
	h.priorSeen = append(h.priorSeen, string(p.State))
	h.mu.Unlock()
	r := *p.Manifest
	return &pluginproto.ApplyResult{
		Resource: &r,
		State:    json.RawMessage(`{"generation":` + itoa(n) + `}`),
		Private:  json.RawMessage(`"priv"`),
	}, nil
}

func (h *statefulHandler) Get(_ context.Context, p pluginproto.GetParams) (*pluginproto.GetResult, error) {
	r := &protocol.Resource{APIVersion: "stateful.openctl.io/v1", Kind: p.Kind}
	r.Metadata.Name = p.Name
	// Echo back a refreshed state so the adapter persists it.
	return &pluginproto.GetResult{Resource: r, State: json.RawMessage(`{"refreshed":true}`)}, nil
}

func (h *statefulHandler) Delete(context.Context, pluginproto.DeleteParams) error { return nil }
func (h *statefulHandler) List(context.Context, string) ([]*protocol.Resource, error) {
	return nil, nil
}

func itoa(n int) string { return string(rune('0' + n)) }

func newStatefulAdapter(t *testing.T, h pluginproto.Handler, store StateStore) (providers.Provider, func()) {
	t.Helper()
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		_ = pluginproto.ServeConn(context.Background(), c2sR, s2cW, h)
		_ = s2cW.Close()
	}()
	client := pluginproto.NewClient(s2cR, c2sW)
	hs, err := client.Handshake(context.Background())
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	prov := New(client, hs, store)
	return prov, func() {
		_ = client.Close(context.Background())
		_ = c2sW.Close()
		<-serveDone
	}
}

func TestStatefulAdapterRoundTripsState(t *testing.T) {
	store := newMemStore()
	h := &statefulHandler{}
	prov, done := newStatefulAdapter(t, h, store)
	defer done()
	ctx := context.Background()

	m := &protocol.Resource{APIVersion: "stateful.openctl.io/v1", Kind: "Thing"}
	m.Metadata.Name = "t1"

	// First Apply: no prior state, provider returns generation 1.
	if _, err := prov.Apply(ctx, m); err != nil {
		t.Fatalf("apply 1: %v", err)
	}
	st, priv, _, _ := store.LoadState(ctx, "stateful.openctl.io/v1", "Thing", "t1")
	if string(st) != `{"generation":1}` {
		t.Errorf("stored state after apply 1 = %s", st)
	}
	if string(priv) != `"priv"` {
		t.Errorf("stored private = %s", priv)
	}

	// Second Apply: the adapter must have loaded the saved state and handed it
	// to the plugin as prior state.
	if _, err := prov.Apply(ctx, m); err != nil {
		t.Fatalf("apply 2: %v", err)
	}
	h.mu.Lock()
	seen := append([]string(nil), h.priorSeen...)
	h.mu.Unlock()
	if len(seen) != 2 || seen[0] != "" || seen[1] != `{"generation":1}` {
		t.Errorf("prior state seen by plugin = %v (want [\"\", generation1])", seen)
	}

	// Get persists refreshed state.
	if _, err := prov.Get(ctx, "Thing", "t1"); err != nil {
		t.Fatalf("get: %v", err)
	}
	st, _, _, _ = store.LoadState(ctx, "stateful.openctl.io/v1", "Thing", "t1")
	if string(st) != `{"refreshed":true}` {
		t.Errorf("state after Get = %s", st)
	}

	// Delete clears the stored state.
	if err := prov.Delete(ctx, "Thing", "t1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	st, _, _, _ = store.LoadState(ctx, "stateful.openctl.io/v1", "Thing", "t1")
	if st != nil {
		t.Errorf("state after Delete = %s, want nil", st)
	}
}

func TestStatelessAdapterIgnoresStore(t *testing.T) {
	// A plugin WITHOUT CapabilityState must not touch the store even if one is
	// provided (the demo handler doesn't advertise state).
	store := newMemStore()
	h := newTestHandler("demo") // no CapabilityState
	prov, done := newStatefulAdapter(t, h, store)
	defer done()
	m := &protocol.Resource{APIVersion: "demo.openctl.io/v1", Kind: "Thing"}
	m.Metadata.Name = "t1"
	if _, err := prov.Apply(context.Background(), m); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(store.state) != 0 {
		t.Errorf("stateless plugin should not write to the store, got %d entries", len(store.state))
	}
}

func TestNewForwardsAdvancedKindsFromHandshake(t *testing.T) {
	// A plugin declares one composite parent (Cluster) and one composite-child
	// (Worker, owned by Cluster). New must forward only the child through
	// AdvancedKindDescriber. New doesn't touch the client during construction,
	// so a nil client is fine here.
	hs := &pluginproto.HandshakeResult{
		ProviderName:    "demo",
		ProtocolVersion: pluginproto.ProtocolVersion,
		Kinds: []pluginproto.KindInfo{
			{Kind: "Cluster"},
			{Kind: "Worker", OwnerKind: "Cluster", AdvancedNote: "made by a Cluster"},
		},
	}
	prov := New(nil, hs, nil)
	d, ok := prov.(providers.AdvancedKindDescriber)
	if !ok {
		t.Fatal("external provider should implement AdvancedKindDescriber")
	}
	adv := d.AdvancedKinds()
	if len(adv) != 1 {
		t.Fatalf("AdvancedKinds len = %d, want 1 (only Worker); got %+v", len(adv), adv)
	}
	if adv[0].Kind != "Worker" || adv[0].OwnerKind != "Cluster" || adv[0].Note != "made by a Cluster" {
		t.Errorf("AdvancedKinds[0] = %+v, want {Worker Cluster \"made by a Cluster\"}", adv[0])
	}
}
