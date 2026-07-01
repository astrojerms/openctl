package manifests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"

	"github.com/openctl/openctl/pkg/protocol"
)

// GitOpsApplyFunc submits an Apply of a manifest read from disk. Same
// signature the reconciler's AutoApplyFunc uses; wire from main.go to
// the ops store + dispatcher.
type GitOpsApplyFunc func(ctx context.Context, r *protocol.Resource) error

// GitOpsDeleteFunc submits a Delete when a manifest file is removed
// from the mirror directory. Optional — pass nil to keep file
// deletions as no-ops (a common preference: users may want to move
// files without triggering resource deletion).
type GitOpsDeleteFunc func(ctx context.Context, apiVersion, kind, name string) error

// Watcher observes the manifest mirror directory and applies file
// changes back through the controller. Symmetric to DiskMirror: the
// mirror writes the file after every Apply; the Watcher reads the
// file after any change and calls Apply if it doesn't match what's
// already stored. Loop-safe because the Apply is a no-op when the
// spec matches the last-applied manifest.
type Watcher struct {
	root      string
	store     *Store
	apply     GitOpsApplyFunc
	delete    GitOpsDeleteFunc
	debounce  time.Duration
	watcher   *fsnotify.Watcher
	done      chan struct{}
	pending   map[string]*time.Timer
	pendingMu sync.Mutex
}

// NewWatcher constructs a Watcher rooted at the mirror dir. apply is
// required; delete is optional (nil = ignore file removals). debounce
// coalesces rapid successive writes to the same file (editors often
// truncate+write, producing multiple events).
func NewWatcher(root string, store *Store, apply GitOpsApplyFunc, del GitOpsDeleteFunc) *Watcher {
	return &Watcher{
		root:     root,
		store:    store,
		apply:    apply,
		delete:   del,
		debounce: 500 * time.Millisecond,
		done:     make(chan struct{}),
		pending:  map[string]*time.Timer{},
	}
}

// Start begins watching. Returns an error if the initial fsnotify
// setup fails; runtime errors are logged and don't stop the watcher.
// The watcher runs until Stop or ctx cancellation.
func (w *Watcher) Start(ctx context.Context) error {
	if w.apply == nil {
		return errors.New("gitops watcher: apply func is required")
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("fsnotify: %w", err)
	}
	w.watcher = fw
	// Recursive add: fsnotify itself is non-recursive, so we walk once
	// and add every subdir. New subdirs get picked up on-the-fly by
	// the event loop (Create on a dir triggers a fresh Add).
	if err := filepath.WalkDir(w.root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Missing subtree is fine — we might be watching a fresh dir.
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() {
			return fw.Add(path)
		}
		return nil
	}); err != nil {
		_ = fw.Close()
		return fmt.Errorf("walk %s: %w", w.root, err)
	}
	go w.loop(ctx)
	return nil
}

// Stop shuts the watcher down. Safe to call multiple times.
func (w *Watcher) Stop() {
	if w.watcher != nil {
		_ = w.watcher.Close()
	}
	select {
	case <-w.done:
	default:
	}
}

func (w *Watcher) loop(ctx context.Context) {
	defer close(w.done)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.handle(ctx, ev)
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("gitops: watcher error: %v", err)
		}
	}
}

func (w *Watcher) handle(ctx context.Context, ev fsnotify.Event) {
	// New directories: add them to the watch so nested files are
	// picked up. Ignore other Create events; the Write that follows
	// will trigger the apply.
	if ev.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			_ = w.watcher.Add(ev.Name)
			return
		}
	}
	// Only care about .yaml files under the manifest tree.
	if !strings.HasSuffix(ev.Name, ".yaml") {
		return
	}
	// Debounce: coalesce rapid writes to the same file (editor
	// truncate+write, atomic rename, etc.) into a single apply
	// after 500ms of quiet.
	w.pendingMu.Lock()
	if t := w.pending[ev.Name]; t != nil {
		t.Stop()
	}
	w.pending[ev.Name] = time.AfterFunc(w.debounce, func() {
		w.pendingMu.Lock()
		delete(w.pending, ev.Name)
		w.pendingMu.Unlock()
		w.process(ctx, ev)
	})
	w.pendingMu.Unlock()
}

func (w *Watcher) process(ctx context.Context, ev fsnotify.Event) {
	// Removed files: submit Delete when configured.
	if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		if w.delete == nil {
			return
		}
		av, kind, name, ok := parsePathHint(w.root, ev.Name)
		if !ok {
			return
		}
		if err := w.delete(ctx, av, kind, name); err != nil {
			log.Printf("gitops: delete %s/%s/%s from file: %v", av, kind, name, err)
			return
		}
		log.Printf("gitops: deleted %s/%s/%s (file removed)", av, kind, name)
		return
	}
	// Write/Create: read + parse + apply if content differs.
	data, err := os.ReadFile(ev.Name) // #nosec G304 -- watching the controller-owned mirror dir
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("gitops: read %s: %v", ev.Name, err)
		}
		return
	}
	var r protocol.Resource
	if err := yaml.Unmarshal(data, &r); err != nil {
		log.Printf("gitops: parse %s: %v (skipping)", ev.Name, err)
		return
	}
	if r.APIVersion == "" || r.Kind == "" || r.Metadata.Name == "" {
		log.Printf("gitops: %s missing apiVersion/kind/name (skipping)", ev.Name)
		return
	}
	if same, err := w.matchesStored(ctx, &r); err != nil {
		log.Printf("gitops: compare %s: %v", ev.Name, err)
	} else if same {
		// No change vs stored manifest — nothing to do. This is the
		// common case for the loop: our own DiskMirror write triggered
		// the fsnotify event, and applied_manifests already reflects
		// this content. Silently skip.
		return
	}
	if err := w.apply(ctx, &r); err != nil {
		log.Printf("gitops: apply %s: %v", ev.Name, err)
		return
	}
	log.Printf("gitops: applied %s/%s/%s (file changed)", r.APIVersion, r.Kind, r.Metadata.Name)
}

// matchesStored returns true when the spec of the incoming manifest
// matches what's on file in applied_manifests. Compared via JSON so
// map key ordering doesn't produce false diffs.
func (w *Watcher) matchesStored(ctx context.Context, r *protocol.Resource) (bool, error) {
	stored, err := w.store.Load(ctx, r.APIVersion, r.Kind, r.Metadata.Name)
	if err != nil {
		return false, err
	}
	if stored == nil {
		return false, nil
	}
	a, err := json.Marshal(stored.Spec)
	if err != nil {
		return false, err
	}
	b, err := json.Marshal(r.Spec)
	if err != nil {
		return false, err
	}
	return string(a) == string(b), nil
}

// parsePathHint extracts (apiVersion, kind, name) from a mirror-shaped
// path: root/<apiVersion-domain>/<version>/<Kind>/<name>.yaml. The
// disk mirror splits apiVersion's "/" into two directories, so the
// relative path is 4 segments: e.g.
//   proxmox.openctl.io/v1/VirtualMachine/foo.yaml
// Falls back to false when the layout doesn't match — user-added
// scratch files or non-manifest content just get ignored.
func parsePathHint(root, path string) (apiVersion, kind, name string, ok bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", "", "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 4 || !strings.HasSuffix(parts[3], ".yaml") {
		return "", "", "", false
	}
	return parts[0] + "/" + parts[1], parts[2], strings.TrimSuffix(parts[3], ".yaml"), true
}
