package manifests

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl/internal/controller/storage"
	"github.com/openctl/openctl/pkg/protocol"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := storage.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}

func sampleVM(cpus int) *protocol.Resource {
	return &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata:   protocol.ResourceMetadata{Name: "vm-1", Labels: map[string]string{"env": "lab"}},
		Spec: map[string]any{
			"cpus":     cpus,
			"memoryMB": 2048,
		},
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Save(ctx, sampleVM(2)); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.Load(ctx, "proxmox.openctl.io/v1", "VirtualMachine", "vm-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil {
		t.Fatal("load: got nil, want a row")
	}
	// JSON round-trips numbers as float64.
	if got.Spec["cpus"] != float64(2) {
		t.Errorf("cpus = %v (%T), want 2", got.Spec["cpus"], got.Spec["cpus"])
	}
	if got.Metadata.Labels["env"] != "lab" {
		t.Errorf("labels.env = %q, want \"lab\"", got.Metadata.Labels["env"])
	}
}

func TestSaveOverwrites(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Save(ctx, sampleVM(2)); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, sampleVM(8)); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Load(ctx, "proxmox.openctl.io/v1", "VirtualMachine", "vm-1")
	if got == nil || got.Spec["cpus"] != float64(8) {
		t.Errorf("cpus = %v after re-save, want 8", got.Spec["cpus"])
	}
}

func TestLoadMissingReturnsNil(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	got, err := s.Load(ctx, "proxmox.openctl.io/v1", "VirtualMachine", "nope")
	if err != nil {
		t.Fatalf("load on missing: %v", err)
	}
	if got != nil {
		t.Errorf("load on missing returned %v, want nil", got)
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_ = s.Save(ctx, sampleVM(2))
	if err := s.Delete(ctx, "proxmox.openctl.io/v1", "VirtualMachine", "vm-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ := s.Load(ctx, "proxmox.openctl.io/v1", "VirtualMachine", "vm-1")
	if got != nil {
		t.Errorf("load after delete returned %v, want nil", got)
	}
}

func TestDeleteOnMissingIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Delete(ctx, "proxmox.openctl.io/v1", "VirtualMachine", "nope"); err != nil {
		t.Errorf("delete on missing: %v", err)
	}
}

func TestLoadHashReturnsStoredHash(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	r := sampleVM(2)
	if err := s.Save(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadHash(ctx, "proxmox.openctl.io/v1", "VirtualMachine", "vm-1")
	if err != nil {
		t.Fatalf("LoadHash: %v", err)
	}
	if got == "" {
		t.Fatal("LoadHash returned empty string for present row")
	}
	if got != Hash(r) {
		t.Errorf("stored hash %q != Hash(r) %q", got, Hash(r))
	}
}

func TestLoadHashReturnsEmptyForMissingRow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	got, err := s.LoadHash(ctx, "proxmox.openctl.io/v1", "VirtualMachine", "nope")
	if err != nil {
		t.Fatalf("LoadHash on missing: %v", err)
	}
	if got != "" {
		t.Errorf("LoadHash on missing returned %q, want \"\"", got)
	}
}

func TestSaveOverwritesHash(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_ = s.Save(ctx, sampleVM(2))
	h1, _ := s.LoadHash(ctx, "proxmox.openctl.io/v1", "VirtualMachine", "vm-1")

	_ = s.Save(ctx, sampleVM(8))
	h2, _ := s.LoadHash(ctx, "proxmox.openctl.io/v1", "VirtualMachine", "vm-1")

	if h1 == h2 {
		t.Error("hashes should differ after spec change; got identical")
	}
}
