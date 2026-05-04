package operations

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/storage"
	"github.com/openctl/openctl/pkg/protocol"
)

// fakeProvider records calls and returns canned responses for the
// dispatcher tests.
type fakeProvider struct {
	name      string
	kinds     []string
	applies   atomic.Int32
	deletes   atomic.Int32
	applyErr  error
	deleteErr error
	applyOut  *protocol.Resource
}

func (f *fakeProvider) Name() string    { return f.name }
func (f *fakeProvider) Kinds() []string { return f.kinds }
func (f *fakeProvider) Apply(_ context.Context, m *protocol.Resource) (*protocol.Resource, error) {
	f.applies.Add(1)
	if f.applyErr != nil {
		return nil, f.applyErr
	}
	if f.applyOut != nil {
		return f.applyOut, nil
	}
	return m, nil
}
func (f *fakeProvider) Get(_ context.Context, _, _ string) (*protocol.Resource, error) {
	return nil, nil
}
func (f *fakeProvider) List(_ context.Context, _ string) ([]*protocol.Resource, error) {
	return nil, nil
}
func (f *fakeProvider) Delete(_ context.Context, _, _ string) error {
	f.deletes.Add(1)
	return f.deleteErr
}

func newDispatcherWithStore(t *testing.T, p *fakeProvider) (*Store, *Dispatcher) {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := New(db, 50)

	reg := providers.NewRegistry()
	reg.Register(p)
	d := NewDispatcher(store, reg, 50*time.Millisecond)
	return store, d
}

func waitForStatus(t *testing.T, store *Store, opID string, want string, _ time.Duration) *Operation {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		op, err := store.Get(context.Background(), opID)
		if err == nil && op.Status == want {
			return op
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for op %s to reach status %q", opID, want)
	return nil
}

func TestDispatcherProcessesApplyOp(t *testing.T) {
	p := &fakeProvider{
		name:  "fake",
		kinds: []string{"FakeKind"},
		applyOut: &protocol.Resource{
			APIVersion: "fake.openctl.io/v1",
			Kind:       "FakeKind",
			Metadata:   protocol.ResourceMetadata{Name: "x"},
			Status:     map[string]any{"applied": true},
		},
	}
	store, d := newDispatcherWithStore(t, p)
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	op, err := store.Submit(context.Background(), &Operation{
		Type:         TypeApply,
		APIVersion:   "fake.openctl.io/v1",
		Kind:         "FakeKind",
		ResourceName: "x",
		ManifestJSON: `{"apiVersion":"fake.openctl.io/v1","kind":"FakeKind","metadata":{"name":"x"}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	d.Notify()

	final := waitForStatus(t, store, op.ID, StatusSucceeded, 2*time.Second)
	if p.applies.Load() != 1 {
		t.Errorf("provider Apply called %d times, want 1", p.applies.Load())
	}
	if final.ResultJSON == "" {
		t.Error("ResultJSON is empty")
	}
}

func TestDispatcherProcessesDeleteOp(t *testing.T) {
	p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}}
	store, d := newDispatcherWithStore(t, p)
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	op, _ := store.Submit(context.Background(), &Operation{
		Type:         TypeDelete,
		APIVersion:   "fake.openctl.io/v1",
		Kind:         "FakeKind",
		ResourceName: "x",
	})
	d.Notify()

	waitForStatus(t, store, op.ID, StatusSucceeded, 2*time.Second)
	if p.deletes.Load() != 1 {
		t.Errorf("provider Delete called %d times, want 1", p.deletes.Load())
	}
}

func TestDispatcherFailsOpWhenProviderErrors(t *testing.T) {
	p := &fakeProvider{
		name:     "fake",
		kinds:    []string{"FakeKind"},
		applyErr: errors.New("proxmox unreachable"),
	}
	store, d := newDispatcherWithStore(t, p)
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	op, _ := store.Submit(context.Background(), &Operation{
		Type:         TypeApply,
		APIVersion:   "fake.openctl.io/v1",
		Kind:         "FakeKind",
		ResourceName: "x",
		ManifestJSON: `{"apiVersion":"fake.openctl.io/v1","kind":"FakeKind","metadata":{"name":"x"}}`,
	})
	d.Notify()

	final := waitForStatus(t, store, op.ID, StatusFailed, 2*time.Second)
	if final.Error == "" {
		t.Error("Error is empty")
	}
}

func TestDispatcherFailsOpWhenProviderUnregistered(t *testing.T) {
	p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}}
	store, d := newDispatcherWithStore(t, p)
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	// Submit an op for a provider we DIDN'T register.
	op, _ := store.Submit(context.Background(), &Operation{
		Type:         TypeApply,
		APIVersion:   "missing.openctl.io/v1",
		Kind:         "Whatever",
		ResourceName: "x",
		ManifestJSON: `{}`,
	})
	d.Notify()

	final := waitForStatus(t, store, op.ID, StatusFailed, 2*time.Second)
	if final.Error == "" {
		t.Error("Error is empty")
	}
}

func TestDispatcherStopBlocksUntilDone(t *testing.T) {
	p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}}
	_, d := newDispatcherWithStore(t, p)
	d.Start(context.Background())

	// Should return promptly even with no work.
	stopped := make(chan struct{})
	go func() { d.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return within 2s")
	}
}
