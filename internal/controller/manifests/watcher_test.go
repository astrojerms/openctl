package manifests

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/openctl/openctl/internal/controller/storage"
	"github.com/openctl/openctl/pkg/protocol"
)

func newWatcherTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(context.Background(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}

// waitFor polls f until it returns true or timeout expires. Returns
// true on success. Used to bridge async watcher events into a
// deterministic assertion without sprinkling arbitrary sleeps.
func waitFor(timeout time.Duration, f func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if f() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return f()
}

func TestWatcherAppliesFreshFile(t *testing.T) {
	root := t.TempDir()
	store := newWatcherTestStore(t)

	// Mirror layout: <root>/proxmox.openctl.io/v1/VirtualMachine/foo.yaml
	dir := filepath.Join(root, "proxmox.openctl.io", "v1", "VirtualMachine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var mu sync.Mutex
	var applied []*protocol.Resource
	applyFn := func(_ context.Context, r *protocol.Resource) error {
		mu.Lock()
		defer mu.Unlock()
		applied = append(applied, r)
		return nil
	}
	w := NewWatcher(root, store, applyFn, nil)
	w.debounce = 50 * time.Millisecond
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer w.Stop()

	// Write a fresh manifest: watcher should apply.
	yaml := `apiVersion: proxmox.openctl.io/v1
kind: VirtualMachine
metadata:
  name: foo
spec:
  node: pve1
`
	if err := os.WriteFile(filepath.Join(dir, "foo.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if !waitFor(2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(applied) >= 1
	}) {
		t.Fatal("watcher never applied the fresh file")
	}
	mu.Lock()
	got := applied[0]
	mu.Unlock()
	if got.Metadata.Name != "foo" || got.APIVersion != "proxmox.openctl.io/v1" {
		t.Errorf("applied wrong resource: %+v", got)
	}
}

func TestWatcherSkipsUnchangedFile(t *testing.T) {
	root := t.TempDir()
	store := newWatcherTestStore(t)

	// Pre-populate the store with the same content the file will
	// carry — the watcher should recognise "no change" and skip.
	preloaded := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata:   protocol.ResourceMetadata{Name: "foo"},
		Spec:       map[string]any{"node": "pve1"},
	}
	if err := store.Save(context.Background(), preloaded); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dir := filepath.Join(root, "proxmox.openctl.io", "v1", "VirtualMachine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var mu sync.Mutex
	var applies int
	applyFn := func(_ context.Context, _ *protocol.Resource) error {
		mu.Lock()
		defer mu.Unlock()
		applies++
		return nil
	}
	w := NewWatcher(root, store, applyFn, nil)
	w.debounce = 50 * time.Millisecond
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer w.Stop()

	// Write the same manifest to disk.
	yaml := `apiVersion: proxmox.openctl.io/v1
kind: VirtualMachine
metadata:
  name: foo
spec:
  node: pve1
`
	if err := os.WriteFile(filepath.Join(dir, "foo.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Wait past the debounce window so any apply would have fired.
	time.Sleep(500 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if applies != 0 {
		t.Errorf("apply calls = %d, want 0 (content matches store)", applies)
	}
}

func TestWatcherHonoursDeleteWhenConfigured(t *testing.T) {
	root := t.TempDir()
	store := newWatcherTestStore(t)

	dir := filepath.Join(root, "proxmox.openctl.io", "v1", "VirtualMachine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var mu sync.Mutex
	var deletes []string
	deleteFn := func(_ context.Context, _, _, name string) error {
		mu.Lock()
		defer mu.Unlock()
		deletes = append(deletes, name)
		return nil
	}
	w := NewWatcher(root, store, func(context.Context, *protocol.Resource) error { return nil }, deleteFn)
	w.debounce = 50 * time.Millisecond
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer w.Stop()

	path := filepath.Join(dir, "gone.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: x\nkind: y\nmetadata:\n  name: gone\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Let create+debounce settle before removing.
	time.Sleep(200 * time.Millisecond)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if !waitFor(2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, n := range deletes {
			if n == "gone" {
				return true
			}
		}
		return false
	}) {
		t.Fatal("watcher never called delete for the removed file")
	}
}
