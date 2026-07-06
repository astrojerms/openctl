package operations

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/storage"
	"github.com/openctl/openctl/pkg/protocol"
)

// --- pure edge / acyclicity helpers ---

func applyOp(id, apiVersion, kind, name string, spec map[string]any) *Operation {
	r := &protocol.Resource{APIVersion: apiVersion, Kind: kind}
	r.Metadata.Name = name
	r.Spec = spec
	b, _ := json.Marshal(r)
	return &Operation{ID: id, Type: TypeApply, APIVersion: apiVersion, Kind: kind, ResourceName: name, ManifestJSON: string(b)}
}

func refTo(apiVersion, kind, name, field string) map[string]any {
	return map[string]any{"$ref": map[string]any{"apiVersion": apiVersion, "kind": kind, "name": name, "field": field}}
}

func TestCrossOpEdges(t *testing.T) {
	const api = "fake.openctl.io/v1"
	// opB references the resource opA applies; opC references something not in
	// the batch (already-applied / external); opD references itself.
	opA := applyOp("A", api, "Thing", "a", nil)
	opB := applyOp("B", api, "Thing", "b", map[string]any{"from": refTo(api, "Thing", "a", "status.x")})
	opC := applyOp("C", api, "Thing", "c", map[string]any{"from": refTo(api, "Thing", "elsewhere", "status.x")})
	opD := applyOp("D", api, "Thing", "d", map[string]any{"self": refTo(api, "Thing", "d", "status.x")})
	del := &Operation{ID: "DEL", Type: TypeDelete, APIVersion: api, Kind: "Thing", ResourceName: "z"}

	edges, err := crossOpEdges([]*Operation{opA, opB, opC, opD, del})
	if err != nil {
		t.Fatalf("crossOpEdges: %v", err)
	}
	if got := edges["B"]; len(got) != 1 || got[0] != "A" {
		t.Errorf("edges[B] = %v, want [A]", got)
	}
	if got, ok := edges["C"]; ok {
		t.Errorf("edges[C] = %v, want none (target not applied in batch)", got)
	}
	if got, ok := edges["D"]; ok {
		t.Errorf("edges[D] = %v, want none (self-ref excluded)", got)
	}
	if got, ok := edges["A"]; ok {
		t.Errorf("edges[A] = %v, want none", got)
	}
}

func TestCrossOpEdges_DedupesAndSorts(t *testing.T) {
	const api = "fake.openctl.io/v1"
	opA := applyOp("A", api, "Thing", "a", nil)
	opB := applyOp("B", api, "Thing", "b", nil)
	// opC references a twice and b once.
	opC := applyOp("C", api, "Thing", "c", map[string]any{
		"one":   refTo(api, "Thing", "a", "status.x"),
		"two":   refTo(api, "Thing", "a", "status.y"),
		"three": refTo(api, "Thing", "b", "status.z"),
	})
	edges, err := crossOpEdges([]*Operation{opA, opB, opC})
	if err != nil {
		t.Fatalf("crossOpEdges: %v", err)
	}
	got := edges["C"]
	if len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Errorf("edges[C] = %v, want [A B] (deduped, sorted)", got)
	}
}

func TestCrossOpAcyclic(t *testing.T) {
	ids := []string{"A", "B", "C"}
	acyclic := map[string][]string{"B": {"A"}, "C": {"B"}}
	if !crossOpAcyclic(ids, acyclic) {
		t.Error("expected acyclic chain A<-B<-C to be reported acyclic")
	}
	cyclic := map[string][]string{"A": {"B"}, "B": {"A"}}
	if crossOpAcyclic([]string{"A", "B"}, cyclic) {
		t.Error("expected mutual A<->B to be reported cyclic")
	}
}

// --- drainScheduled integration ---

// orderProvider records apply order and stores applied resources so a
// dependent op's $ref can resolve only after its predecessor has applied.
type orderProvider struct {
	name  string
	kinds []string

	mu    sync.Mutex
	order []string
	store map[string]*protocol.Resource
}

func newOrderProvider() *orderProvider {
	return &orderProvider{name: "fake", kinds: []string{"Thing"}, store: map[string]*protocol.Resource{}}
}

func (p *orderProvider) Name() string    { return p.name }
func (p *orderProvider) Kinds() []string { return p.kinds }

func (p *orderProvider) Apply(_ context.Context, m *protocol.Resource) (*protocol.Resource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.order = append(p.order, m.Metadata.Name)
	obs := &protocol.Resource{
		APIVersion: m.APIVersion, Kind: m.Kind,
		Metadata: protocol.ResourceMetadata{Name: m.Metadata.Name},
		Spec:     m.Spec,
		Status:   map[string]any{"x": "ready-" + m.Metadata.Name},
	}
	p.store[m.Kind+"/"+m.Metadata.Name] = obs
	return obs, nil
}

func (p *orderProvider) Get(_ context.Context, kind, name string) (*protocol.Resource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r, ok := p.store[kind+"/"+name]; ok {
		return r, nil
	}
	return nil, providers.NotFound(kind, name)
}
func (p *orderProvider) List(context.Context, string) ([]*protocol.Resource, error) { return nil, nil }
func (p *orderProvider) Delete(context.Context, string, string) error               { return nil }

