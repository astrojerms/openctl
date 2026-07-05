package server

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/providers"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

func TestAuthorize(t *testing.T) {
	cases := []struct {
		name    string
		ctx     context.Context
		min     auth.Role
		wantErr bool
	}{
		{"no principal (auth disabled) allows", context.Background(), auth.RoleAdmin, false},
		{"viewer denied editor", ctxWithRole(auth.RoleViewer), auth.RoleEditor, true},
		{"viewer denied admin", ctxWithRole(auth.RoleViewer), auth.RoleAdmin, true},
		{"viewer allowed viewer", ctxWithRole(auth.RoleViewer), auth.RoleViewer, false},
		{"editor allowed editor", ctxWithRole(auth.RoleEditor), auth.RoleEditor, false},
		{"editor denied admin", ctxWithRole(auth.RoleEditor), auth.RoleAdmin, true},
		{"editor allowed viewer", ctxWithRole(auth.RoleEditor), auth.RoleViewer, false},
		{"admin allowed everything", ctxWithRole(auth.RoleAdmin), auth.RoleAdmin, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := authorize(c.ctx, c.min)
			if c.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				if status.Code(err) != codes.PermissionDenied {
					t.Errorf("code = %v, want PermissionDenied", status.Code(err))
				}
			} else if err != nil {
				t.Errorf("want nil, got %v", err)
			}
		})
	}
}

func ctxWithRole(r auth.Role) context.Context {
	return auth.ContextWithPrincipal(context.Background(), auth.Principal{UserID: "u", Role: r})
}

// TestResourceServiceEnforcesRBAC proves the enforcement is wired at the
// handler entry points: a viewer is denied a mutation but allowed a read, and
// an editor clears the mutation's authz gate (failing later only on the empty
// request, i.e. InvalidArgument — never PermissionDenied).
func TestResourceServiceEnforcesRBAC(t *testing.T) {
	h := newResourceHandler(providers.NewRegistry(), nil, nil, nil)

	// Viewer cannot Apply (a mutation).
	_, err := h.Apply(ctxWithRole(auth.RoleViewer), &apiv1.ApplyRequest{})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("viewer Apply: code = %v, want PermissionDenied", status.Code(err))
	}

	// Viewer cannot Delete.
	_, err = h.Delete(ctxWithRole(auth.RoleViewer), &apiv1.DeleteRequest{})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("viewer Delete: code = %v, want PermissionDenied", status.Code(err))
	}

	// Editor clears the Apply authz gate; the empty request then fails
	// validation (InvalidArgument), proving we got past authorization.
	_, err = h.Apply(ctxWithRole(auth.RoleEditor), &apiv1.ApplyRequest{})
	if status.Code(err) == codes.PermissionDenied {
		t.Error("editor Apply should pass the authz gate")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("editor Apply on empty request: code = %v, want InvalidArgument", status.Code(err))
	}

	// Viewer may List (a read) — authz must not block it.
	if _, err := h.List(ctxWithRole(auth.RoleViewer), &apiv1.ListRequest{}); status.Code(err) == codes.PermissionDenied {
		t.Error("viewer List should be permitted")
	}

	// No principal (auth disabled): a mutation is allowed through the gate.
	if _, err := h.Apply(context.Background(), &apiv1.ApplyRequest{}); status.Code(err) == codes.PermissionDenied {
		t.Error("auth-disabled Apply should not be permission-denied")
	}
}
