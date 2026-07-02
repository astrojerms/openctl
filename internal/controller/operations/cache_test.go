package operations

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/storage"
	"github.com/openctl/openctl/pkg/protocol"
)

// newDispatcherWithManifests is like newDispatcherWithStore but wires a
// real manifests.Store as the dispatcher's ManifestSink so the verifying-
// trace cache is exercised end-to-end.
func newDispatcherWithManifests(t *testing.T, p *fakeProvider) (*Store, *Dispatcher) {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	opStore := New(db, 50)
	mStore := manifests.New(db)
	reg := providers.NewRegistry()
	reg.Register(p)
	d := NewDispatcher(opStore, reg, mStore, 50*time.Millisecond)
	return opStore, d
}

func TestVerifyingTraceCacheHitSkipsApply(t *testing.T) {
	cachedResource := &protocol.Resource{
		APIVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
		Metadata:   protocol.ResourceMetadata{Name: "x"},
		Status:     map[string]any{"cached": true},
	}
	p := &fakeProvider{
		name:     "fake",
		kinds:    []string{"FakeKind"},
		applyOut: cachedResource,
		getOut:   cachedResource,
	}
	store, d := newDispatcherWithManifests(t, p)
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	manifest := `{"apiVersion":"fake.openctl.io/v1","kind":"FakeKind","metadata":{"name":"x"},"spec":{"cores":2}}`

	// First apply: cache miss, provider.Apply runs.
	first, err := store.Submit(context.Background(), &Operation{
		Type: TypeApply, APIVersion: "fake.openctl.io/v1", Kind: "FakeKind",
		ResourceName: "x", ManifestJSON: manifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	d.Notify()
	waitForStatus(t, store, first.ID, StatusSucceeded, 2*time.Second)

	if p.applies.Load() != 1 {
		t.Fatalf("first apply: provider.Apply called %d times, want 1", p.applies.Load())
	}

	// Second apply with the SAME manifest: cache hit.
	second, err := store.Submit(context.Background(), &Operation{
		Type: TypeApply, APIVersion: "fake.openctl.io/v1", Kind: "FakeKind",
		ResourceName: "x", ManifestJSON: manifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	d.Notify()
	finalOp := waitForStatus(t, store, second.ID, StatusSucceeded, 2*time.Second)

	if p.applies.Load() != 1 {
		t.Errorf("second apply (cache hit): provider.Apply called %d times, want still 1", p.applies.Load())
	}
	if p.gets.Load() < 1 {
		t.Errorf("cache hit should call provider.Get to populate result; got %d Get calls", p.gets.Load())
	}
	if finalOp.Label == "" {
		t.Errorf("cache hit op should have a label, got empty")
	}
}

func TestVerifyingTraceCacheMissOnSpecChange(t *testing.T) {
	p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}}
	store, d := newDispatcherWithManifests(t, p)
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	manifestA := `{"apiVersion":"fake.openctl.io/v1","kind":"FakeKind","metadata":{"name":"x"},"spec":{"cores":2}}`
	manifestB := `{"apiVersion":"fake.openctl.io/v1","kind":"FakeKind","metadata":{"name":"x"},"spec":{"cores":4}}`

	op1, err := store.Submit(context.Background(), &Operation{
		Type: TypeApply, APIVersion: "fake.openctl.io/v1", Kind: "FakeKind",
		ResourceName: "x", ManifestJSON: manifestA,
	})
	if err != nil {
		t.Fatal(err)
	}
	d.Notify()
	waitForStatus(t, store, op1.ID, StatusSucceeded, 2*time.Second)

	op2, err := store.Submit(context.Background(), &Operation{
		Type: TypeApply, APIVersion: "fake.openctl.io/v1", Kind: "FakeKind",
		ResourceName: "x", ManifestJSON: manifestB,
	})
	if err != nil {
		t.Fatal(err)
	}
	d.Notify()
	waitForStatus(t, store, op2.ID, StatusSucceeded, 2*time.Second)

	if p.applies.Load() != 2 {
		t.Errorf("spec change should miss the cache; provider.Apply called %d times, want 2", p.applies.Load())
	}
}

func TestVerifyingTraceCacheDisabledByIKnowFlag(t *testing.T) {
	p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}}
	store, d := newDispatcherWithManifests(t, p)
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	manifest := `{"apiVersion":"fake.openctl.io/v1","kind":"FakeKind","metadata":{"name":"x"},"spec":{"cores":2}}`
	manifestWithFlag := `{"apiVersion":"fake.openctl.io/v1","kind":"FakeKind","metadata":{"name":"x","annotations":{"openctl.io/i-know-this-breaks-the-cluster":"true"}},"spec":{"cores":2}}`

	op1, err := store.Submit(context.Background(), &Operation{
		Type: TypeApply, APIVersion: "fake.openctl.io/v1", Kind: "FakeKind",
		ResourceName: "x", ManifestJSON: manifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	d.Notify()
	waitForStatus(t, store, op1.ID, StatusSucceeded, 2*time.Second)

	op2, err := store.Submit(context.Background(), &Operation{
		Type: TypeApply, APIVersion: "fake.openctl.io/v1", Kind: "FakeKind",
		ResourceName: "x", ManifestJSON: manifestWithFlag,
	})
	if err != nil {
		t.Fatal(err)
	}
	d.Notify()
	waitForStatus(t, store, op2.ID, StatusSucceeded, 2*time.Second)

	if p.applies.Load() != 2 {
		t.Errorf("i-know flag should disable cache; provider.Apply called %d times, want 2", p.applies.Load())
	}
}
