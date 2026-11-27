// Package recovery implements offline, local-only controller recovery. Archives
// contain credentials in plaintext and must be stored as sensitive material.
package recovery

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/openctl/openctl/internal/controller/instance"
	"github.com/openctl/openctl/internal/controller/storage"
)

const (
	archiveVersion         = 1
	maxEntries             = 100000
	maxFileBytes     int64 = 16 << 30
	maxTotalBytes    int64 = 32 << 30
	maxMetadataBytes int64 = 16 << 20
	metadataName           = "openctl-backup.json"
)

// Options identify filesystem locations, never live infrastructure targets.
type Options struct {
	Root     string
	StateDir string
	Archive  string
}

// Result records both actual paths and original paths for operator review.
type Result struct {
	Root             string
	StateDir         string
	OriginalRoot     string
	OriginalStateDir string
}

type entry struct {
	Path      string `json:"path"`
	Directory bool   `json:"directory,omitempty"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256,omitempty"`
	Mode      int64  `json:"mode"`
}

type metadata struct {
	Version       int     `json:"version"`
	Created       string  `json:"created"`
	Root          string  `json:"root"`
	StateDir      string  `json:"stateDir"`
	StateRelative string  `json:"stateRelative,omitempty"`
	Entries       []entry `json:"entries"`
}

func absolute(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	return filepath.Abs(filepath.Clean(p))
}

func within(root, name string) bool {
	rel, err := filepath.Rel(root, name)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// rejectSymlinks also checks ancestors: recovery must not redirect through an
// existing symlink. Missing suffixes are permitted only for restore targets.
func rejectSymlinks(name string, missingOK bool) error {
	for p := name; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			if !missingOK || !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		} else if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink path is not supported: %s", p)
		}
		if filepath.Dir(p) == p {
			return nil
		}
	}
}

func locations(opts Options) (string, string, error) {
	root, err := absolute(opts.Root)
	if err != nil {
		return "", "", err
	}
	state := opts.StateDir
	if state == "" {
		state = filepath.Join(root, "controller")
	}
	state, err = absolute(state)
	if err != nil {
		return "", "", err
	}
	if within(state, root) {
		return "", "", fmt.Errorf("state directory must not equal or contain the OpenCtl root")
	}
	return root, state, nil
}

