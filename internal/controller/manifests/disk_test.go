package manifests

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/openctl/openctl/pkg/protocol"
)

func newDiskMirror(t *testing.T) (*DiskMirror, string) {
	t.Helper()
	store := newTestStore(t)
	root := t.TempDir()
	return NewDiskMirror(store, root), root
}

func TestDiskMirrorSaveWritesYAML(t *testing.T) {
	m, root := newDiskMirror(t)
	ctx := context.Background()

	if err := m.Save(ctx, sampleVM(4)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	want := filepath.Join(root, "proxmox.openctl.io", "v1", "VirtualMachine", "vm-1.yaml")
	data, err := os.ReadFile(want) // #nosec G304 -- path built from test root
	if err != nil {
		t.Fatalf("read materialized file: %v", err)
	}
	var decoded protocol.Resource
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode yaml: %v", err)
	}
	if decoded.Kind != "VirtualMachine" || decoded.Metadata.Name != "vm-1" {
		t.Errorf("decoded resource = %+v, want kind=VirtualMachine name=vm-1", decoded)
	}
}

func TestDiskMirrorDeleteRemovesFileAndEmptyDirs(t *testing.T) {
	m, root := newDiskMirror(t)
	ctx := context.Background()

	if err := m.Save(ctx, sampleVM(4)); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, "proxmox.openctl.io/v1", "VirtualMachine", "vm-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// File gone.
	if _, err := os.Stat(filepath.Join(root, "proxmox.openctl.io", "v1", "VirtualMachine", "vm-1.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected file gone, got err=%v", err)
	}
	// Empty parent dirs cleaned up, root preserved.
	for _, sub := range []string{
		filepath.Join(root, "proxmox.openctl.io", "v1", "VirtualMachine"),
		filepath.Join(root, "proxmox.openctl.io", "v1"),
		filepath.Join(root, "proxmox.openctl.io"),
	} {
		if _, err := os.Stat(sub); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("expected empty parent %s pruned, got err=%v", sub, err)
		}
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("root should remain: %v", err)
	}
}

func TestDiskMirrorDeleteOnMissingFileIsNoop(t *testing.T) {
	m, _ := newDiskMirror(t)
	ctx := context.Background()
	if err := m.Delete(ctx, "proxmox.openctl.io/v1", "VirtualMachine", "ghost"); err != nil {
		t.Errorf("Delete on missing: %v", err)
	}
}

func TestDiskMirrorSaveOverwritesAtomically(t *testing.T) {
	m, root := newDiskMirror(t)
	ctx := context.Background()

	if err := m.Save(ctx, sampleVM(2)); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(ctx, sampleVM(16)); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(root, "proxmox.openctl.io", "v1", "VirtualMachine", "vm-1.yaml")
	data, _ := os.ReadFile(dst) // #nosec G304 -- path built from test root
	if !strings.Contains(string(data), "cpus: 16") {
		t.Errorf("overwrite didn't take; body = %s", data)
	}
	// No leftover .tmp files in the directory.
	entries, _ := os.ReadDir(filepath.Dir(dst))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("leftover temp file %q", e.Name())
		}
	}
}

func TestDiskMirrorRejectsPathTraversalInName(t *testing.T) {
	m, root := newDiskMirror(t)
	ctx := context.Background()

	r := sampleVM(2)
	r.Metadata.Name = "../escape"
	if err := m.Save(ctx, r); err != nil {
		t.Fatalf("Save with hostile name: %v", err)
	}
	// Must NOT have written outside the root.
	escapePath := filepath.Join(filepath.Dir(root), "escape.yaml")
	if _, err := os.Stat(escapePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("hostile name escaped root: %v exists", escapePath)
	}
	// And the safe path is inside the root: "/" becomes "_", so
	// "../escape" lands at ".._escape.yaml" under the kind dir.
	expected := filepath.Join(root, "proxmox.openctl.io", "v1", "VirtualMachine", ".._escape.yaml")
	if _, err := os.Stat(expected); err != nil {
		t.Errorf("expected scrubbed file %s: %v", expected, err)
	}
}

func TestDiskMirrorHashAndLoadHashDelegate(t *testing.T) {
	m, _ := newDiskMirror(t)
	ctx := context.Background()

	r := sampleVM(2)
	if err := m.Save(ctx, r); err != nil {
		t.Fatal(err)
	}
	if h := m.Hash(r); h != Hash(r) {
		t.Errorf("Hash delegate: got %q, want %q", h, Hash(r))
	}
	got, _ := m.LoadHash(ctx, "proxmox.openctl.io/v1", "VirtualMachine", "vm-1")
	if got != Hash(r) {
		t.Errorf("LoadHash delegate: got %q, want %q", got, Hash(r))
	}
}

func TestDiskMirrorHookFiresOnSaveAndDelete(t *testing.T) {
	m, _ := newDiskMirror(t)
	ctx := context.Background()

	var events []string
	m.SetHook(func(_ context.Context, kind string, r *protocol.Resource, verb string) error {
		events = append(events, verb+":"+kind+"/"+r.Metadata.Name)
		return nil
	})
	if err := m.Save(ctx, sampleVM(2)); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, "proxmox.openctl.io/v1", "VirtualMachine", "vm-1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"apply:VirtualMachine/vm-1", "delete:VirtualMachine/vm-1"}
	if len(events) != len(want) || events[0] != want[0] || events[1] != want[1] {
		t.Errorf("hook events = %v, want %v", events, want)
	}
}

func TestStoreListAllReturnsRefs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Save(ctx, sampleVM(2)); err != nil {
		t.Fatal(err)
	}
	r2 := sampleVM(4)
	r2.Metadata.Name = "vm-2"
	if err := s.Save(ctx, r2); err != nil {
		t.Fatal(err)
	}
	refs, err := s.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("ListAll len = %d, want 2", len(refs))
	}
	names := map[string]bool{refs[0].Name: true, refs[1].Name: true}
	if !names["vm-1"] || !names["vm-2"] {
		t.Errorf("ListAll names = %v, want vm-1 + vm-2", names)
	}
}
