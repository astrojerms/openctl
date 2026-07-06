package providertest

import (
	"context"
	"errors"
	"runtime"
	"testing"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/pkg/protocol"
)

// memProvider is a minimal, fully-compliant in-memory CRUD provider. It backs
// both the happy-path tests (the battery must pass a correct implementation)
// and — subclassed below — the teeth tests (the battery must fail a broken
// one). noop switches it between update-on-reapply and atomic
// no-op-on-existing semantics.
type memProvider struct {
	store map[string]*protocol.Resource
	noop  bool
}

func newMem(noop bool) *memProvider {
	return &memProvider{store: map[string]*protocol.Resource{}, noop: noop}
}

func (m *memProvider) Name() string    { return "mem" }
func (m *memProvider) Kinds() []string { return []string{"Widget"} }
func memKey(kind, name string) string  { return kind + "/" + name }
func memAPI() string                   { return "mem.openctl.io/v1" }

func (m *memProvider) Apply(_ context.Context, r *protocol.Resource) (*protocol.Resource, error) {
	key := memKey(r.Kind, r.Metadata.Name)
	if m.noop {
		if existing, ok := m.store[key]; ok {
			return existing, nil
		}
	}
	obs := &protocol.Resource{
		APIVersion: memAPI(),
		Kind:       r.Kind,
		Metadata:   protocol.ResourceMetadata{Name: r.Metadata.Name},
		Spec:       r.Spec,
		Status:     map[string]any{"phase": "Ready"},
	}
	m.store[key] = obs
	return obs, nil
}

func (m *memProvider) Get(_ context.Context, kind, name string) (*protocol.Resource, error) {
	if r, ok := m.store[memKey(kind, name)]; ok {
		return r, nil
	}
	return nil, providers.NotFound(kind, name)
}

func (m *memProvider) List(_ context.Context, kind string) ([]*protocol.Resource, error) {
	var out []*protocol.Resource
	for _, r := range m.store {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memProvider) Delete(_ context.Context, kind, name string) error {
	delete(m.store, memKey(kind, name))
	return nil
}

func widgetManifest(name string) *protocol.Resource {
	return &protocol.Resource{
		APIVersion: memAPI(),
		Kind:       "Widget",
		Metadata:   protocol.ResourceMetadata{Name: name},
		Spec:       map[string]any{"size": "M"},
	}
}

// The battery passes a correct CRUD (update-on-reapply) provider.
func TestSuitePassesCompliantCRUDProvider(t *testing.T) {
	Suite{
		NewProvider:  func(_ *testing.T) (providers.Provider, func()) { return newMem(false), func() {} },
		Kind:         "Widget",
		Manifest:     widgetManifest,
		Capabilities: Capabilities{SupportsList: true},
	}.Run(t)
}

// The battery passes a correct atomic (no-op-on-existing) provider, exercising
// the NoOpOnExisting branch.
func TestSuitePassesCompliantAtomicProvider(t *testing.T) {
	Suite{
		NewProvider:  func(_ *testing.T) (providers.Provider, func()) { return newMem(true), func() {} },
		Kind:         "Widget",
		Manifest:     widgetManifest,
		Capabilities: Capabilities{SupportsList: true, NoOpOnExisting: true},
	}.Run(t)
}

// --- Teeth: the battery must FAIL non-conforming providers ---

// recorder is a testingT double that records failure/skip and, like the real
// *testing.T, aborts the current check on Fatal*/Skip via runtime.Goexit.
type recorder struct {
	failed  bool
	skipped bool
}

func (r *recorder) Helper()               {}
func (r *recorder) Error(...any)          { r.failed = true }
func (r *recorder) Errorf(string, ...any) { r.failed = true }
func (r *recorder) Fatal(...any)          { r.failed = true; runtime.Goexit() }
func (r *recorder) Fatalf(string, ...any) { r.failed = true; runtime.Goexit() }
func (r *recorder) Skip(...any)           { r.skipped = true; runtime.Goexit() }

// runCheck runs one assertion against p in a goroutine so a Fatal/Skip
// (runtime.Goexit) unwinds only that goroutine, then returns the recorder.
func runCheck(assert func(testingT, providers.Provider), p providers.Provider) *recorder {
	rec := &recorder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		assert(rec, p)
	}()
	<-done
	return rec
}

