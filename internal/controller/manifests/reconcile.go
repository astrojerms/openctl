package manifests

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ReconcileReport describes the difference between the SQLite applied
// manifests and the files on disk at controller startup. The dispatcher
// re-materializes any missing-on-disk row before resuming normal traffic,
// but never deletes a present-on-disk file the user might have committed.
type ReconcileReport struct {
	// MissingOnDisk is the set of SQLite rows whose .yaml file is absent.
	// These get re-materialized.
	MissingOnDisk []Ref
	// OrphansOnDisk is the set of .yaml files (relative to root) for which
	// no SQLite row exists. Logged but left alone — the user may have
	// hand-authored or committed them.
	OrphansOnDisk []string
}

// Reconcile compares the wrapped Store against the disk tree and returns a
// report. Missing-on-disk rows are re-materialized (idempotent: same content
// as the prior write would have produced). Orphan files are left alone.
//
// Run once at controller startup, before the dispatcher starts pulling ops.
func (m *DiskMirror) Reconcile(ctx context.Context) (ReconcileReport, error) {
	var report ReconcileReport

	refs, err := m.store.ListAll(ctx)
	if err != nil {
		return report, fmt.Errorf("list applied manifests: %w", err)
	}

	expected := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		dst := m.pathFor(ref.APIVersion, ref.Kind, ref.Name)
		rel, _ := filepath.Rel(m.root, dst)
		expected[rel] = struct{}{}

		if _, err := os.Stat(dst); err == nil {
			continue
		}
		report.MissingOnDisk = append(report.MissingOnDisk, ref)

		// Re-materialize from the stored spec.
		r, err := m.store.Load(ctx, ref.APIVersion, ref.Kind, ref.Name)
		if err != nil {
			return report, fmt.Errorf("load %s/%s for re-materialize: %w", ref.Kind, ref.Name, err)
		}
		if r == nil {
			continue
		}
		if err := m.writeFile(r); err != nil {
			return report, fmt.Errorf("re-materialize %s/%s: %w", ref.Kind, ref.Name, err)
		}
	}

	// Walk disk for orphan .yaml files. Best-effort: any walk error short-
	// circuits with the partial report we've built so far.
	if _, statErr := os.Stat(m.root); statErr == nil {
		_ = filepath.WalkDir(m.root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return walkErr
			}
			if !strings.HasSuffix(path, ".yaml") {
				return nil
			}
			rel, _ := filepath.Rel(m.root, path)
			if _, ok := expected[rel]; !ok {
				report.OrphansOnDisk = append(report.OrphansOnDisk, rel)
			}
			return nil
		})
	}

	return report, nil
}
