package manifests

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Pruner implements the git-as-source repo-wide prune: after a pull, delete the
// managed resources whose manifest file was removed from the mirror, so the
// repo becomes the desired SET rather than an additive source.
//
// It is deliberately conservative — it deletes real infrastructure, so every
// path errs toward NOT deleting:
//   - A resource whose file is still on disk is desired → never a candidate.
//   - A composite child (owner labels, or the operative k3s cluster label) is
//     owned by its parent; deleting the parent cascades to it, so the Pruner
//     never deletes children independently.
//   - A resource whose latest apply was explicitly cli- or ui-sourced is
//     protected — the operator manages it by hand, not via the repo.
//
// Provenance is read directly from the applied_manifests row's source column
// (K3), which the dispatcher records on every apply — no longer reconstructed
// from the GC'd operations table. An empty source ("provenance unknown", e.g. a
// row written before the source column existed) is not protected, matching the
// prior GC'd-op behavior.
//
// Everything else absent from the repo (top-level, gitops/auto-reconcile or
// unknown provenance) is deleted through the same Delete-op path the watcher's
// deleteOnRemove uses.
type Pruner struct {
	store *Store
	root  string
	del   GitOpsDeleteFunc
}

// NewPruner constructs a Pruner. del is required: it submits the actual Delete.
func NewPruner(store *Store, root string, del GitOpsDeleteFunc) *Pruner {
	return &Pruner{store: store, root: root, del: del}
}

// Prune runs one pass and returns the refs it deleted. Errors from individual
// deletes are logged and skipped so one failure doesn't abort the sweep; only
// a failure to enumerate the store/disk aborts.
func (p *Pruner) Prune(ctx context.Context) ([]Ref, error) {
	if p.del == nil {
		return nil, errors.New("pruner: delete func is required")
	}
	onDisk, err := onDiskRefs(p.root)
	if err != nil {
		return nil, err
	}
	all, err := p.store.ListAll(ctx)
	if err != nil {
		return nil, err
	}

	var pruned []Ref
	for _, ref := range all {
		if onDisk[refKey(ref.APIVersion, ref.Kind, ref.Name)] {
			continue // file present → still desired
		}
		// Guard 1: composite children are owned by their parent.
		stored, err := p.store.Load(ctx, ref.APIVersion, ref.Kind, ref.Name)
		if err != nil {
			log.Printf("gitops prune: load %s/%s/%s: %v (skipping)", ref.APIVersion, ref.Kind, ref.Name, err)
			continue
		}
		if stored != nil && isLikelyCompositeChild(stored.Metadata.Labels) {
			log.Printf("gitops prune: skip %s/%s (composite child — deleted with its parent)", ref.Kind, ref.Name)
			continue
		}
		// Guard 2: protect explicitly hand-managed (cli/ui) resources. The
		// source is recorded on the applied_manifests row (K3), read here via
		// Ref.Source — no ops-table reconstruction. Empty source (unknown
		// provenance) is not protected.
		if ref.Source == SourceCLI || ref.Source == SourceUI {
			log.Printf("gitops prune: skip %s/%s (last applied via %s, not the repo)", ref.Kind, ref.Name, ref.Source)
			continue
		}
		if err := p.del(ctx, ref.APIVersion, ref.Kind, ref.Name); err != nil {
			log.Printf("gitops prune: delete %s/%s/%s: %v", ref.APIVersion, ref.Kind, ref.Name, err)
			continue
		}
		log.Printf("gitops prune: deleted %s/%s/%s (removed from repo)", ref.APIVersion, ref.Kind, ref.Name)
		pruned = append(pruned, ref)
	}
	return pruned, nil
}

func refKey(apiVersion, kind, name string) string { return apiVersion + "|" + kind + "|" + name }

// onDiskRefs walks the mirror dir and returns the set of resources present as
// manifest files, keyed by refKey. Non-manifest files and paths that don't
// match the mirror layout are ignored.
func onDiskRefs(root string) (map[string]bool, error) {
	out := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		if av, kind, name, ok := parsePathHint(root, path); ok {
			out[refKey(av, kind, name)] = true
		}
		return nil
	})
	return out, err
}

// Composite-child label markers. A stored resource carrying any of these is
// owned by a parent composite and must not be pruned independently.
const (
	labelOwnerKind  = "openctl.io/owner-kind" // Planner-produced children
	labelOwnerName  = "openctl.io/owner-name"
	labelK3sCluster = "k3s.openctl.io/cluster" // operative k3s Cluster.Apply children
)

// isLikelyCompositeChild reports whether stored metadata labels mark a resource
// as a composite child. Checks both the generic owner labels (future Planner
// path) and the operative k3s cluster label (today's live path) — see the
// prune safety analysis; missing either would risk orphan-deleting a cluster's
// VMs out from under it.
func isLikelyCompositeChild(labels map[string]string) bool {
	if labels == nil {
		return false
	}
	return labels[labelOwnerKind] != "" || labels[labelOwnerName] != "" || labels[labelK3sCluster] != ""
}