// notIdempotentDelete violates "delete-on-missing is success".
type notIdempotentDelete struct{ *memProvider }

func (b notIdempotentDelete) Delete(_ context.Context, kind, name string) error {
	if _, ok := b.store[memKey(kind, name)]; !ok {
		return errors.New("resource not found")
	}
	delete(b.store, memKey(kind, name))
	return nil
}

// untypedNotFound violates "missing Get returns *providers.NotFoundError".
type untypedNotFound struct{ *memProvider }

func (b untypedNotFound) Get(_ context.Context, kind, name string) (*protocol.Resource, error) {
	if r, ok := b.store[memKey(kind, name)]; ok {
		return r, nil
	}
	return nil, errors.New("nope") // not a NotFoundError
}

// deleteDoesNotRemove violates "after Delete, Get is NotFound".
type deleteDoesNotRemove struct{ *memProvider }

func (b deleteDoesNotRemove) Delete(_ context.Context, _, _ string) error { return nil }

// bumpOnReapply mutates observed state on every Apply — a NoOpOnExisting
// violation.
type bumpOnReapply struct {
	store map[string]int
}

func (b *bumpOnReapply) Name() string    { return "bump" }
func (b *bumpOnReapply) Kinds() []string { return []string{"Widget"} }
func (b *bumpOnReapply) Apply(_ context.Context, r *protocol.Resource) (*protocol.Resource, error) {
	if b.store == nil {
		b.store = map[string]int{}
	}
	key := memKey(r.Kind, r.Metadata.Name)
	b.store[key]++
	return &protocol.Resource{
		APIVersion: "bump.openctl.io/v1",
		Kind:       r.Kind,
		Metadata:   protocol.ResourceMetadata{Name: r.Metadata.Name},
		Spec:       map[string]any{"applyCount": b.store[key]},
	}, nil
}
func (b *bumpOnReapply) Get(_ context.Context, kind, name string) (*protocol.Resource, error) {
	if n, ok := b.store[memKey(kind, name)]; ok {
		return &protocol.Resource{APIVersion: "bump.openctl.io/v1", Kind: kind, Metadata: protocol.ResourceMetadata{Name: name}, Spec: map[string]any{"applyCount": n}}, nil
	}
	return nil, providers.NotFound(kind, name)
}
func (b *bumpOnReapply) List(context.Context, string) ([]*protocol.Resource, error) { return nil, nil }
func (b *bumpOnReapply) Delete(_ context.Context, kind, name string) error {
	delete(b.store, memKey(kind, name))
	return nil
}

func TestSuiteHasTeeth(t *testing.T) {
	s := Suite{Kind: "Widget", Manifest: widgetManifest}

	if rec := runCheck(s.assertDeleteIdempotent, notIdempotentDelete{newMem(false)}); !rec.failed {
		t.Error("assertDeleteIdempotent passed a provider that errors on delete-of-missing")
	}
	if rec := runCheck(s.assertGetMissing, untypedNotFound{newMem(false)}); !rec.failed {
		t.Error("assertGetMissing passed a provider that returns an untyped not-found error")
	}
	if rec := runCheck(s.assertDeleteRemoves, deleteDoesNotRemove{newMem(false)}); !rec.failed {
		t.Error("assertDeleteRemoves passed a provider whose Delete does not remove")
	}

	// NoOpOnExisting must catch a provider that mutates on re-Apply.
	atomicSuite := Suite{Kind: "Widget", Manifest: widgetManifest, Capabilities: Capabilities{NoOpOnExisting: true}}
	if rec := runCheck(atomicSuite.assertApplyRepeat, &bumpOnReapply{}); !rec.failed {
		t.Error("assertApplyRepeat (NoOpOnExisting) passed a provider that mutates on re-Apply")
	}

	// The List assertion must SKIP (not fail) when the provider opts out of
	// List via Capabilities.SupportsList=false.
	if rec := runCheck(s.assertList, newMem(false)); !rec.skipped || rec.failed {
		t.Errorf("assertList with SupportsList=false: skipped=%v failed=%v, want skipped and not failed", rec.skipped, rec.failed)
	}
}
