package server

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

func TestRepoAuthorityAdmission(t *testing.T) {
	h := newRepoHandler(nil, nil)
	_, err := h.Pull(context.Background(), &apiv1.PullRepoRequest{})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "mirror mode never pulls") {
		t.Fatalf("mirror pull: %v", err)
	}
	h.mode = "gitops"
	_, err = h.Push(context.Background(), &apiv1.PushRepoRequest{})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "authored upstream") {
		t.Fatalf("gitops push: %v", err)
	}
	for _, mode := range []string{"", "mirror", "gitops"} {
		h.mode = mode
		got, err := h.GetStatus(context.Background(), &apiv1.GetRepoStatusRequest{})
		if err != nil {
			t.Fatal(err)
		}
		want := mode
		if want == "" {
			want = "mirror"
		}
		if got.Mode != want {
			t.Fatalf("mode=%q, want %q", got.Mode, want)
		}
	}
}
