package server

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/storage"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

func newTestMirror(t *testing.T) *manifests.DiskMirror {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return manifests.NewDiskMirror(manifests.New(db), t.TempDir())
}

func newTestRepo(t *testing.T, dir string) *manifests.Repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	r, err := manifests.NewRepo(manifests.RepoOptions{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.EnsureInit(context.Background()); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRepoServiceGetStatusWithoutGitReturnsDisabled(t *testing.T) {
	mirror := newTestMirror(t)
	h := newRepoHandler(mirror, nil)
	resp, err := h.GetStatus(context.Background(), &apiv1.GetRepoStatusRequest{})
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if resp.Enabled {
		t.Error("Enabled = true with no git wired")
	}
	if resp.Dir != mirror.Root() {
		t.Errorf("Dir = %q, want %q", resp.Dir, mirror.Root())
	}
}

func TestRepoServiceGetStatusWithGitReturnsCleanRepo(t *testing.T) {
	mirror := newTestMirror(t)
	repo := newTestRepo(t, mirror.Root())
	h := newRepoHandler(mirror, repo)

	resp, err := h.GetStatus(context.Background(), &apiv1.GetRepoStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Enabled {
		t.Error("Enabled = false with git wired")
	}
	if !resp.Clean {
		t.Errorf("Clean = false on fresh repo; dirty=%v", resp.DirtyPaths)
	}
	if resp.Branch != "main" {
		t.Errorf("Branch = %q, want main", resp.Branch)
	}
	if resp.Ahead != -1 || resp.Behind != -1 {
		t.Errorf("ahead/behind = %d/%d, want -1/-1 (no remote)", resp.Ahead, resp.Behind)
	}
}

func TestRepoServicePushFailsWithoutRemote(t *testing.T) {
	mirror := newTestMirror(t)
	repo := newTestRepo(t, mirror.Root())
	h := newRepoHandler(mirror, repo)

	_, err := h.Push(context.Background(), &apiv1.PushRepoRequest{})
	if err == nil {
		t.Fatal("Push should error with no remote configured")
	}
	if got := status.Code(err); got != codes.FailedPrecondition {
		t.Errorf("error code = %v, want FailedPrecondition", got)
	}
	if !strings.Contains(err.Error(), "no remote") {
		t.Errorf("error %q should mention 'no remote'", err.Error())
	}
}

func TestRepoServicePushFailsWithoutGit(t *testing.T) {
	mirror := newTestMirror(t)
	h := newRepoHandler(mirror, nil)
	_, err := h.Push(context.Background(), &apiv1.PushRepoRequest{})
	if err == nil {
		t.Fatal("Push should error with git disabled")
	}
	if got := status.Code(err); got != codes.FailedPrecondition {
		t.Errorf("error code = %v, want FailedPrecondition", got)
	}
}

func TestRepoServicePullFailsWithoutRemote(t *testing.T) {
	mirror := newTestMirror(t)
	repo := newTestRepo(t, mirror.Root())
	h := newRepoHandler(mirror, repo)
	_, err := h.Pull(context.Background(), &apiv1.PullRepoRequest{})
	if got := status.Code(err); got != codes.FailedPrecondition {
		t.Errorf("error code = %v, want FailedPrecondition", got)
	}
}
