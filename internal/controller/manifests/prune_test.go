package manifests

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

// TestPrunerGuards exercises every prune decision branch: file-present keeps,
// top-level gitops/unknown-provenance prunes, and the two safety guards
// (composite children, hand-managed cli/ui resources) skip. Provenance is read
// from the applied_manifests source column (K3), recorded via WithSource on the
// Save context — not a separate lookup.
func TestPrunerGuards(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := newWatcherTestStore(t)

	// save records the resource with the given provenance source (empty =
	// unknown, e.g. a row written before the source column existed).
	save := func(name, source string, labels map[string]string) {
		r := &protocol.Resource{APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine"}
		r.Metadata.Name = name
		r.Metadata.Labels = labels
		r.Spec = map[string]any{"x": 1}
		if err := store.Save(WithSource(ctx, source), r); err != nil {
			t.Fatalf("save %s: %v", name, err)
		}
	}

	// "keep" has a live file in the mirror → still desired.
	dir := filepath.Join(root, "proxmox.openctl.io", "v1", "VirtualMachine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.yaml"),
		[]byte("apiVersion: proxmox.openctl.io/v1\nkind: VirtualMachine\nmetadata:\n  name: keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	save("keep", SourceGitOps, nil)                                                                      // file present → keep
	save("gone", SourceGitOps, nil)                                                                      // no file, gitops → prune
	save("unknown", "", nil)                                                                             // no file, provenance unknown → prune
	save("manual", SourceCLI, nil)                                                                       // no file, cli-sourced → keep
	save("manual-ui", SourceUI, nil)                                                                     // no file, ui-sourced → keep
	save("child-owner", SourceGitOps, map[string]string{labelOwnerKind: "Cluster", labelOwnerName: "c"}) // child → keep
	save("child-k3s", SourceGitOps, map[string]string{labelK3sCluster: "c"})                             // child → keep

	var deleted []string
	del := func(_ context.Context, _, _, name string) error {
		deleted = append(deleted, name)
		return nil
	}

	pruned, err := NewPruner(store, root, del).Prune(ctx)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	slices.Sort(deleted)
	if want := []string{"gone", "unknown"}; !slices.Equal(deleted, want) {
		t.Fatalf("deleted = %v, want %v", deleted, want)
	}
	if len(pruned) != 2 {
		t.Errorf("pruned count = %d, want 2", len(pruned))
	}
	// Belt-and-suspenders: the protected ones must never appear.
	for _, n := range deleted {
		if n == "keep" || n == "manual" || n == "manual-ui" || n == "child-owner" || n == "child-k3s" {
			t.Errorf("%s must not be pruned", n)
		}
	}
}

// TestPrunerChildGuardIndependentOfSource proves the composite-child guard holds
// regardless of provenance: a child with no recorded source (empty) is still
// skipped, while a plain top-level resource with unknown provenance is pruned.
func TestPrunerChildGuardIndependentOfSource(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := newWatcherTestStore(t)

	save := func(name string, labels map[string]string) {
		r := &protocol.Resource{APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine"}
		r.Metadata.Name = name
		r.Metadata.Labels = labels
		if err := store.Save(ctx, r); err != nil { // no source → unknown provenance
			t.Fatalf("save: %v", err)
		}
	}
	save("plain", nil)
	save("child", map[string]string{labelK3sCluster: "c"})

	var deleted []string
	pruned, err := NewPruner(store, root, func(_ context.Context, _, _, name string) error {
		deleted = append(deleted, name)
		return nil
	}).Prune(ctx)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if !slices.Equal(deleted, []string{"plain"}) {
		t.Fatalf("deleted = %v, want [plain] (child guarded even with unknown source)", deleted)
	}
	if len(pruned) != 1 {
		t.Errorf("pruned = %d, want 1", len(pruned))
	}
}