// Backup holds the same kernel lock as serve, snapshots SQLite from private
// copies (including any uncheckpointed WAL), and never opens the source SQLite
// database. Apart from the persistent lockfile it does not change source files.
func Backup(ctx context.Context, opts Options) (_ Result, retErr error) {
	root, state, err := locations(opts)
	if err != nil {
		return Result{}, err
	}
	for _, p := range []string{root, state} {
		if err := rejectSymlinks(p, false); err != nil {
			return Result{}, err
		}
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() {
			return Result{}, fmt.Errorf("backup source must be a directory: %s", p)
		}
	}
	output, err := absolute(opts.Archive)
	if err != nil {
		return Result{}, err
	}
	if within(root, output) || within(state, output) {
		return Result{}, fmt.Errorf("backup output must be outside both source roots")
	}
	if err := rejectSymlinks(filepath.Dir(output), false); err != nil {
		return Result{}, err
	}
	lock, err := instance.Acquire(state)
	if err != nil {
		return Result{}, err
	}
	defer lock.Close()
	work, err := os.MkdirTemp("", "openctl-backup-")
	if err != nil {
		return Result{}, err
	}
	defer func() { retErr = errors.Join(retErr, os.RemoveAll(work)) }()
	if within(root, work) || within(state, work) {
		return Result{}, fmt.Errorf("temporary directory must be outside source roots")
	}
	m := metadata{Version: archiveVersion, Created: time.Now().UTC().Format(time.RFC3339), Root: root, StateDir: state}
	if within(root, state) {
		m.StateRelative, _ = filepath.Rel(root, state)
		m.StateRelative = filepath.ToSlash(m.StateRelative)
	}
	var total int64
	for _, source := range []struct{ prefix, dir string }{{"root", root}, {"state", state}} {
		err = filepath.WalkDir(source.dir, func(name string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if source.prefix == "root" && name == state {
				return filepath.SkipDir
			}
			rel, err := filepath.Rel(source.dir, name)
			if err != nil {
				return err
			}
			if source.prefix == "state" && (rel == instance.Filename || rel == "state.db-wal" || rel == "state.db-shm") {
				return nil
			}
			archivePath := source.prefix
			if rel != "." {
				archivePath += "/" + filepath.ToSlash(rel)
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if !info.IsDir() && !info.Mode().IsRegular() {
				return fmt.Errorf("unsupported source file type: %s", name)
			}
			e := entry{Path: archivePath, Directory: info.IsDir(), Mode: int64(info.Mode().Perm() & 0o700)}
			if e.Directory {
				e.Mode = 0o700
			} else {
				e.Mode = int64(0o600 | (info.Mode().Perm() & 0o100))
				dest := filepath.Join(work, filepath.FromSlash(archivePath))
				if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
					return err
				}
				h := sha256.New()
				if source.prefix == "state" && rel == "state.db" {
					if err := snapshot(ctx, state, dest, work); err != nil {
						return err
					}
					f, err := os.Open(dest)
					if err != nil {
						return err
					}
					e.Size, err = io.Copy(h, io.LimitReader(f, maxFileBytes+1))
					if err := errors.Join(err, f.Close()); err != nil {
						return err
					}
				} else {
					e.Size, err = copyRegular(name, dest, maxFileBytes, h)
					if err != nil {
						return err
					}
				}
				e.SHA256 = hex.EncodeToString(h.Sum(nil))
				total += e.Size
				if e.Size > maxFileBytes || total > maxTotalBytes {
					return fmt.Errorf("backup exceeds size limits")
				}
			}
			m.Entries = append(m.Entries, e)
			if len(m.Entries) > maxEntries {
				return fmt.Errorf("backup exceeds entry limit")
			}
			return nil
		})
		if err != nil {
			return Result{}, err
		}
	}
	if err := storage.ValidateSnapshot(ctx, filepath.Join(work, "state", "state.db")); err != nil {
		return Result{}, err
	}
	if err := validateMetadata(m); err != nil {
		return Result{}, err
	}
	body, err := json.Marshal(m)
	if err != nil {
		return Result{}, err
	}
	if int64(len(body)) > maxMetadataBytes {
		return Result{}, fmt.Errorf("backup metadata exceeds limit")
	}
	out, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		_ = out.Close()
		if retErr != nil {
			_ = os.Remove(output)
		}
	}()
	tw := tar.NewWriter(out)
	if err := tw.WriteHeader(&tar.Header{Name: metadataName, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		return Result{}, err
	}
	if _, err := tw.Write(body); err != nil {
		return Result{}, err
	}
	for _, e := range m.Entries {
		h := &tar.Header{Name: e.Path, Mode: e.Mode, Size: e.Size, Typeflag: tar.TypeReg}
		if e.Directory {
			h.Typeflag = tar.TypeDir
		}
		if err := tw.WriteHeader(h); err != nil {
			return Result{}, err
		}
		if !e.Directory {
			f, err := os.Open(filepath.Join(work, filepath.FromSlash(e.Path)))
			if err != nil {
				return Result{}, err
			}
			_, copyErr := io.CopyN(tw, f, e.Size)
			closeErr := f.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return Result{}, err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return Result{}, err
	}
	if err := out.Sync(); err != nil {
		return Result{}, err
	}
	if err := out.Close(); err != nil {
		return Result{}, err
	}
	if err := syncDir(filepath.Dir(output)); err != nil {
		return Result{}, err
	}
	return Result{Root: root, StateDir: state, OriginalRoot: root, OriginalStateDir: state}, nil
}

func copyRegular(source, dest string, limit int64, digest io.Writer) (int64, error) {
	fd, err := unix.Open(source, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return 0, err
	}
	in := os.NewFile(uintptr(fd), source)
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return 0, fmt.Errorf("unsupported or oversized source file: %s", source)
	}
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	var target io.Writer = out
	if digest != nil {
		target = io.MultiWriter(out, digest)
	}
	n, copyErr := io.Copy(target, io.LimitReader(in, limit+1))
	closeErr := out.Close()
	if n > limit {
		return n, fmt.Errorf("source exceeds size limit")
	}
	return n, errors.Join(copyErr, closeErr)
}

func snapshot(ctx context.Context, state, dest, work string) (retErr error) {
	dir, err := os.MkdirTemp(work, "sqlite-")
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, os.RemoveAll(dir)) }()
	for _, name := range []string{"state.db", "state.db-wal", "state.db-shm"} {
		_, err := copyRegular(filepath.Join(state, name), filepath.Join(dir, name), maxFileBytes, nil)
		if err != nil && (name == "state.db" || !errors.Is(err, fs.ErrNotExist)) {
			return err
		}
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(dir, "state.db"))}
	db, err := sql.Open("sqlite", u.String()+"?mode=ro")
	if err != nil {
		return err
	}
	_, vacuumErr := db.ExecContext(ctx, "VACUUM INTO ?", dest)
	return errors.Join(vacuumErr, db.Close())
}

