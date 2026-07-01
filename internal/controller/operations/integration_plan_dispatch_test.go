package operations

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/storage"
	"github.com/openctl/openctl/pkg/protocol"
)

// End-to-end integration coverage for the Phase-8 dispatcher refactor.
//
// These tests wire a REAL *Dispatcher with two fake providers:
//
//   - `orchestrator` provider with kind "Orchestration" implements
//     providers.Planner. Its Plan() emits two Leaf children where the
//     second's spec.upstream is a $ref to the first's status.result.
//     Its Apply fans out those children via ChildDispatcherFrom(ctx).
//
//   - `leaf` provider with kind "Leaf" is the atomic worker. Apply
//     saves state indexed by name and echoes back status.result =
//     "produced-by:" + spec.name. Get returns the stored state.
//
// This exercises exactly the mechanics the k3s Plan-path depends on:
//
//   1. Dispatcher's execute() sets up WithChildDispatcher(ctx, d).
//   2. Orchestrator.Apply pulls the ChildDispatcher back out and calls
//      ApplyChild on each Plan child.
//   3. ApplyChild → ApplyManifest → refs.Resolver: the second Leaf's
//      $ref to the first Leaf's status.result gets resolved from the
//      just-saved state.
//   4. On second submission of the same parent op, both Leaves hit
//      the two-dimensional cache (input_hash + refs_hash) and neither
//      provider.Apply runs a second time.
//
// If ANY of those chain steps breaks — bad ctx plumbing, wrong Apply
// ordering, missed cache save, ref resolution racing state
// persistence — one of these tests will fail with a specific message.

// ---------- fake providers ----------

// leafProvider is the atomic worker. Apply/Get/List/Delete are all
// backed by an in-memory map keyed by name, so a Leaf's status is
// observable immediately after Apply returns (mirroring how real
// K3sNode's applyK3sNode makes status.nodeToken visible to the next
// sibling's $ref during the plan fan-out).
type leafProvider struct {
	mu       sync.Mutex
	state    map[string]*protocol.Resource
	applies  atomic.Int32
	gets     atomic.Int32
	lastSpec sync.Map // name → spec map from most recent Apply
}

func newLeafProvider() *leafProvider {
	return &leafProvider{state: map[string]*protocol.Resource{}}
}

func (p *leafProvider) Name() string    { return "leaf" }
func (p *leafProvider) Kinds() []string { return []string{"Leaf"} }

func (p *leafProvider) Apply(_ context.Context, m *protocol.Resource) (*protocol.Resource, error) {
	p.applies.Add(1)
	p.lastSpec.Store(m.Metadata.Name, m.Spec)
	name, _ := m.Spec["name"].(string)
	result := &protocol.Resource{
		APIVersion: m.APIVersion,
		Kind:       m.Kind,
		Metadata:   protocol.ResourceMetadata{Name: m.Metadata.Name},
		Spec:       m.Spec,
		Status: map[string]any{
			"result": "produced-by:" + name,
		},
	}
	// Include any resolved upstream value in status so downstream
	// assertions can distinguish "ran with upstream resolved" from
	// "ran with upstream still a $ref marker."
	if upstream, ok := m.Spec["upstream"]; ok {
		result.Status["observedUpstream"] = upstream
	}
	p.mu.Lock()
	p.state[m.Metadata.Name] = result
	p.mu.Unlock()
	return result, nil
}

func (p *leafProvider) Get(_ context.Context, _, name string) (*protocol.Resource, error) {
	p.gets.Add(1)
	p.mu.Lock()
	defer p.mu.Unlock()
	if r, ok := p.state[name]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("leaf %q not found", name)
}

func (p *leafProvider) List(context.Context, string) ([]*protocol.Resource, error) {
	return nil, nil
}
func (p *leafProvider) Delete(context.Context, string, string) error { return nil }

// specAtApply returns the spec passed to Leaf.Apply for `name`, or nil
// if none seen. Used to assert what the ref resolver produced.
func (p *leafProvider) specAtApply(name string) map[string]any {
	v, ok := p.lastSpec.Load(name)
	if !ok {
		return nil
	}
	s, _ := v.(map[string]any)
	return s
}

