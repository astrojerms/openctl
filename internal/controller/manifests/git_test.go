package manifests

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
