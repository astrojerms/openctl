package auth

import "context"

// Role is a coarse permission level for an authenticated caller. Roles are
// ordered: admin ⊃ editor ⊃ viewer. Higher roles include every capability of
// the lower ones.
type Role string

const (
	// RoleViewer may read resources but not mutate them.
	RoleViewer Role = "viewer"
	// RoleEditor may read and mutate resources (apply/delete/invoke actions).
	RoleEditor Role = "editor"
	// RoleAdmin has every capability, including future admin-only surfaces.
	RoleAdmin Role = "admin"
)

// rank orders roles for AtLeast comparisons. Unknown roles rank 0 (below
// viewer), so an unrecognized role authorizes nothing.
func (r Role) rank() int {
	switch r {
	case RoleViewer:
		return 1
	case RoleEditor:
		return 2
	case RoleAdmin:
		return 3
	default:
		return 0
	}
}

// AtLeast reports whether r is at least as privileged as minRole. An unknown r
// is never at least anything (rank 0).
func (r Role) AtLeast(minRole Role) bool {
	return r.rank() > 0 && r.rank() >= minRole.rank()
}

// Principal is the authenticated caller resolved from the bearer token. It is
// injected into the request context by the auth interceptors and read by
// handlers to make authorization decisions.
type Principal struct {
	// UserID identifies the caller. "root" for the install-time root token.
	UserID string
	// Role is the caller's permission level.
	Role Role
	// Root is true for the install-time root token (full admin, never
	// revocable via the user store).
	Root bool
}

// RootPrincipal is the principal for the install-time root token: full admin.
func RootPrincipal() Principal {
	return Principal{UserID: "root", Role: RoleAdmin, Root: true}
}

type principalCtxKey struct{}

// ContextWithPrincipal returns a copy of ctx carrying p.
func ContextWithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalCtxKey{}, p)
}

// PrincipalFromContext returns the authenticated principal and true, or a zero
// Principal and false when none is present (e.g. auth disabled via --no-auth,
// where no interceptor runs).
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalCtxKey{}).(Principal)
	return p, ok
}
