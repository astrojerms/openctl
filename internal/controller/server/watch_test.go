package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/providers"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
)

// watchableProvider differs from fakeProvider in that List returns the
// applied set instead of a fixed [a, b] pair. Watch tests need the list
// to reflect mutations so ADD/DELETED events fire correctly.
type watchableProvider struct {
	mu        sync.Mutex
	resources map[string]*protocol.Resource
}

func newWatchableProvider() *watchableProvider {
	return &watchableProvider{resources: map[string]*protocol.Resource{}}
}

func (w *watchableProvider) Name() string    { return "fake" }
func (w *watchableProvider) Kinds() []string { return []string{"FakeKind"} }
func (w *watchableProvider) Apply(_ context.Context, r *protocol.Resource) (*protocol.Resource, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.resources[r.Metadata.Name] = r
	return r, nil
}
func (w *watchableProvider) Get(_ context.Context, _, name string) (*protocol.Resource, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.resources[name], nil
}
func (w *watchableProvider) List(_ context.Context, _ string) ([]*protocol.Resource, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]*protocol.Resource, 0, len(w.resources))
	for _, r := range w.resources {
		out = append(out, r)
	}
	return out, nil
}
func (w *watchableProvider) Delete(_ context.Context, _, name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.resources, name)
	return nil
}
func (w *watchableProvider) preload(r *protocol.Resource) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.resources[r.Metadata.Name] = r
}
func (w *watchableProvider) remove(name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.resources, name)
}

func startWatchTestServer(t *testing.T, fake *watchableProvider) (string, string) {
	addr, caPath, _ := startWatchTestServerWithManifests(t, fake)
	return addr, caPath
}

// startWatchTestServerWithManifests returns the manifest store so watch
// tests can pre-populate applied_manifests for the managed-only filter.
// Watch fires DELETED for resources missing from applied_manifests, so
// pre-existing observed resources need a corresponding row to be visible.
func startWatchTestServerWithManifests(t *testing.T, fake *watchableProvider) (string, string, *manifests.Store) {
	t.Helper()
	reg := providers.NewRegistry()
	reg.Register(fake)
	addr, mat, mstore := startTestServerWithManifests(t, reg)
	return addr, mat.CACertPath, mstore
}

