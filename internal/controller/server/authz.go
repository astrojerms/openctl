package server

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/openctl/openctl/internal/controller/auth"
)

// authorize enforces role-based access on a handler. It reads the Principal
// injected by the auth interceptor and requires it to be at least min.
//
// When no Principal is present, auth is disabled for this server (the
// controller was started with --no-auth, so no interceptor ran) — every
// caller is trusted, so authorize allows the request. When a Principal is
// present it is always enforced.
func authorize(ctx context.Context, minRole auth.Role) error {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil // auth disabled; nothing to enforce against
	}
	if !p.Role.AtLeast(minRole) {
		return status.Errorf(codes.PermissionDenied,
			"role %q is not permitted this operation (requires %q or higher)", p.Role, minRole)
	}
	return nil
}
