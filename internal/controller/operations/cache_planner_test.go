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

// plannerFakeProvider is a fakeProvider that also implements providers.Planner,
// so the dispatcher treats it as a composite.
type plannerFakeProvider struct {
	fakeProvider
}

func (p *plannerFakeProvider) Plan(_ context.Context, _ *protocol.Resource) (*providers.PlanResult, error) {
	return &providers.PlanResult{}, nil
}

// A composite (Planner) provider never gets a verifying-trace cache hit: even
// with an identical manifest, the second apply must re-run provider.Apply so a
// child that drifted out-of-band (e.g. a deleted VM) is reconciled. This is the
// fix for the recovery bug where a re-apply cache-hit did nothing.
func TestVerifyingCacheSkippedForComposite(t *testing.T) {
	out := &protocol.Resource{
		APIVersion: "fake.openctl.io/v1", Kind: "FakeKind",
		Metadata: protocol.ResourceMetadata{Name: "x"},
	}
	p := &plannerFakeProvider{fakeProvider{name: "fake", kinds: []string{"FakeKind"}, applyOut: out, getOut: out}}

	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := New(db, 50)
	reg := providers.NewRegistry()
	reg.Register(p)
	d := NewDispatcher(store, reg, manifests.New(db), 50*time.Millisecond)

	manifest := `{"apiVersion":"fake.openctl.io/v1","kind":"FakeKind","metadata":{"name":"x"},"spec":{"cores":2}}`

	first, err := store.Submit(context.Background(), &Operation{
		Type: TypeApply, APIVersion: "fake.openctl.io/v1", Kind: "FakeKind",
		ResourceName: "x", ManifestJSON: manifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	runPendingOp(t, d, store, first.ID, StatusSucceeded)
	if p.applies.Load() != 1 {
		t.Fatalf("first apply: got %d, want 1", p.applies.Load())
	}

	// Second apply, identical manifest — a composite must NOT cache-hit.
	second, err := store.Submit(context.Background(), &Operation{
		Type: TypeApply, APIVersion: "fake.openctl.io/v1", Kind: "FakeKind",
		ResourceName: "x", ManifestJSON: manifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	runPendingOp(t, d, store, second.ID, StatusSucceeded)
	if p.applies.Load() != 2 {
		t.Errorf("second apply on a composite: provider.Apply called %d times, want 2 (no cache hit)", p.applies.Load())
	}
}