func syncDir(name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func validateMetadata(m metadata) error {
	if m.Version != archiveVersion || !filepath.IsAbs(m.Root) || !filepath.IsAbs(m.StateDir) || len(m.Entries) == 0 || len(m.Entries) > maxEntries {
		return fmt.Errorf("unsupported or invalid backup metadata")
	}
	if m.StateRelative != "" && (!safeRelative(m.StateRelative) || m.StateRelative == ".") {
		return fmt.Errorf("invalid archived state location")
	}
	seen := make(map[string]bool, len(m.Entries))
	var total int64
	for _, e := range m.Entries {
		if !safeRelative(e.Path) || (e.Path != "root" && e.Path != "state" && !strings.HasPrefix(e.Path, "root/") && !strings.HasPrefix(e.Path, "state/")) || seen[e.Path] {
			return fmt.Errorf("invalid or duplicate archive path: %q", e.Path)
		}
		seen[e.Path] = true
		if ((e.Path == "root" || e.Path == "state") && !e.Directory) || (e.Path == "state/state.db" && e.Directory) {
			return fmt.Errorf("invalid archive root or database type")
		}
		if e.Path == "state/"+instance.Filename || e.Path == "state/state.db-wal" || e.Path == "state/state.db-shm" {
			return fmt.Errorf("archive includes transient controller files")
		}
		if e.Size < 0 || e.Size > maxFileBytes || (e.Directory && (e.Size != 0 || e.SHA256 != "" || e.Mode != 0o700)) || (!e.Directory && e.Mode != 0o600 && e.Mode != 0o700) {
			return fmt.Errorf("invalid archive entry size or permissions")
		}
		if !e.Directory {
			b, err := hex.DecodeString(e.SHA256)
			if err != nil || len(b) != sha256.Size {
				return fmt.Errorf("invalid archive checksum")
			}
		}
		total += e.Size
		if total > maxTotalBytes {
			return fmt.Errorf("archive exceeds total size limit")
		}
	}
	if !seen["root"] || !seen["state"] || !seen["state/state.db"] {
		return fmt.Errorf("archive missing required roots or database")
	}
	return nil
}

func safeRelative(name string) bool {
	return name != "" && name != "." && !strings.ContainsAny(name, "\\\x00") && !strings.HasPrefix(name, "/") && path.Clean(name) == name && name != ".." && !strings.HasPrefix(name, "../") && !strings.Contains(name, ":")
}

func newDestination(name string) error {
	if err := rejectSymlinks(name, true); err != nil {
		return err
	}
	if _, err := os.Lstat(name); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("restore destination must not exist: %s", name)
	}
	info, err := os.Stat(filepath.Dir(name))
	if err != nil || !info.IsDir() {
		return fmt.Errorf("restore destination parent must be an existing directory: %s", filepath.Dir(name))
	}
	return nil
}

