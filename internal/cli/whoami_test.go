package cli

import (
	"testing"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

func TestFormatWhoAmI(t *testing.T) {
	cases := []struct {
		name string
		resp *apiv1.WhoAmIResponse
		want string
	}{
		{
			name: "root credential",
			resp: &apiv1.WhoAmIResponse{Role: "admin"},
			want: "user: <root>  role: admin",
		},
		{
			name: "named user, no session",
			resp: &apiv1.WhoAmIResponse{UserId: "bob", Role: "editor"},
			want: "user: bob  role: editor",
		},
		{
			name: "session caller",
			resp: &apiv1.WhoAmIResponse{UserId: "alice", Role: "viewer", SessionId: "sess-123"},
			want: "user: alice  role: viewer  session: sess-123",
		},
		{
			name: "missing role degrades gracefully",
			resp: &apiv1.WhoAmIResponse{UserId: "x"},
			want: "user: x  role: unknown",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatWhoAmI(c.resp); got != c.want {
				t.Errorf("formatWhoAmI = %q, want %q", got, c.want)
			}
		})
	}
}
