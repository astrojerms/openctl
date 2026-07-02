package operations

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/storage"
	"github.com/openctl/openctl/pkg/protocol"
)

// recordingManifestSink is a minimal ManifestSink that records Delete
// calls so DeleteManifest's store-removal wiring can be asserted without
// a real manifests.Store.
type recordingManifestSink struct {
	deleted   []string // "apiVersion|kind|name" per Delete call
	deleteErr error
}

func (s *recordingManifestSink) Save(context.Context, *protocol.Resource) error { return nil }
func (s *recordingManifestSink) SaveWithRefsHash(context.Context, *protocol.Resource, string) error {
	return nil
}
func (s *recordingManifestSink) Delete(_ context.Context, apiVersion, kind, name string) error {
	s.deleted = append(s.deleted, apiVersion+"|"+kind+"|"+name)
	return s.deleteErr
}
func (s *recordingManifestSink) LoadHash(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (s *recordingManifestSink) LoadHashes(context.Context, string, string, string) (string, string, error) {
	return "", "", nil
}
func (s *recordingManifestSink) Hash(*protocol.Resource) string { return "" }

func newDispatcherWithSink(t *testing.T, p *fakeProvider, sink ManifestSink) *Dispatcher {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := New(db, 50)
	reg := providers.NewRegistry()
	reg.Register(p)
	return NewDispatcher(store, reg, sink, 50*time.Millisecond)
}

func childManifest(apiVersion, kind, name string) *protocol.Resource {
	return &protocol.Resource{
		APIVersion: apiVersion,
		Kind:       kind,
		Metadata:   protocol.ResourceMetadata{Name: name},
	}
}

// TestDeleteManifest_CallsProviderAndSink is the happy path: DeleteManifest
// routes through provider.Delete and removes the manifest-store row.
func TestDeleteManifest_CallsProviderAndSink(t *testing.T) {
	p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}}
	sink := &recordingManifestSink{}
	d := newDispatcherWithSink(t, p, sink)

	if err := d.DeleteManifest(context.Background(), childManifest("fake.openctl.io/v1", "FakeKind", "x")); err != nil {
		t.Fatalf("DeleteManifest: %v", err)
	}
	if p.deletes.Load() != 1 {
		t.Errorf("provider Delete called %d times, want 1", p.deletes.Load())
	}
	if len(sink.deleted) != 1 || sink.deleted[0] != "fake.openctl.io/v1|FakeKind|x" {
		t.Errorf("sink deleted = %v, want [fake.openctl.io/v1|FakeKind|x]", sink.deleted)
	}
}

// TestDeleteManifest_NilManifestsStillDeletes: with no manifest sink
// configured (as in unit tests), DeleteManifest still runs provider.Delete
// and doesn't panic.
func TestDeleteManifest_NilManifestsStillDeletes(t *testing.T) {
	p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}}
	_, d := newDispatcherWithStore(t, p) // nil manifests

	if err := d.DeleteManifest(context.Background(), childManifest("fake.openctl.io/v1", "FakeKind", "x")); err != nil {
		t.Fatalf("DeleteManifest: %v", err)
	}
	if p.deletes.Load() != 1 {
		t.Errorf("provider Delete called %d times, want 1", p.deletes.Load())
	}
}

// TestDeleteManifest_PropagatesProviderErrorAndSkipsSink: a provider.Delete
// failure is returned to the caller and the manifest row is left intact
// (we only drop the row once the provider confirms the resource is gone).
func TestDeleteManifest_PropagatesProviderErrorAndSkipsSink(t *testing.T) {
	p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}, deleteErr: errors.New("boom")}
	sink := &recordingManifestSink{}
	d := newDispatcherWithSink(t, p, sink)

	err := d.DeleteManifest(context.Background(), childManifest("fake.openctl.io/v1", "FakeKind", "x"))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want provider error surfaced, got %v", err)
	}
	if len(sink.deleted) != 0 {
		t.Errorf("manifest sink must not be touched when provider.Delete fails, got %v", sink.deleted)
	}
}

// TestDeleteManifest_UnknownAPIVersion: no provider for the apiVersion is a
// hard error, and provider.Delete is never reached.
func TestDeleteManifest_UnknownAPIVersion(t *testing.T) {
	p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}}
	_, d := newDispatcherWithStore(t, p)

	err := d.DeleteManifest(context.Background(), childManifest("nope.openctl.io/v1", "Other", "x"))
	if err == nil || !strings.Contains(err.Error(), "no provider for apiVersion") {
		t.Fatalf("want no-provider error, got %v", err)
	}
	if p.deletes.Load() != 0 {
		t.Errorf("provider Delete should not be called for an unknown apiVersion, got %d", p.deletes.Load())
	}
}

// TestDeleteChild_ReachableViaCtxAndIdempotent proves the ChildDispatcher
// wiring: a provider that pulls the dispatcher off ctx can DeleteChild, and
// repeating it is safe (the provider reports already-absent as nil).
func TestDeleteChild_ReachableViaCtxAndIdempotent(t *testing.T) {
	p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}}
	_, d := newDispatcherWithStore(t, p)

	ctx := WithChildDispatcher(context.Background(), d)
	cd, ok := ChildDispatcherFrom(ctx)
	if !ok {
		t.Fatal("no ChildDispatcher on ctx")
	}
	m := childManifest("fake.openctl.io/v1", "FakeKind", "x")
	if err := cd.DeleteChild(ctx, m); err != nil {
		t.Fatalf("DeleteChild (first): %v", err)
	}
	if err := cd.DeleteChild(ctx, m); err != nil {
		t.Fatalf("DeleteChild (repeat, should be idempotent): %v", err)
	}
	if p.deletes.Load() != 2 {
		t.Errorf("provider Delete called %d times, want 2", p.deletes.Load())
	}
}
