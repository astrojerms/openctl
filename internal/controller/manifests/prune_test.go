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
// (composite children, hand-managed cli/ui resources) skip.
func TestPrunerGuards(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := newWatcherTestStore(t)

	save := func(name string, labels map[string]string) {
		r := &protocol.Resource{APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine"}
		r.Metadata.Name = name
		r.Metadata.Labels = labels
		r.Spec = map[string]any{"x": 1}
		if err := store.Save(ctx, r); err != nil {
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

	save("keep", nil)                                                                      // file present → keep
	save("gone", nil)                                                                      // no file, gitops → prune
	save("unknown", nil)                                                                   // no file, provenance GC'd → prune
	save("manual", nil)                                                                    // no file, cli-sourced → keep
	save("child-owner", map[string]string{labelOwnerKind: "Cluster", labelOwnerName: "c"}) // child → keep
	save("child-k3s", map[string]string{labelK3sCluster: "c"})                             // child → keep

	sourceOf := func(_ context.Context, _, _, name string) (string, bool) {
		switch name {
		case "gone":
			return SourceGitOps, true
		case "manual":
			return SourceCLI, true
		default:
			return "", false // GC'd / never recorded
		}
	}
	var deleted []string
	del := func(_ context.Context, _, _, name string) error {
		deleted = append(deleted, name)
		return nil
	}

	pruned, err := NewPruner(store, root, sourceOf, del).Prune(ctx)
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
		if n == "keep" || n == "manual" || n == "child-owner" || n == "child-k3s" {
			t.Errorf("%s must not be pruned", n)
		}
	}
}

// TestPrunerNoSourceLookupStillGuardsChildren proves the composite-child guard
// holds even without a provenance lookup (source=nil): children are skipped,
// plain top-level resources are pruned.
func TestPrunerNoSourceLookupStillGuardsChildren(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := newWatcherTestStore(t)

	save := func(name string, labels map[string]string) {
		r := &protocol.Resource{APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine"}
		r.Metadata.Name = name
		r.Metadata.Labels = labels
		if err := store.Save(ctx, r); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	save("plain", nil)
	save("child", map[string]string{labelK3sCluster: "c"})

	var deleted []string
	pruned, err := NewPruner(store, root, nil, func(_ context.Context, _, _, name string) error {
		deleted = append(deleted, name)
		return nil
	}).Prune(ctx)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if !slices.Equal(deleted, []string{"plain"}) {
		t.Fatalf("deleted = %v, want [plain] (child guarded even with nil source)", deleted)
	}
	if len(pruned) != 1 {
		t.Errorf("pruned = %d, want 1", len(pruned))
	}
}