// Restore validates and stages the entire archive before exclusive publication.
// It neither edits archived configuration nor starts controller automation.
func Restore(ctx context.Context, opts Options) (_ Result, retErr error) {
	input, err := absolute(opts.Archive)
	if err != nil {
		return Result{}, err
	}
	if err := rejectSymlinks(input, false); err != nil {
		return Result{}, err
	}
	in, err := os.Open(input)
	if err != nil {
		return Result{}, err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxTotalBytes+maxMetadataBytes+(maxEntries+4)*1024 {
		return Result{}, fmt.Errorf("input must be a bounded regular archive")
	}
	bounded := &io.LimitedReader{R: in, N: maxTotalBytes + maxMetadataBytes + (maxEntries+4)*1024}
	tr := tar.NewReader(bounded)
	h, err := tr.Next()
	if err != nil {
		return Result{}, err
	}
	if h.Name != metadataName || h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > maxMetadataBytes {
		return Result{}, fmt.Errorf("archive must start with bounded versioned metadata")
	}
	body, err := io.ReadAll(tr)
	if err != nil {
		return Result{}, err
	}
	var m metadata
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Result{}, err
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Result{}, fmt.Errorf("trailing backup metadata")
	}
	if err := validateMetadata(m); err != nil {
		return Result{}, err
	}
	if opts.StateDir == "" && m.StateRelative != "" {
		opts.StateDir = filepath.Join(opts.Root, filepath.FromSlash(m.StateRelative))
	}
	root, state, err := locations(opts)
	if err != nil {
		return Result{}, err
	}
	if err := newDestination(root); err != nil {
		return Result{}, err
	}
	inside := within(root, state)
	if !inside {
		if err := newDestination(state); err != nil {
			return Result{}, err
		}
	}
	stageRoot, err := os.MkdirTemp(filepath.Dir(root), ".openctl-restore-")
	if err != nil {
		return Result{}, err
	}
	defer func() { retErr = errors.Join(retErr, os.RemoveAll(stageRoot)) }()
	stageState := ""
	if inside {
		rel, _ := filepath.Rel(root, state)
		stageState = filepath.Join(stageRoot, rel)
		if err := os.MkdirAll(stageState, 0o700); err != nil {
			return Result{}, err
		}
	} else {
		stageState, err = os.MkdirTemp(filepath.Dir(state), ".openctl-restore-")
		if err != nil {
			return Result{}, err
		}
		defer func() { retErr = errors.Join(retErr, os.RemoveAll(stageState)) }()
	}
	for _, e := range m.Entries {
		h, err := tr.Next()
		if err != nil {
			return Result{}, fmt.Errorf("missing archive entry: %w", err)
		}
		wantType := byte(tar.TypeReg)
		if e.Directory {
			wantType = tar.TypeDir
		}
		if h.Name != e.Path || h.Typeflag != wantType || h.Size != e.Size || h.Mode != e.Mode || h.Linkname != "" {
			return Result{}, fmt.Errorf("archive entry does not match metadata: %q", h.Name)
		}
		for key, value := range h.PAXRecords {
			if key != "path" || value != e.Path {
				return Result{}, fmt.Errorf("unsupported archive extension: %s", key)
			}
		}
		base, rel := stageRoot, strings.TrimPrefix(e.Path, "root")
		if e.Path == "state" || strings.HasPrefix(e.Path, "state/") {
			base, rel = stageState, strings.TrimPrefix(e.Path, "state")
		}
		dest := filepath.Join(base, filepath.FromSlash(strings.TrimPrefix(rel, "/")))
		if base == stageRoot && inside && within(stageState, dest) {
			return Result{}, fmt.Errorf("archived root content conflicts with requested state destination")
		}
		if e.Directory {
			if err := os.MkdirAll(dest, 0o700); err != nil {
				return Result{}, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return Result{}, err
		}
		f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(e.Mode))
		if err != nil {
			return Result{}, err
		}
		hash := sha256.New()
		_, copyErr := io.CopyN(io.MultiWriter(f, hash), tr, e.Size)
		syncErr := f.Sync()
		closeErr := f.Close()
		if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
			return Result{}, err
		}
		if hex.EncodeToString(hash.Sum(nil)) != e.SHA256 {
			return Result{}, fmt.Errorf("archive checksum mismatch: %s", e.Path)
		}
	}
	beforeTrailer := bounded.N
	if _, err := tr.Next(); !errors.Is(err, io.EOF) {
		if err != nil {
			return Result{}, fmt.Errorf("unexpected trailing archive entry or corruption: %w", err)
		}
		return Result{}, errors.New("unexpected trailing archive entry")
	}
	lastSize := m.Entries[len(m.Entries)-1].Size
	if beforeTrailer-bounded.N != (512-lastSize%512)%512+1024 {
		return Result{}, fmt.Errorf("archive is missing its complete tar trailer")
	}
	// Reject concatenated archives or nonzero data hidden after the tar trailer.
	padding, err := io.ReadAll(io.LimitReader(in, 10241))
	if err != nil || len(padding) > 10240 || len(bytes.Trim(padding, "\x00")) != 0 {
		return Result{}, fmt.Errorf("unexpected trailing archive data")
	}
	if err := storage.ValidateSnapshot(ctx, filepath.Join(stageState, "state.db")); err != nil {
		return Result{}, err
	}
	// Keep a new daemon out of the staged/published state during both renames
	// and any rollback. The lock follows the state directory's inode.
	lock, err := instance.Acquire(stageState)
	if err != nil {
		return Result{}, err
	}
	defer lock.Close()
	bases := []string{stageRoot}
	if !inside {
		bases = append(bases, stageState)
	}
	for _, base := range bases {
		var dirs []string
		if err := filepath.WalkDir(base, func(name string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				dirs = append(dirs, name)
			}
			return err
		}); err != nil {
			return Result{}, err
		}
		sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
		for _, dir := range dirs {
			if err := syncDir(dir); err != nil {
				return Result{}, err
			}
		}
	}
	if !inside {
		if err := publish(stageState, state); err != nil {
			return Result{}, fmt.Errorf("publish state: %w", err)
		}
	}
	if err := publish(stageRoot, root); err != nil {
		if !inside {
			if rollbackErr := publish(state, stageState); rollbackErr != nil {
				return Result{}, fmt.Errorf("root publication failed: %w; PARTIAL RESTORE: state remains at %s; rollback failed: %w; do not start controller", err, state, rollbackErr)
			}
			_ = syncDir(filepath.Dir(state))
		}
		return Result{}, fmt.Errorf("publish root (state publication rolled back if separate): %w", err)
	}
	if err := errors.Join(syncDir(filepath.Dir(root)), syncDir(filepath.Dir(state))); err != nil {
		return Result{}, fmt.Errorf("restore published at root=%s state=%s but directory durability sync failed: %w; do not retry over these destinations", root, state, err)
	}
	return Result{Root: root, StateDir: state, OriginalRoot: m.Root, OriginalStateDir: m.StateDir}, nil
}
