package manifests

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReconcileReMaterializesMissingFile(t *testing.T) {
	m, root := newDiskMirror(t)
	ctx := context.Background()

	if err := m.Save(ctx, sampleVM(2)); err != nil {
		t.Fatal(err)
	}
	// Simulate the user (or a stale install) wiping the file.
	target := filepath.Join(root, "proxmox.openctl.io", "v1", "VirtualMachine", "vm-1.yaml")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	report, err := m.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(report.MissingOnDisk) != 1 || report.MissingOnDisk[0].Name != "vm-1" {
		t.Errorf("MissingOnDisk = %v, want [vm-1]", report.MissingOnDisk)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("file should be re-materialized; stat err = %v", err)
	}
}

func TestReconcileFlagsOrphanFiles(t *testing.T) {
	m, root := newDiskMirror(t)
	ctx := context.Background()

	if err := m.Save(ctx, sampleVM(2)); err != nil {
		t.Fatal(err)
	}
	// Drop an orphan that doesn't correspond to any applied_manifests row.
	orphan := filepath.Join(root, "proxmox.openctl.io", "v1", "VirtualMachine", "rogue.yaml")
	if err := os.WriteFile(orphan, []byte("hand-authored\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := m.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.OrphansOnDisk) != 1 || !filepath.IsLocal(report.OrphansOnDisk[0]) {
		t.Errorf("OrphansOnDisk = %v, want single local path", report.OrphansOnDisk)
	}
	// Orphan must NOT be deleted.
	if _, err := os.Stat(orphan); err != nil {
		t.Errorf("orphan should be left alone; stat err = %v", err)
	}
}

func TestReconcileEmptyOnFreshController(t *testing.T) {
	m, _ := newDiskMirror(t)
	ctx := context.Background()
	report, err := m.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.MissingOnDisk) != 0 || len(report.OrphansOnDisk) != 0 {
		t.Errorf("fresh report = %+v, want empty", report)
	}
}
