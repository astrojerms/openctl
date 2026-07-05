package server

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/openctl/openctl/internal/controller/auth"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

// sessionHandler implements apiv1.SessionServiceServer. The actual
// credential check (the caller must hold a valid bearer or session token
// to call Login/Logout/WhoAmI) is enforced by the auth.Validator
// interceptor running before this handler. Login then mints a NEW
// session token regardless of which credential the caller used.
type sessionHandler struct {
	apiv1.UnimplementedSessionServiceServer
	sessions *auth.SessionStore
}

func newSessionHandler(sessions *auth.SessionStore) *sessionHandler {
	return &sessionHandler{sessions: sessions}
}

func (h *sessionHandler) Login(ctx context.Context, req *apiv1.LoginRequest) (*apiv1.LoginResponse, error) {
	if h.sessions == nil {
		return nil, status.Error(codes.Unimplemented, "session store not configured")
	}
	ttl := time.Duration(req.GetTtlSeconds()) * time.Second
	// Inherit the caller's identity: the interceptor resolved their bearer
	// token to a Principal, so a viewer-token holder mints a viewer session.
	// With --no-auth (no interceptor, no principal) fall back to the admin
	// default so single-user setups keep working.
	userID, role := auth.DefaultUserID, auth.RoleAdmin
	if p, ok := auth.PrincipalFromContext(ctx); ok {
		if !p.Root {
			userID = p.UserID
		}
		role = p.Role
	}
	sess, err := h.sessions.Create(ctx, userID, req.GetDisplayName(), role, ttl)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create session: %v", err)
	}
	return &apiv1.LoginResponse{
		Token:     sess.Token,
		SessionId: sess.ID,
		ExpiresAt: sess.ExpiresAt,
	}, nil
}

func (h *sessionHandler) Logout(ctx context.Context, _ *apiv1.LogoutRequest) (*apiv1.LogoutResponse, error) {
	if h.sessions == nil {
		return nil, status.Error(codes.Unimplemented, "session store not configured")
	}
	tok := bearerFromCtx(ctx)
	if tok == "" {
		return &apiv1.LogoutResponse{}, nil
	}
	if err := h.sessions.DeleteByToken(ctx, tok); err != nil {
		return nil, status.Errorf(codes.Internal, "delete session: %v", err)
	}
	return &apiv1.LogoutResponse{}, nil
}

func (h *sessionHandler) WhoAmI(ctx context.Context, _ *apiv1.WhoAmIRequest) (*apiv1.WhoAmIResponse, error) {
	tok := bearerFromCtx(ctx)
	if h.sessions != nil && tok != "" {
		if sess, _ := h.sessions.Lookup(ctx, tok); sess != nil {
			return &apiv1.WhoAmIResponse{UserId: sess.UserID, SessionId: sess.ID}, nil
		}
	}
	// No session match — caller authenticated via the root bearer token.
	// Leave user_id empty to signal "root credential, not a session".
	return &apiv1.WhoAmIResponse{}, nil
}

// bearerFromCtx pulls the raw token out of the incoming Authorization
// header. Returns "" if missing or malformed.
func bearerFromCtx(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	auths := md.Get("authorization")
	if len(auths) == 0 {
		return ""
	}
	tok, ok := strings.CutPrefix(auths[0], "Bearer ")
	if !ok {
		return ""
	}
	return tok
}
