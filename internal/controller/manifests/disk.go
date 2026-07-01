package manifests

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/openctl/openctl/pkg/protocol"
)

// DiskMirror wraps a SQLite Store with a write-through file mirror on disk.
// Every Save additionally materializes the manifest as YAML at
// <root>/<apiVersion>/<kind>/<name>.yaml; every Delete removes that file.
// LoadHash and Hash pass straight through to the wrapped Store.
//
// The wrapped Store remains canonical — disk is a materialized projection
// for humans, git, and `ls`. If a disk write fails after the SQLite write
// succeeded, the SQLite truth is preserved and the disk error is returned
// to the caller (the dispatcher logs and continues).
//
// Implements operations.ManifestSink.
type DiskMirror struct {
	store *Store
	root  string
	// hook runs after each successful Save/Delete with the resource and
	// the "what changed" verb ("apply" or "delete"). Used by U2.2 to plug
	// in git-commit side-effects without DiskMirror having to import the
	// git package. The originator ("cli"/"ui") is read from ctx via
	// SourceFromContext — the dispatcher injects it before calling Save
	// or Delete. nil = no hook.
	hook func(ctx context.Context, kind string, r *protocol.Resource, verb string) error
}

// NewDiskMirror wraps store with a disk mirror rooted at root. The root
// directory is created on first write — callers do not need to mkdir.
func NewDiskMirror(store *Store, root string) *DiskMirror {
	return &DiskMirror{store: store, root: root}
}

// SetHook installs a post-write callback. Replaces any previously-set hook.
// The hook fires after the SQLite write and the disk write both succeed.
// A non-nil error from the hook is returned to the caller; the SQLite +
// disk state is left as-is (the hook is responsible for handling its own
// partial-failure cleanup).
func (m *DiskMirror) SetHook(hook func(ctx context.Context, kind string, r *protocol.Resource, verb string) error) {
	m.hook = hook
}

// Root returns the directory the mirror writes into.
func (m *DiskMirror) Root() string { return m.root }

// Save writes the manifest to SQLite and then to disk. On disk-write
// failure the SQLite row is left in place (truth wins).
func (m *DiskMirror) Save(ctx context.Context, r *protocol.Resource) error {
	return m.SaveWithRefsHash(ctx, r, "")
}

// SaveWithRefsHash is Save plus the resolved-refs hash used by the
// dispatcher's verifying-trace cache. See Store.SaveWithRefsHash.
func (m *DiskMirror) SaveWithRefsHash(ctx context.Context, r *protocol.Resource, refsHash string) error {
	if err := m.store.SaveWithRefsHash(ctx, r, refsHash); err != nil {
		return err
	}
	if err := m.writeFile(r); err != nil {
		return fmt.Errorf("materialize %s/%s to disk: %w", r.Kind, r.Metadata.Name, err)
	}
	if m.hook != nil {
		if err := m.hook(ctx, r.Kind, r, "apply"); err != nil {
			return fmt.Errorf("post-write hook: %w", err)
		}
	}
	return nil
}

// Delete removes the row from SQLite and then removes the disk file.
// Idempotent: a missing file is not an error.
func (m *DiskMirror) Delete(ctx context.Context, apiVersion, kind, name string) error {
	if err := m.store.Delete(ctx, apiVersion, kind, name); err != nil {
		return err
	}
	if err := m.removeFile(apiVersion, kind, name); err != nil {
		return fmt.Errorf("remove %s/%s from disk: %w", kind, name, err)
	}
	if m.hook != nil {
		stub := &protocol.Resource{
			APIVersion: apiVersion,
			Kind:       kind,
			Metadata:   protocol.ResourceMetadata{Name: name},
		}
		if err := m.hook(ctx, kind, stub, "delete"); err != nil {
			return fmt.Errorf("post-delete hook: %w", err)
		}
	}
	return nil
}

// LoadHash delegates to the wrapped store.
func (m *DiskMirror) LoadHash(ctx context.Context, apiVersion, kind, name string) (string, error) {
	return m.store.LoadHash(ctx, apiVersion, kind, name)
}

// LoadHashes delegates to the wrapped store.
func (m *DiskMirror) LoadHashes(ctx context.Context, apiVersion, kind, name string) (string, string, error) {
	return m.store.LoadHashes(ctx, apiVersion, kind, name)
}

// Hash delegates to the wrapped store.
func (m *DiskMirror) Hash(r *protocol.Resource) string { return m.store.Hash(r) }

// pathFor returns the absolute file path for a (apiVersion, kind, name).
// apiVersion's "/" becomes a directory separator naturally — e.g.
// proxmox.openctl.io/v1 → proxmox.openctl.io/v1 — which produces a clean
// tree like proxmox.openctl.io/v1/VirtualMachine/foo.yaml when committed.
//
// safeSegment scrubs each path component of separators and "..", defending
// against pathological resource names (a "../etc/passwd" name would
// otherwise escape the root).
func (m *DiskMirror) pathFor(apiVersion, kind, name string) string {
	parts := []string{m.root}
	for seg := range strings.SplitSeq(apiVersion, "/") {
		parts = append(parts, safeSegment(seg))
	}
	parts = append(parts, safeSegment(kind), safeSegment(name)+".yaml")
	return filepath.Join(parts...)
}

// safeSegment scrubs a path component so it can't break out of the root
// directory or collide with filesystem special names. Replaces all
// path-traversal-shaped tokens; empties become "_" so the join doesn't
// produce a double-slash.
func safeSegment(s string) string {
	if s == "" || s == "." || s == ".." {
		return "_"
	}
	out := strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', 0:
			return '_'
		}
		return r
	}, s)
	return out
}

func (m *DiskMirror) writeFile(r *protocol.Resource) error {
	dst := m.pathFor(r.APIVersion, r.Kind, r.Metadata.Name)
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	out, err := yaml.Marshal(r)
	if err != nil {
		return fmt.Errorf("encode yaml: %w", err)
	}
	// Atomic write: tmp file in the same directory, then rename. Same-dir
	// keeps the rename a same-filesystem move (otherwise os.Rename can
	// fall back to copy+delete and lose atomicity). Both tmpPath and dst
	// derive from m.root + safeSegment-scrubbed inputs, so the os.Remove
	// / os.Rename calls below can't path-traverse out of the root.
	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".tmp.*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath) // #nosec G703 -- tmpPath from os.CreateTemp inside scrubbed dir
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath) // #nosec G703 -- see above
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath) // #nosec G703 -- see above
		return err
	}
	if err := os.Rename(tmpPath, dst); err != nil { // #nosec G703 -- both paths scrubbed and rooted
		_ = os.Remove(tmpPath) // #nosec G703 -- see above
		return err
	}
	return nil
}

func (m *DiskMirror) removeFile(apiVersion, kind, name string) error {
	dst := m.pathFor(apiVersion, kind, name)
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Clean up newly-empty parent dirs walking up to (but not including)
	// the root. Best-effort; ENOTEMPTY just means another resource shares
	// the directory.
	dir := filepath.Dir(dst)
	for dir != m.root && len(dir) > len(m.root) {
		if err := os.Remove(dir); err != nil {
			break
		}
		dir = filepath.Dir(dir)
	}
	return nil
}
