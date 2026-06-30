package reconciler

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/storage"
	"github.com/openctl/openctl/pkg/protocol"
)

// stubProvider lets tests dictate the observed state per (kind, name)
// and count Get calls. Implements the providers.Provider surface
// minimally — Apply/Delete aren't exercised by the reconciler.
type stubProvider struct {
	mu       sync.Mutex
	observed map[string]*protocol.Resource
	notFound map[string]bool
	gets     int
}

func newStubProvider() *stubProvider {
	return &stubProvider{
		observed: map[string]*protocol.Resource{},
		notFound: map[string]bool{},
	}
}

func (s *stubProvider) Name() string    { return "fake" }
func (s *stubProvider) Kinds() []string { return []string{"FakeKind"} }
func (s *stubProvider) Apply(context.Context, *protocol.Resource) (*protocol.Resource, error) {
	return nil, nil
}
func (s *stubProvider) Get(_ context.Context, kind, name string) (*protocol.Resource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	key := kind + "/" + name
	if s.notFound[key] {
		return nil, providers.NotFound(kind, name)
	}
	return s.observed[key], nil
}
func (s *stubProvider) List(context.Context, string) ([]*protocol.Resource, error) {
	return nil, nil
}
func (s *stubProvider) Delete(context.Context, string, string) error { return nil }

func (s *stubProvider) set(kind, name string, r *protocol.Resource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observed[kind+"/"+name] = r
}

func (s *stubProvider) setMissing(kind, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notFound[kind+"/"+name] = true
}

func (s *stubProvider) getCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets
}

func newTestStore(t *testing.T) *manifests.Store {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(context.Background(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return manifests.New(db)
}

func TestReconcilerHitsEveryAppliedManifest(t *testing.T) {
	store := newTestStore(t)
	prov := newStubProvider()
	reg := providers.NewRegistry()
	reg.Register(prov)

	for _, name := range []string{"a", "b", "c"} {
		if err := store.Save(context.Background(), &protocol.Resource{
			APIVersion: "fake.openctl.io/v1",
			Kind:       "FakeKind",
			Metadata:   protocol.ResourceMetadata{Name: name},
			Spec:       map[string]any{"cpus": 2.0},
		}); err != nil {
			t.Fatalf("Save %q: %v", name, err)
		}
		// Provider returns matching observed state so no drift.
		prov.set("FakeKind", name, &protocol.Resource{
			APIVersion: "fake.openctl.io/v1",
			Kind:       "FakeKind",
			Metadata:   protocol.ResourceMetadata{Name: name},
			Spec:       map[string]any{"cpus": 2.0},
		})
	}

	rec := New(reg, store, 0)
	rec.ReconcileOnce(context.Background())

	if got := prov.getCount(); got != 3 {
		t.Errorf("Get calls = %d, want 3 (one per applied manifest)", got)
	}
}

func TestReconcilerSkipsUnregisteredProviders(t *testing.T) {
	store := newTestStore(t)
	reg := providers.NewRegistry()
	// No providers registered — the reconciler should not panic.
	if err := store.Save(context.Background(), &protocol.Resource{
		APIVersion: "nobody.openctl.io/v1",
		Kind:       "Orphan",
		Metadata:   protocol.ResourceMetadata{Name: "x"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rec := New(reg, store, 0)
	rec.ReconcileOnce(context.Background()) // would panic on nil deref if unsafe
}

func TestReconcilerTracksDriftTransitions(t *testing.T) {
	store := newTestStore(t)
	prov := newStubProvider()
	reg := providers.NewRegistry()
	reg.Register(prov)

	if err := store.Save(context.Background(), &protocol.Resource{
		APIVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
		Metadata:   protocol.ResourceMetadata{Name: "x"},
		Spec:       map[string]any{"cpus": 2.0},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Start clean.
	prov.set("FakeKind", "x", &protocol.Resource{
		APIVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
		Metadata:   protocol.ResourceMetadata{Name: "x"},
		Spec:       map[string]any{"cpus": 2.0},
	})

	rec := New(reg, store, 0)
	rec.ReconcileOnce(context.Background())
	if rec.driftState["fake.openctl.io/v1/FakeKind/x"] {
		t.Error("expected clean state after first tick")
	}

	// Observed drifts.
	prov.set("FakeKind", "x", &protocol.Resource{
		APIVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
		Metadata:   protocol.ResourceMetadata{Name: "x"},
		Spec:       map[string]any{"cpus": 4.0},
	})
	rec.ReconcileOnce(context.Background())
	if !rec.driftState["fake.openctl.io/v1/FakeKind/x"] {
		t.Error("expected drifted state after spec change")
	}

	// Observed converges back.
	prov.set("FakeKind", "x", &protocol.Resource{
		APIVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
		Metadata:   protocol.ResourceMetadata{Name: "x"},
		Spec:       map[string]any{"cpus": 2.0},
	})
	rec.ReconcileOnce(context.Background())
	if rec.driftState["fake.openctl.io/v1/FakeKind/x"] {
		t.Error("expected clean state after convergence")
	}
}

func TestReconcilerTreatsMissingProviderResourceAsDrift(t *testing.T) {
	store := newTestStore(t)
	prov := newStubProvider()
	reg := providers.NewRegistry()
	reg.Register(prov)

	if err := store.Save(context.Background(), &protocol.Resource{
		APIVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
		Metadata:   protocol.ResourceMetadata{Name: "ghost"},
		Spec:       map[string]any{"cpus": 2.0},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	prov.setMissing("FakeKind", "ghost")

	rec := New(reg, store, 0)
	rec.ReconcileOnce(context.Background())
	if !rec.driftState["fake.openctl.io/v1/FakeKind/ghost"] {
		t.Error("missing provider resource should mark as drifted")
	}
}

func TestSpecsEqualNormalizesNumberTypes(t *testing.T) {
	// YAML loads "2" as int; protobuf JSON round-trips as float64. They
	// must compare equal.
	desired := map[string]any{"cpus": 2, "memory": int64(4096)}
	observed := map[string]any{"cpus": 2.0, "memory": 4096.0}
	if !specsEqual(desired, observed) {
		t.Error("specsEqual should normalize int/float for numeric fields")
	}
}

func TestSpecsEqualIgnoresUnmanagedKeysInObserved(t *testing.T) {
	// Only fields in `desired` participate in the comparison. Provider-set
	// defaults that don't appear in the manifest must not register as drift.
	desired := map[string]any{"cpus": 2}
	observed := map[string]any{"cpus": 2, "providerSetDefault": "yes"}
	if !specsEqual(desired, observed) {
		t.Error("extra observed keys should not register as drift")
	}
}

func TestSpecsEqualDetectsNestedDifference(t *testing.T) {
	desired := map[string]any{"net": map[string]any{"bridge": "vmbr0"}}
	observed := map[string]any{"net": map[string]any{"bridge": "vmbr1"}}
	if specsEqual(desired, observed) {
		t.Error("nested mismatch should register as drift")
	}
}