// orchestratorProvider is the composite. Plan emits two Leaves; the
// second refs the first. Apply fans out via the ChildDispatcher on
// ctx — matching the shape k3s Cluster uses for its plan-based apply.
type orchestratorProvider struct {
	applies atomic.Int32
}

func (p *orchestratorProvider) Name() string    { return "orchestrator" }
func (p *orchestratorProvider) Kinds() []string { return []string{"Orchestration"} }

func (p *orchestratorProvider) Plan(_ context.Context, m *protocol.Resource) (*providers.PlanResult, error) {
	parentName := m.Metadata.Name
	return &providers.PlanResult{
		Children: []*protocol.Resource{
			{
				APIVersion: "leaf.openctl.io/v1",
				Kind:       "Leaf",
				Metadata: protocol.ResourceMetadata{
					Name:   parentName + "-a",
					Labels: map[string]string{providers.LabelOwnerName: parentName},
				},
				Spec: map[string]any{"name": "first"},
			},
			{
				APIVersion: "leaf.openctl.io/v1",
				Kind:       "Leaf",
				Metadata: protocol.ResourceMetadata{
					Name:   parentName + "-b",
					Labels: map[string]string{providers.LabelOwnerName: parentName},
				},
				Spec: map[string]any{
					"name": "second",
					// $ref to the first Leaf's status.result. If ref
					// resolution during fan-out is broken, this stays as
					// a map with a "$ref" key when Leaf.Apply sees it.
					"upstream": map[string]any{
						"$ref": map[string]any{
							"apiVersion": "leaf.openctl.io/v1",
							"kind":       "Leaf",
							"name":       parentName + "-a",
							"field":      "status.result",
						},
					},
				},
			},
		},
	}, nil
}

func (p *orchestratorProvider) Apply(ctx context.Context, m *protocol.Resource) (*protocol.Resource, error) {
	p.applies.Add(1)
	cd, ok := ChildDispatcherFrom(ctx)
	if !ok {
		return nil, fmt.Errorf("no ChildDispatcher on ctx — dispatcher wiring broken")
	}
	plan, err := p.Plan(ctx, m)
	if err != nil {
		return nil, err
	}
	// Sequential fan-out: mirrors what applyClusterViaPlan does for
	// K3sNode. Order matters because the second Leaf $refs the first.
	for _, child := range plan.Children {
		if _, err := cd.ApplyChild(ctx, child); err != nil {
			return nil, fmt.Errorf("apply child %s: %w", child.Metadata.Name, err)
		}
	}
	return &protocol.Resource{
		APIVersion: m.APIVersion,
		Kind:       m.Kind,
		Metadata:   protocol.ResourceMetadata{Name: m.Metadata.Name},
		Spec:       m.Spec,
		Status:     map[string]any{"phase": "Ready"},
	}, nil
}

func (p *orchestratorProvider) Get(_ context.Context, _, name string) (*protocol.Resource, error) {
	return &protocol.Resource{
		APIVersion: "orchestrator.openctl.io/v1",
		Kind:       "Orchestration",
		Metadata:   protocol.ResourceMetadata{Name: name},
		Status:     map[string]any{"phase": "Ready"},
	}, nil
}

func (p *orchestratorProvider) List(context.Context, string) ([]*protocol.Resource, error) {
	return nil, nil
}
func (p *orchestratorProvider) Delete(context.Context, string, string) error { return nil }

// ---------- test harness ----------

func newIntegrationDispatcher(t *testing.T) (*Store, *Dispatcher, *orchestratorProvider, *leafProvider) {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	opStore := New(db, 50)
	mStore := manifests.New(db)
	reg := providers.NewRegistry()
	orch := &orchestratorProvider{}
	leaf := newLeafProvider()
	reg.Register(orch)
	reg.Register(leaf)
	d := NewDispatcher(opStore, reg, mStore, 50*time.Millisecond)
	return opStore, d, orch, leaf
}