func (p *orderProvider) appliedOrder() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.order...)
}

func newSchedulerDispatcher(t *testing.T, p providers.Provider) (*Store, *Dispatcher) {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := New(db, 50)
	reg := providers.NewRegistry()
	reg.Register(p)
	return store, NewDispatcher(store, reg, nil, 50*time.Millisecond)
}

func submitApply(t *testing.T, store *Store, name string, spec map[string]any) *Operation {
	t.Helper()
	const api = "fake.openctl.io/v1"
	r := &protocol.Resource{APIVersion: api, Kind: "Thing"}
	r.Metadata.Name = name
	r.Spec = spec
	b, _ := json.Marshal(r)
	op, err := store.Submit(context.Background(), &Operation{
		Type: TypeApply, APIVersion: api, Kind: "Thing", ResourceName: name, ManifestJSON: string(b),
	})
	if err != nil {
		t.Fatalf("submit %s: %v", name, err)
	}
	return op
}

func statusOf(t *testing.T, store *Store, id string) string {
	t.Helper()
	op, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get op %s: %v", id, err)
	}
	return op.Status
}

// A dependent op (b references a) is ordered after its predecessor, and its
// $ref resolves because a applied first.
func TestDrainScheduled_OrdersDependents(t *testing.T) {
	p := newOrderProvider()
	store, d := newSchedulerDispatcher(t, p)

	a := submitApply(t, store, "a", nil)
	b := submitApply(t, store, "b", map[string]any{"from": refTo("fake.openctl.io/v1", "Thing", "a", "status.x")})

	d.drainScheduled(context.Background())

	if s := statusOf(t, store, a.ID); s != StatusSucceeded {
		t.Errorf("op a status = %q, want succeeded", s)
	}
	if s := statusOf(t, store, b.ID); s != StatusSucceeded {
		t.Errorf("op b status = %q, want succeeded (its $ref to a must resolve)", s)
	}
	order := p.appliedOrder()
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Errorf("apply order = %v, want [a b]", order)
	}
}

// Independent ops all complete (isolation: no ordering constraints).
func TestDrainScheduled_IndependentOpsAllComplete(t *testing.T) {
	p := newOrderProvider()
	store, d := newSchedulerDispatcher(t, p)

	ops := []*Operation{
		submitApply(t, store, "one", nil),
		submitApply(t, store, "two", nil),
		submitApply(t, store, "three", nil),
	}
	d.drainScheduled(context.Background())

	for _, op := range ops {
		if s := statusOf(t, store, op.ID); s != StatusSucceeded {
			t.Errorf("op %s status = %q, want succeeded", op.ResourceName, s)
		}
	}
}

// A failing op does not stop an independent op in the same batch.
func TestDrainScheduled_FailureIsIsolated(t *testing.T) {
	p := &failOneProvider{orderProvider: newOrderProvider(), failName: "bad"}
	store, d := newSchedulerDispatcher(t, p)

	bad := submitApply(t, store, "bad", nil)
	good := submitApply(t, store, "good", nil)

	d.drainScheduled(context.Background())

	if s := statusOf(t, store, bad.ID); s != StatusFailed {
		t.Errorf("op bad status = %q, want failed", s)
	}
	if s := statusOf(t, store, good.ID); s != StatusSucceeded {
		t.Errorf("op good status = %q, want succeeded (independent of the failure)", s)
	}
}

type failOneProvider struct {
	*orderProvider
	failName string
}

func (p *failOneProvider) Apply(ctx context.Context, m *protocol.Resource) (*protocol.Resource, error) {
	if m.Metadata.Name == p.failName {
		return nil, context.Canceled // any non-nil error
	}
	return p.orderProvider.Apply(ctx, m)
}

// A $ref cycle between two ops does not hang or leave ops claimed-but-unrun:
// both reach a terminal state (scheduled unordered as a fallback).
func TestDrainScheduled_CycleDoesNotHang(t *testing.T) {
	p := newOrderProvider()
	store, d := newSchedulerDispatcher(t, p)

	const api = "fake.openctl.io/v1"
	a := submitApply(t, store, "a", map[string]any{"from": refTo(api, "Thing", "b", "status.x")})
	b := submitApply(t, store, "b", map[string]any{"from": refTo(api, "Thing", "a", "status.x")})

	done := make(chan struct{})
	go func() {
		d.drainScheduled(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drainScheduled hung on a $ref cycle")
	}

	for _, op := range []*Operation{a, b} {
		if s := statusOf(t, store, op.ID); s != StatusSucceeded && s != StatusFailed {
			t.Errorf("op %s left in non-terminal status %q after a cycle", op.ResourceName, s)
		}
	}
}

// With the flag set, drain() routes to the scheduled path and processes the
// pending batch.
func TestDrainRoutesToScheduledWhenFlagSet(t *testing.T) {
	t.Setenv("OPENCTL_CROSS_OP_SCHEDULING", "1")
	p := newOrderProvider()
	store, d := newSchedulerDispatcher(t, p)

	op := submitApply(t, store, "solo", nil)
	d.drain(context.Background())

	if s := statusOf(t, store, op.ID); s != StatusSucceeded {
		t.Errorf("op status = %q, want succeeded via the scheduled drain path", s)
	}
}
