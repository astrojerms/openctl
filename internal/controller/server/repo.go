package server

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/openctl/openctl/internal/controller/manifests"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

// repoHandler implements apiv1.RepoServiceServer. mirror is the disk
// projection of the controller's desired state; repo (optional) is the
// git wrapper that operates on mirror.Root(). When repo is nil, git
// tracking is off in config — GetStatus returns enabled=false and the
// dir + base fields; Push/Pull return FailedPrecondition.
type repoHandler struct {
	apiv1.UnimplementedRepoServiceServer
	mirror *manifests.DiskMirror
	repo   *manifests.Repo
}

func newRepoHandler(mirror *manifests.DiskMirror, repo *manifests.Repo) *repoHandler {
	return &repoHandler{mirror: mirror, repo: repo}
}

func (h *repoHandler) GetStatus(ctx context.Context, _ *apiv1.GetRepoStatusRequest) (*apiv1.GetRepoStatusResponse, error) {
	resp := &apiv1.GetRepoStatusResponse{}
	if h.mirror != nil {
		resp.Dir = h.mirror.Root()
	}
	if h.repo == nil {
		return resp, nil
	}
	resp.Enabled = true
	resp.Remote = h.repo.Remote()
	resp.PushMode = h.repo.PushMode()

	st, err := h.repo.Status(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "git status: %v", err)
	}
	resp.Branch = st.Branch
	resp.HeadSha = st.HeadSHA
	resp.Clean = st.Clean
	resp.DirtyPaths = st.DirtyPaths
	// Status returns -1 for "not measurable"; preserve that on the wire so
	// the UI can render "—" rather than misleading zeros.
	if st.Ahead >= 0 {
		resp.Ahead = int32(st.Ahead) // #nosec G115 -- ahead/behind from git: small integer count
	} else {
		resp.Ahead = -1
	}
	if st.Behind >= 0 {
		resp.Behind = int32(st.Behind) // #nosec G115 -- see above
	} else {
		resp.Behind = -1
	}
	return resp, nil
}

func (h *repoHandler) Push(ctx context.Context, _ *apiv1.PushRepoRequest) (*apiv1.PushRepoResponse, error) {
	if h.repo == nil {
		return nil, status.Error(codes.FailedPrecondition, "git tracking is disabled (set manifests.git.enabled in config)")
	}
	if h.repo.Remote() == "" {
		return nil, status.Error(codes.FailedPrecondition, "no remote configured (set manifests.git.remote in config)")
	}
	if err := h.repo.Push(ctx); err != nil {
		return nil, status.Errorf(codes.Internal, "git push: %v", err)
	}
	return &apiv1.PushRepoResponse{
		Message: fmt.Sprintf("pushed to %s", h.repo.Remote()),
	}, nil
}

func (h *repoHandler) Pull(ctx context.Context, _ *apiv1.PullRepoRequest) (*apiv1.PullRepoResponse, error) {
	if h.repo == nil {
		return nil, status.Error(codes.FailedPrecondition, "git tracking is disabled (set manifests.git.enabled in config)")
	}
	if h.repo.Remote() == "" {
		return nil, status.Error(codes.FailedPrecondition, "no remote configured (set manifests.git.remote in config)")
	}
	if err := h.repo.Pull(ctx); err != nil {
		return nil, status.Errorf(codes.Internal, "git pull: %v", err)
	}
	return &apiv1.PullRepoResponse{
		Message: fmt.Sprintf("pulled from %s", h.repo.Remote()),
	}, nil
}