func TestResourceWatchEmitsSnapshotThenLiveAdds(t *testing.T) {
	fake := newWatchableProvider()
	addr, caPath, mstore := startWatchTestServerWithManifests(t, fake)
	conn := dialTestServer(t, addr, caPath)
	defer func() { _ = conn.Close() }()

	// Pre-populate one VM so the snapshot phase emits it. Both the provider
	// (observed state) and applied_manifests (managed-only filter) need a
	// record — the filter hides anything not in applied_manifests.
	pre := &protocol.Resource{
		APIVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
		Metadata:   protocol.ResourceMetadata{Name: "pre-existing"},
	}
	fake.preload(pre)
	if err := mstore.Save(context.Background(), pre); err != nil {
		t.Fatalf("Save pre-existing: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+"") // no auth on test server
	stream, err := apiv1.NewResourceServiceClient(conn).Watch(ctx, &apiv1.WatchRequest{
		ApiVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
	})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	events := make(chan *apiv1.WatchEvent, 16)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			ev, err := stream.Recv()
			if err != nil {
				return
			}
			events <- ev
		}
	}()

	// First event(s): snapshot of pre-existing.
	got := mustReadEvent(t, events, 2*time.Second)
	if got.GetType() != apiv1.WatchEvent_ADDED || got.GetResource().GetMetadata().GetName() != "pre-existing" {
		t.Errorf("first event: got %v %q, want ADDED pre-existing", got.GetType(), got.GetResource().GetMetadata().GetName())
	}

	// Apply a new resource — should emit ADDED on the next poll tick.
	spec, _ := structpb.NewStruct(map[string]any{"x": "y"})
	_, err = apiv1.NewResourceServiceClient(conn).Apply(ctx, &apiv1.ApplyRequest{
		Resource: &apiv1.Resource{
			ApiVersion: "fake.openctl.io/v1", Kind: "FakeKind",
			Metadata: &apiv1.Metadata{Name: "live-add"}, Spec: spec,
		},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got = mustReadEvent(t, events, 3*time.Second)
	if got.GetType() != apiv1.WatchEvent_ADDED || got.GetResource().GetMetadata().GetName() != "live-add" {
		t.Errorf("live event: got %v %q, want ADDED live-add", got.GetType(), got.GetResource().GetMetadata().GetName())
	}

	cancel()
	wg.Wait()
}

func TestResourceWatchEmitsDeletedWhenResourceGone(t *testing.T) {
	fake := newWatchableProvider()
	addr, caPath, mstore := startWatchTestServerWithManifests(t, fake)
	conn := dialTestServer(t, addr, caPath)
	defer func() { _ = conn.Close() }()

	// Pre-populate; then remove from the fake mid-stream.
	pre := &protocol.Resource{
		APIVersion: "fake.openctl.io/v1", Kind: "FakeKind",
		Metadata: protocol.ResourceMetadata{Name: "transient"},
	}
	fake.preload(pre)
	if err := mstore.Save(context.Background(), pre); err != nil {
		t.Fatalf("Save transient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, _ := apiv1.NewResourceServiceClient(conn).Watch(ctx, &apiv1.WatchRequest{
		ApiVersion: "fake.openctl.io/v1", Kind: "FakeKind",
	})

	events := make(chan *apiv1.WatchEvent, 16)
	go func() {
		for {
			ev, err := stream.Recv()
			if err != nil {
				return
			}
			events <- ev
		}
	}()

	// Snapshot: ADDED transient.
	got := mustReadEvent(t, events, 2*time.Second)
	if got.GetType() != apiv1.WatchEvent_ADDED {
		t.Fatalf("want ADDED first, got %v", got.GetType())
	}

	// Remove from fake (simulates out-of-band deletion) — next poll fires DELETED.
	fake.remove("transient")
	got = mustReadEvent(t, events, 3*time.Second)
	if got.GetType() != apiv1.WatchEvent_DELETED || got.GetResource().GetMetadata().GetName() != "transient" {
		t.Errorf("want DELETED transient, got %v %q", got.GetType(), got.GetResource().GetMetadata().GetName())
	}
}

func TestWatchOperationsTerminatesOnTerminalForIDFilter(t *testing.T) {
	fake := newWatchableProvider()
	addr, caPath := startWatchTestServer(t, fake)
	conn := dialTestServer(t, addr, caPath)
	defer func() { _ = conn.Close() }()

	// Submit an apply that the dispatcher will quickly mark succeeded.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := apiv1.NewResourceServiceClient(conn).Apply(ctx, &apiv1.ApplyRequest{
		Resource: &apiv1.Resource{
			ApiVersion: "fake.openctl.io/v1", Kind: "FakeKind",
			Metadata: &apiv1.Metadata{Name: "watched"},
		},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	opID := resp.GetOperationId()

	stream, err := apiv1.NewOperationServiceClient(conn).WatchOperations(ctx, &apiv1.WatchOperationsRequest{
		Id: opID,
	})
	if err != nil {
		t.Fatalf("WatchOperations: %v", err)
	}

	// Drain until terminal=true. Should arrive promptly because the
	// dispatcher races to completion.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ev, err := stream.Recv()
		if err != nil {
			return // EOF after terminal; success.
		}
		if ev.GetTerminal() {
			// Stream should close cleanly shortly after — try one more Recv to confirm.
			_, err := stream.Recv()
			if err == nil {
				t.Error("expected stream to close after terminal=true, got another event")
			}
			return
		}
	}
	t.Fatal("timeout waiting for terminal=true on id-scoped watch")
}

func mustReadEvent(t *testing.T, ch <-chan *apiv1.WatchEvent, timeout time.Duration) *apiv1.WatchEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(timeout):
		t.Fatal("timeout waiting for watch event")
		return nil
	}
}
