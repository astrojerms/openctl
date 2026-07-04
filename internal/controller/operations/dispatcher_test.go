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
	gets      atomic.Int32
	deletes   atomic.Int32
	applyErr  error
	deleteErr error
	applyOut  *protocol.Resource
	getOut    *protocol.Resource
	// blockUntilCtx makes Apply block until its context is canceled, then
	// return ctx.Err() — used to exercise running-op cancellation.
	blockUntilCtx bool
	// applyStarted, when non-nil, is signaled once as Apply is entered so a
	// test can wait until the op is genuinely in flight.
	applyStarted chan struct{}
}

func (f *fakeProvider) Name() string    { return f.name }
func (f *fakeProvider) Kinds() []string { return f.kinds }
func (f *fakeProvider) Apply(ctx context.Context, m *protocol.Resource) (*protocol.Resource, error) {
	f.applies.Add(1)
	if f.applyStarted != nil {
		select {
		case f.applyStarted <- struct{}{}:
		default:
		}
	}
	if f.blockUntilCtx {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.applyErr != nil {
		return nil, f.applyErr
	}
	if f.applyOut != nil {
		return f.applyOut, nil
	}
	return m, nil
}
func (f *fakeProvider) Get(_ context.Context, _, _ string) (*protocol.Resource, error) {
	f.gets.Add(1)
	return f.getOut, nil
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
	d := NewDispatcher(store, reg, nil, 50*time.Millisecond)
	return store, d
}

func waitForStatus(t *testing.T, store *Store, opID string, want string, timeout time.Duration) *Operation {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastStatus string
	var lastErr error
	for time.Now().Before(deadline) {
		op, err := store.Get(context.Background(), opID)
		if err == nil && op.Status == want {
			return op
		}
		if err != nil {
			lastErr = err
		} else if op != nil {
			lastStatus = op.Status
			if op.Error != "" {
				lastErr = errors.New(op.Error)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("timeout waiting for op %s to reach status %q (last status %q, last error: %v)", opID, want, lastStatus, lastErr)
	}
	t.Fatalf("timeout waiting for op %s to reach status %q (last status %q)", opID, want, lastStatus)
	return nil
}

func runPendingOp(t *testing.T, d *Dispatcher, store *Store, opID string, want string) *Operation {
	t.Helper()
	op, err := store.ClaimNextPending(context.Background())
	if err != nil {
		t.Fatalf("ClaimNextPending: %v", err)
	}
	if op.ID != opID {
		t.Fatalf("ClaimNextPending got %s, want %s", op.ID, opID)
	}
	d.execute(context.Background(), op)
	final, err := store.Get(context.Background(), opID)
	if err != nil {
		t.Fatalf("Get %s: %v", opID, err)
	}
	if final.Status != want {
		t.Fatalf("op %s status = %q, want %q (error: %s)", opID, final.Status, want, final.Error)
	}
	return final
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

func TestCancelRunningOpCompletesAsCancelled(t *testing.T) {
	p := &fakeProvider{
		name:          "fake",
		kinds:         []string{"FakeKind"},
		blockUntilCtx: true,
		applyStarted:  make(chan struct{}, 1),
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

	// Wait until the provider's Apply is genuinely in flight before canceling.
	select {
	case <-p.applyStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("op never entered Apply")
	}

	if !d.CancelRunning(op.ID) {
		t.Fatal("CancelRunning returned false for an in-flight op")
	}
	got := waitForStatus(t, store, op.ID, StatusCancelled, 2*time.Second)
	if got.Error != "canceled by user while running" {
		t.Errorf("cancel message = %q, want the clean canceled message", got.Error)
	}
}

func TestCancelRunningUnknownOpIsNoop(t *testing.T) {
	p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}}
	_, d := newDispatcherWithStore(t, p)
	if d.CancelRunning("op-nonexistent") {
		t.Error("CancelRunning should return false for an unknown op")
	}
}

func TestDispatcherProcessesDeleteOp(t *testing.T) {
	p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}}
	store, d := newDispatcherWithStore(t, p)
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	op, err := store.Submit(context.Background(), &Operation{
		Type:         TypeDelete,
		APIVersion:   "fake.openctl.io/v1",
		Kind:         "FakeKind",
		ResourceName: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
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
	op, err := store.Submit(context.Background(), &Operation{
		Type:         TypeApply,
		APIVersion:   "missing.openctl.io/v1",
		Kind:         "Whatever",
		ResourceName: "x",
		ManifestJSON: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
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
