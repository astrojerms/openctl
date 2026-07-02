package operations

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/storage"
	"github.com/openctl/openctl/pkg/protocol"
)

// refTargetProvider is a fake provider whose Get returns a mutable
// status field. Used by the refs_hash cache tests to simulate an
// upstream resource updating its observable state between applies.
type refTargetProvider struct {
	name        string
	kind        string
	ip          atomic.Value // string; the value returned as status.ip
	getRefCalls atomic.Int32
}

func newRefTargetProvider(name, kind, initialIP string) *refTargetProvider {
	p := &refTargetProvider{name: name, kind: kind}
	p.ip.Store(initialIP)
	return p
}

func (p *refTargetProvider) Name() string    { return p.name }
func (p *refTargetProvider) Kinds() []string { return []string{p.kind} }
func (p *refTargetProvider) Apply(_ context.Context, m *protocol.Resource) (*protocol.Resource, error) {
	return m, nil
}
func (p *refTargetProvider) Get(_ context.Context, _, name string) (*protocol.Resource, error) {
	p.getRefCalls.Add(1)
	return &protocol.Resource{
		APIVersion: p.name + ".openctl.io/v1",
		Kind:       p.kind,
		Metadata:   protocol.ResourceMetadata{Name: name},
		Status: map[string]any{
			"ip": p.ip.Load().(string),
		},
	}, nil
}
func (p *refTargetProvider) List(context.Context, string) ([]*protocol.Resource, error) {
	return nil, nil
}
func (p *refTargetProvider) Delete(context.Context, string, string) error { return nil }
func (p *refTargetProvider) setIP(ip string)                              { p.ip.Store(ip) }

func newDispatcherWithTwoProviders(t *testing.T, consumer *fakeProvider, target *refTargetProvider) (*Store, *Dispatcher) {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	opStore := New(db, 50)
	mStore := manifests.New(db)
	reg := providers.NewRegistry()
	reg.Register(consumer)
	reg.Register(target)
	d := NewDispatcher(opStore, reg, mStore, 50*time.Millisecond)
	return opStore, d
}

// TestRefsHashCacheMissWhenTargetChanges: same raw manifest applied
// twice, but between applies the ref target's status.ip changes.
// The stored input_hash still matches (raw manifest unchanged), but
// refs_hash differs — so the cache must miss and provider.Apply runs
// again with the new resolved value.
func TestRefsHashCacheMissWhenTargetChanges(t *testing.T) {
	consumer := &fakeProvider{
		name:  "consumer",
		kinds: []string{"Consumer"},
	}
	target := newRefTargetProvider("target", "Target", "10.0.0.1")
	store, d := newDispatcherWithTwoProviders(t, consumer, target)

	// A manifest whose spec.ipRef resolves against Target/foo's
	// status.ip. Same raw manifest for both applies.
	manifest := `{"apiVersion":"consumer.openctl.io/v1","kind":"Consumer","metadata":{"name":"c"},"spec":{"ipRef":{"$ref":{"apiVersion":"target.openctl.io/v1","kind":"Target","name":"foo","field":"status.ip"}}}}`

	op1, err := store.Submit(context.Background(), &Operation{
		Type: TypeApply, APIVersion: "consumer.openctl.io/v1", Kind: "Consumer",
		ResourceName: "c", ManifestJSON: manifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	runPendingOp(t, d, store, op1.ID, StatusSucceeded)
	if consumer.applies.Load() != 1 {
		t.Fatalf("first apply: consumer.Apply called %d times, want 1", consumer.applies.Load())
	}

	// Target's IP changes underneath. Raw manifest is unchanged.
	target.setIP("10.0.0.99")

	op2, err := store.Submit(context.Background(), &Operation{
		Type: TypeApply, APIVersion: "consumer.openctl.io/v1", Kind: "Consumer",
		ResourceName: "c", ManifestJSON: manifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	runPendingOp(t, d, store, op2.ID, StatusSucceeded)

	if consumer.applies.Load() != 2 {
		t.Errorf("ref target change should miss the cache; consumer.Apply called %d times, want 2", consumer.applies.Load())
	}
}

// TestRefsHashCacheHitWhenTargetUnchanged: baseline. Same raw
// manifest + same target state → cache hit (Apply skipped on second
// pass). The complement of the previous test — proves the miss above
// was actually driven by the ref value delta.
func TestRefsHashCacheHitWhenTargetUnchanged(t *testing.T) {
	consumer := &fakeProvider{
		name:     "consumer",
		kinds:    []string{"Consumer"},
		applyOut: &protocol.Resource{APIVersion: "consumer.openctl.io/v1", Kind: "Consumer", Metadata: protocol.ResourceMetadata{Name: "c"}, Status: map[string]any{"applied": true}},
		getOut:   &protocol.Resource{APIVersion: "consumer.openctl.io/v1", Kind: "Consumer", Metadata: protocol.ResourceMetadata{Name: "c"}, Status: map[string]any{"applied": true}},
	}
	target := newRefTargetProvider("target", "Target", "10.0.0.1")
	store, d := newDispatcherWithTwoProviders(t, consumer, target)

	manifest := `{"apiVersion":"consumer.openctl.io/v1","kind":"Consumer","metadata":{"name":"c"},"spec":{"ipRef":{"$ref":{"apiVersion":"target.openctl.io/v1","kind":"Target","name":"foo","field":"status.ip"}}}}`

	op1, err := store.Submit(context.Background(), &Operation{
		Type: TypeApply, APIVersion: "consumer.openctl.io/v1", Kind: "Consumer",
		ResourceName: "c", ManifestJSON: manifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	runPendingOp(t, d, store, op1.ID, StatusSucceeded)

	op2, err := store.Submit(context.Background(), &Operation{
		Type: TypeApply, APIVersion: "consumer.openctl.io/v1", Kind: "Consumer",
		ResourceName: "c", ManifestJSON: manifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	runPendingOp(t, d, store, op2.ID, StatusSucceeded)

	if consumer.applies.Load() != 1 {
		t.Errorf("unchanged target: expected 1 Apply (cache hit on second pass), got %d", consumer.applies.Load())
	}
}