// ---------- tests ----------

// TestIntegration_PlanDispatch_FansOutViaChildDispatcher proves the
// bare wiring: a composite parent's Apply, when invoked by the
// dispatcher, gets a working ChildDispatcher on ctx and can call
// ApplyChild on Plan children. Provable failure mode: no
// ChildDispatcher on ctx → orchestrator.Apply errors with a clear
// message and the parent op fails.
func TestIntegration_PlanDispatch_FansOutViaChildDispatcher(t *testing.T) {
	store, d, orch, leaf := newIntegrationDispatcher(t)
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	manifest := `{"apiVersion":"orchestrator.openctl.io/v1","kind":"Orchestration","metadata":{"name":"cluster"},"spec":{"foo":"bar"}}`

	op, err := store.Submit(context.Background(), &Operation{
		Type:         TypeApply,
		APIVersion:   "orchestrator.openctl.io/v1",
		Kind:         "Orchestration",
		ResourceName: "cluster",
		ManifestJSON: manifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	d.Notify()
	waitForStatus(t, store, op.ID, StatusSucceeded, 3*time.Second)

	if orch.applies.Load() != 1 {
		t.Errorf("orchestrator.Apply count = %d, want 1", orch.applies.Load())
	}
	if leaf.applies.Load() != 2 {
		t.Errorf("leaf.Apply count = %d, want 2 (both children fanned out)", leaf.applies.Load())
	}
}

// TestIntegration_ChildRefResolvesFromJustSavedState proves the
// load-bearing behavior that made this refactor risky: during the
// parent's fan-out, the second child's $ref to the first child's
// status.result must resolve from state the first child JUST wrote
// via ApplyManifest's SaveWithRefsHash path. If ref resolution and
// state-save race, we'll see the raw {"$ref": {...}} marker in the
// second Leaf's observed spec.
func TestIntegration_ChildRefResolvesFromJustSavedState(t *testing.T) {
	store, d, _, leaf := newIntegrationDispatcher(t)
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	manifest := `{"apiVersion":"orchestrator.openctl.io/v1","kind":"Orchestration","metadata":{"name":"cluster"},"spec":{}}`
	op, _ := store.Submit(context.Background(), &Operation{
		Type: TypeApply, APIVersion: "orchestrator.openctl.io/v1", Kind: "Orchestration",
		ResourceName: "cluster", ManifestJSON: manifest,
	})
	d.Notify()
	waitForStatus(t, store, op.ID, StatusSucceeded, 3*time.Second)

	secondSpec := leaf.specAtApply("cluster-b")
	if secondSpec == nil {
		t.Fatal("second leaf never applied")
	}
	upstream, ok := secondSpec["upstream"]
	if !ok {
		t.Fatal("second leaf's spec missing upstream field")
	}
	// If ref resolution worked, upstream is a string. If it didn't,
	// upstream is still the raw $ref map.
	upstreamStr, isString := upstream.(string)
	if !isString {
		t.Fatalf("upstream not resolved — got %T (%+v), want string. Ref resolution during fan-out is broken.", upstream, upstream)
	}
	if upstreamStr != "produced-by:first" {
		t.Errorf("upstream = %q, want %q (should equal first Leaf's status.result)", upstreamStr, "produced-by:first")
	}
}

// TestIntegration_SecondApplyHitsPerChildCache proves that once
// children have been through the pipeline, a second apply of the
// SAME parent manifest hits the two-dimensional cache PER CHILD.
// This is the whole point of the refactor: per-resource cache
// invalidation. Under the old imperative Cluster.Apply, the second
// apply either re-did all children (waste) or hit the parent-level
// cache and no-op'd everything (invisibly incorrect on drift).
func TestIntegration_SecondApplyHitsPerChildCache(t *testing.T) {
	store, d, _, leaf := newIntegrationDispatcher(t)
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	manifest := `{"apiVersion":"orchestrator.openctl.io/v1","kind":"Orchestration","metadata":{"name":"cluster"},"spec":{}}`

	op1, _ := store.Submit(context.Background(), &Operation{
		Type: TypeApply, APIVersion: "orchestrator.openctl.io/v1", Kind: "Orchestration",
		ResourceName: "cluster", ManifestJSON: manifest,
	})
	d.Notify()
	waitForStatus(t, store, op1.ID, StatusSucceeded, 3*time.Second)
	firstApplies := leaf.applies.Load()
	if firstApplies != 2 {
		t.Fatalf("first apply: leaf.Apply = %d, want 2", firstApplies)
	}

	op2, _ := store.Submit(context.Background(), &Operation{
		Type: TypeApply, APIVersion: "orchestrator.openctl.io/v1", Kind: "Orchestration",
		ResourceName: "cluster", ManifestJSON: manifest,
	})
	d.Notify()
	waitForStatus(t, store, op2.ID, StatusSucceeded, 3*time.Second)

	if leaf.applies.Load() != firstApplies {
		t.Errorf("second apply: leaf.Apply = %d, want still %d (per-child cache should have hit both leaves)",
			leaf.applies.Load(), firstApplies)
	}
}

// TestIntegration_ChildFailurePropagatesToParent proves failure
// attribution: if any Plan child fails inside ApplyManifest, the
// parent op reflects the failure with the child's error message in
// scope. Without this, a broken K3sNode install would surface as a
// generic parent failure and the operator would have to dig into
// logs to figure out which node.
func TestIntegration_ChildFailurePropagatesToParent(t *testing.T) {
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	opStore := New(db, 50)
	mStore := manifests.New(db)
	reg := providers.NewRegistry()
	reg.Register(&orchestratorProvider{})
	// Leaf provider whose Apply always fails — simulates a broken
	// child provider.
	reg.Register(&failingLeafProvider{})
	d := NewDispatcher(opStore, reg, mStore, 50*time.Millisecond)
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	manifest := `{"apiVersion":"orchestrator.openctl.io/v1","kind":"Orchestration","metadata":{"name":"cluster"},"spec":{}}`
	op, _ := opStore.Submit(context.Background(), &Operation{
		Type: TypeApply, APIVersion: "orchestrator.openctl.io/v1", Kind: "Orchestration",
		ResourceName: "cluster", ManifestJSON: manifest,
	})
	d.Notify()
	finalOp := waitForStatus(t, opStore, op.ID, StatusFailed, 3*time.Second)
	// The error message should identify which child failed —
	// orchestrator.Apply wraps the child error with the child name.
	if finalOp.Error == "" {
		t.Fatal("expected non-empty error message on parent op")
	}
	// Look for the specific child name and the failing-leaf sentinel.
	// Robustness note: this is a substring check, not equality —
	// exact wording of the wrapper may evolve.
	wantSubstrs := []string{"cluster-a", "simulated leaf failure"}
	for _, want := range wantSubstrs {
		if !containsAll(finalOp.Error, want) {
			t.Errorf("parent error missing %q; got %q", want, finalOp.Error)
		}
	}
}

// failingLeafProvider implements Provider but always errors on Apply.
// Used only by the child-failure-propagation test.
type failingLeafProvider struct{}

func (failingLeafProvider) Name() string    { return "leaf" }
func (failingLeafProvider) Kinds() []string { return []string{"Leaf"} }
func (failingLeafProvider) Apply(context.Context, *protocol.Resource) (*protocol.Resource, error) {
	return nil, fmt.Errorf("simulated leaf failure")
}
func (failingLeafProvider) Get(context.Context, string, string) (*protocol.Resource, error) {
	return nil, fmt.Errorf("not found")
}
func (failingLeafProvider) List(context.Context, string) ([]*protocol.Resource, error) {
	return nil, nil
}
func (failingLeafProvider) Delete(context.Context, string, string) error { return nil }

// containsAll: single-substring check pulled into a helper so tests
// read as "want %q to appear in message" rather than the noise of
// strings.Contains inline. Named plural for potential future extension.
func containsAll(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
