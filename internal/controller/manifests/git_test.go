package manifests

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDiskMirrorRelPathFor(t *testing.T) {
	m := &DiskMirror{}
	got := m.RelPathFor("proxmox.openctl.io/v1", "VirtualMachine", "foo")
	if want := "proxmox.openctl.io/v1/VirtualMachine/foo.yaml"; got != want {
		t.Errorf("RelPathFor = %q, want %q", got, want)
	}
	// A "/" in the name is scrubbed so it can't introduce a new path segment
	// (which would let a resource name escape its kind directory).
	if got := m.RelPathFor("k3s.openctl.io/v1", "Cluster", "a/b"); got != "k3s.openctl.io/v1/Cluster/a_b.yaml" {
		t.Errorf("RelPathFor did not scrub name separator: %q", got)
	}
}

func TestRepoLogForPathAndShowAtCommit(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	rel := "proxmox.openctl.io/v1/VirtualMachine/foo.yaml"
	abs := filepath.Join(r.Dir(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		t.Fatal(err)
	}

	writeCommit := func(body, subject string) {
		if err := os.WriteFile(abs, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := r.CommitAll(ctx, subject); err != nil {
			t.Fatalf("CommitAll %q: %v", subject, err)
		}
	}
	writeCommit("kind: VM\nv: 1\n", "apply VM/foo v1")
	writeCommit("kind: VM\nv: 2\n", "apply VM/foo v2")

	commits, err := r.LogForPath(ctx, rel)
	if err != nil {
		t.Fatalf("LogForPath: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("want 2 commits, got %d", len(commits))
	}
	if commits[0].Subject != "apply VM/foo v2" || commits[1].Subject != "apply VM/foo v1" {
		t.Errorf("commits not newest-first: %q, %q", commits[0].Subject, commits[1].Subject)
	}
	if commits[0].SHA == "" || commits[0].CommittedAt == "" || commits[0].Author == "" {
		t.Errorf("commit fields incomplete: %+v", commits[0])
	}

	newest, err := r.ShowAtCommit(ctx, commits[0].SHA, rel)
	if err != nil || !strings.Contains(string(newest), "v: 2") {
		t.Errorf("ShowAtCommit newest: %q err=%v", newest, err)
	}
	oldest, err := r.ShowAtCommit(ctx, commits[1].SHA, rel)
	if err != nil || !strings.Contains(string(oldest), "v: 1") {
		t.Errorf("ShowAtCommit oldest: %q err=%v", oldest, err)
	}

	// A path absent at the first commit → ErrPathNotInCommit.
	if err := os.WriteFile(filepath.Join(r.Dir(), "other.yaml"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.CommitAll(ctx, "add other"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ShowAtCommit(ctx, commits[1].SHA, "other.yaml"); !errors.Is(err, ErrPathNotInCommit) {
		t.Errorf("want ErrPathNotInCommit for absent path, got %v", err)
	}
}

func TestRepoLogForPathEmptyCases(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()

	// No commits yet → empty, no error.
	commits, err := r.LogForPath(ctx, "nope.yaml")
	if err != nil || len(commits) != 0 {
		t.Errorf("empty repo: commits=%d err=%v", len(commits), err)
	}
	// A commit exists, but the queried path was never tracked → empty.
	if err := os.WriteFile(filepath.Join(r.Dir(), "a.yaml"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.CommitAll(ctx, "add a"); err != nil {
		t.Fatal(err)
	}
	commits, err = r.LogForPath(ctx, "b.yaml")
	if err != nil || len(commits) != 0 {
		t.Errorf("untracked path: commits=%d err=%v", len(commits), err)
	}
}

// requireGit skips the test if a git binary isn't on PATH. Lets the test
// suite remain runnable in minimal environments while still exercising
// the real git wrapper everywhere it can.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not on PATH; skipping git-dependent test")
	}
}

func newTestRepo(t *testing.T) *Repo {
	t.Helper()
	requireGit(t)
	r, err := NewRepo(RepoOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewRepo: %v", err)
	}
	if err := r.EnsureInit(context.Background()); err != nil {
		t.Fatalf("EnsureInit: %v", err)
	}
	return r
}

func TestRepoEnsureInitIsIdempotent(t *testing.T) {
	r := newTestRepo(t)
	if err := r.EnsureInit(context.Background()); err != nil {
		t.Errorf("second EnsureInit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), ".git")); err != nil {
		t.Errorf(".git missing: %v", err)
	}
}

func TestRepoCommitAllRecordsCommit(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()

	// Drop a file to commit.
	if err := os.WriteFile(filepath.Join(r.Dir(), "vm.yaml"), []byte("kind: VM\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.CommitAll(ctx, "apply VM/foo via CLI"); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	// Inspect git log to confirm message landed.
	out, err := exec.CommandContext(ctx, "git", "-C", r.Dir(), "log", "-1", "--pretty=%s").Output() // #nosec G204 -- path from t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "apply VM/foo via CLI" {
		t.Errorf("commit subject = %q, want %q", got, "apply VM/foo via CLI")
	}
}

func TestRepoCommitAllReturnsNothingToCommitOnCleanTree(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()

	if err := r.CommitAll(ctx, "noop"); err == nil || err.Error() != ErrNothingToCommit.Error() {
		t.Errorf("CommitAll on clean tree: err = %v, want ErrNothingToCommit", err)
	}
}

func TestRepoStatusReportsDirtyAndClean(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()

	// Initial commit so HEAD exists.
	_ = os.WriteFile(filepath.Join(r.Dir(), "seed.yaml"), []byte("x\n"), 0o600)
	if err := r.CommitAll(ctx, "seed"); err != nil {
		t.Fatal(err)
	}

	st, err := r.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Clean || len(st.DirtyPaths) != 0 {
		t.Errorf("after commit: clean=%v dirty=%v, want clean=true dirty=[]", st.Clean, st.DirtyPaths)
	}
	if st.HeadSHA == "" {
		t.Error("HeadSHA empty after commit")
	}
	if st.Branch != "main" {
		t.Errorf("Branch = %q, want main", st.Branch)
	}

	// Dirty the tree.
	if err := os.WriteFile(filepath.Join(r.Dir(), "untracked.yaml"), []byte("y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ = r.Status(ctx)
	if st.Clean {
		t.Error("Clean=true after writing untracked file")
	}
	if len(st.DirtyPaths) == 0 {
		t.Error("DirtyPaths empty after writing untracked file")
	}
}

func TestRepoStatusAheadBehindMinusOneWithoutRemote(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	_ = os.WriteFile(filepath.Join(r.Dir(), "seed.yaml"), []byte("x\n"), 0o600)
	_ = r.CommitAll(ctx, "seed")
	st, _ := r.Status(ctx)
	if st.Ahead != -1 || st.Behind != -1 {
		t.Errorf("no-remote ahead/behind = %d/%d, want -1/-1", st.Ahead, st.Behind)
	}
}

func TestRepoPushNoopWithoutRemote(t *testing.T) {
	r := newTestRepo(t)
	if err := r.Push(context.Background()); err != nil {
		t.Errorf("Push without remote should be a no-op; got %v", err)
	}
}

func TestRepoPushModeDefaultsByRemote(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	r, err := NewRepo(RepoOptions{Dir: dir, Remote: "https://example.invalid/repo.git"})
	if err != nil {
		t.Fatal(err)
	}
	if r.PushMode() != PushModeOnCommit {
		t.Errorf("default pushMode with remote = %q, want %q", r.PushMode(), PushModeOnCommit)
	}

	r2, err := NewRepo(RepoOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if r2.PushMode() != PushModeManual {
		t.Errorf("default pushMode no-remote = %q, want %q", r2.PushMode(), PushModeManual)
	}
}

func TestGitHookCommitsWithSourceFromContext(t *testing.T) {
	requireGit(t)
	store := newTestStore(t)
	root := t.TempDir()
	mirror := NewDiskMirror(store, root)
	repo, err := NewRepo(RepoOptions{Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureInit(context.Background()); err != nil {
		t.Fatal(err)
	}
	mirror.SetHook(GitHook(repo, false))

	ctx := WithSource(context.Background(), SourceUI)
	if err := mirror.Save(ctx, sampleVM(2)); err != nil {
		t.Fatalf("Save: %v", err)
	}

	out, err := exec.CommandContext(ctx, "git", "-C", root, "log", "-1", "--pretty=%s").Output() // #nosec G204 -- path from t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(out))
	want := "apply VirtualMachine/vm-1 via UI"
	if got != want {
		t.Errorf("commit subject = %q, want %q", got, want)
	}

	// Delete via CLI source — should produce a "delete ... via CLI" commit.
	ctx2 := WithSource(context.Background(), SourceCLI)
	if err := mirror.Delete(ctx2, "proxmox.openctl.io/v1", "VirtualMachine", "vm-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	out, _ = exec.CommandContext(ctx2, "git", "-C", root, "log", "-1", "--pretty=%s").Output() // #nosec G204 -- path from t.TempDir()
	got = strings.TrimSpace(string(out))
	want = "delete VirtualMachine/vm-1 via CLI"
	if got != want {
		t.Errorf("delete commit subject = %q, want %q", got, want)
	}
}

func TestGitHookSwallowsNothingToCommit(t *testing.T) {
	requireGit(t)
	store := newTestStore(t)
	root := t.TempDir()
	mirror := NewDiskMirror(store, root)
	repo, _ := NewRepo(RepoOptions{Dir: root})
	if err := repo.EnsureInit(context.Background()); err != nil {
		t.Fatal(err)
	}
	mirror.SetHook(GitHook(repo, false))

	ctx := context.Background()
	if err := mirror.Save(ctx, sampleVM(2)); err != nil {
		t.Fatal(err)
	}
	// Save the same manifest again — yaml content identical, git sees no
	// staged changes, hook must NOT propagate ErrNothingToCommit.
	if err := mirror.Save(ctx, sampleVM(2)); err != nil {
		t.Errorf("second identical Save: %v", err)
	}
}

func TestCommitMessageFormat(t *testing.T) {
	tests := []struct {
		verb, kind, name, source, want string
	}{
		{"apply", "VirtualMachine", "vm-1", "cli", "apply VirtualMachine/vm-1 via CLI"},
		{"delete", "Cluster", "c", "ui", "delete Cluster/c via UI"},
		{"apply", "K3sNode", "n", "robot", "apply K3sNode/n via ROBOT"},
	}
	for _, tt := range tests {
		if got := commitMessage(tt.verb, tt.kind, tt.name, tt.source); got != tt.want {
			t.Errorf("commitMessage(%s,%s,%s,%s) = %q, want %q",
				tt.verb, tt.kind, tt.name, tt.source, got, tt.want)
		}
	}
}

// TestRepoStartPeriodicPullReconcilesRemoteCommits proves the git-as-source
// loop: when a new commit lands on the remote, the periodic pull advances the
// local tree and invokes the reconcile callback.
func TestRepoStartPeriodicPullReconcilesRemoteCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not on PATH; skipping git-dependent test")
	}
	ctx := t.Context() // canceled when the test ends, stopping the pull goroutine

	base := t.TempDir()
	bare := filepath.Join(base, "remote.git")
	authorDir := filepath.Join(base, "author")
	ctlDir := filepath.Join(base, "ctl")

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...) // #nosec G204 -- test args, paths from t.TempDir()
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	run(base, "init", "--bare", "-b", "main", bare)
	// Author clone seeds an initial commit and pushes it.
	run(base, "clone", bare, authorDir)
	run(authorDir, "config", "user.email", "a@example.com")
	run(authorDir, "config", "user.name", "Author")
	if err := os.WriteFile(filepath.Join(authorDir, "seed.txt"), []byte("seed"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(authorDir, "add", "-A")
	run(authorDir, "commit", "-m", "seed")
	run(authorDir, "push", "origin", "HEAD")

	// Controller clone bound to a Repo with the remote.
	run(base, "clone", bare, ctlDir)
	repo, err := NewRepo(RepoOptions{Dir: ctlDir, Remote: bare})
	if err != nil {
		t.Fatalf("NewRepo: %v", err)
	}

	var mu sync.Mutex
	changes := 0
	repo.StartPeriodicPull(ctx, 30*time.Millisecond, func(context.Context) error {
		mu.Lock()
		changes++
		mu.Unlock()
		return nil
	}, nil)

	// A new commit lands on the remote; the loop should pull + reconcile it.
	if err := os.WriteFile(filepath.Join(authorDir, "new.txt"), []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(authorDir, "add", "-A")
	run(authorDir, "commit", "-m", "add new")
	run(authorDir, "push", "origin", "HEAD")

	if !waitFor(3*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return changes >= 1 }) {
		t.Fatal("pull loop never reconciled the new remote commit")
	}
	// The pulled tree must contain the new file.
	if _, err := os.Stat(filepath.Join(ctlDir, "new.txt")); err != nil {
		t.Errorf("pulled working tree missing new.txt: %v", err)
	}
}
