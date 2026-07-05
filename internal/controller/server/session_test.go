package server

import (
	"context"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/metadata"

	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/storage"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

func TestSessionHandlerLoginMintsUsableToken(t *testing.T) {
	store := newSessionStoreForTest(t)
	h := newSessionHandler(store)

	resp, err := h.Login(context.Background(), &apiv1.LoginRequest{DisplayName: "test-browser"})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if resp.GetToken() == "" {
		t.Fatal("Login returned empty token")
	}
	if resp.GetSessionId() == "" {
		t.Fatal("Login returned empty session_id")
	}
	if resp.GetExpiresAt() == "" {
		t.Fatal("Login returned empty expires_at")
	}

	sess, err := store.Lookup(context.Background(), resp.GetToken())
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if sess == nil {
		t.Fatal("token from Login didn't resolve to a session")
	}
	if sess.DisplayName != "test-browser" {
		t.Errorf("DisplayName = %q, want test-browser", sess.DisplayName)
	}
}

func TestSessionHandlerLoginInheritsCallerRole(t *testing.T) {
	store := newSessionStoreForTest(t)
	h := newSessionHandler(store)

	// Caller authenticated as a viewer (principal injected by the interceptor).
	ctx := auth.ContextWithPrincipal(context.Background(),
		auth.Principal{UserID: "alice", Role: auth.RoleViewer})
	resp, err := h.Login(ctx, &apiv1.LoginRequest{DisplayName: "alice-laptop"})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	sess, err := store.Lookup(context.Background(), resp.GetToken())
	if err != nil || sess == nil {
		t.Fatalf("Lookup: %v", err)
	}
	if sess.UserID != "alice" {
		t.Errorf("session UserID = %q, want alice", sess.UserID)
	}
	if sess.Role != auth.RoleViewer {
		t.Errorf("session role = %q, want viewer (inherited from caller)", sess.Role)
	}
	if p := sess.Principal(); p.Role != auth.RoleViewer {
		t.Errorf("session principal role = %q, want viewer", p.Role)
	}
}

func TestSessionHandlerLoginNoAuthDefaultsAdmin(t *testing.T) {
	store := newSessionStoreForTest(t)
	h := newSessionHandler(store)

	// No principal in ctx (--no-auth): fall back to the admin default.
	resp, err := h.Login(context.Background(), &apiv1.LoginRequest{})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	sess, _ := store.Lookup(context.Background(), resp.GetToken())
	if sess == nil || sess.Role != auth.RoleAdmin || sess.UserID != auth.DefaultUserID {
		t.Errorf("no-auth login session = %+v, want default/admin", sess)
	}
}

func TestSessionHandlerLogoutRevokesCallersToken(t *testing.T) {
	store := newSessionStoreForTest(t)
	h := newSessionHandler(store)

	// Pre-mint a session and then call Logout with that token in metadata.
	sess, _ := store.Create(context.Background(), "", "x", auth.RoleAdmin, 0)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"authorization", "Bearer "+sess.Token,
	))
	if _, err := h.Logout(ctx, &apiv1.LogoutRequest{}); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	got, _ := store.Lookup(context.Background(), sess.Token)
	if got != nil {
		t.Error("session should be revoked after Logout")
	}
}

func TestSessionHandlerWhoAmIIdentifiesSessionVsRoot(t *testing.T) {
	store := newSessionStoreForTest(t)
	h := newSessionHandler(store)

	// Session-token caller: WhoAmI returns user_id + session_id + role.
	sess, _ := store.Create(context.Background(), "alice", "", auth.RoleViewer, 0)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"authorization", "Bearer "+sess.Token,
	))
	resp, _ := h.WhoAmI(ctx, &apiv1.WhoAmIRequest{})
	if resp.GetSessionId() != sess.ID {
		t.Errorf("session caller: SessionId = %q, want %q", resp.GetSessionId(), sess.ID)
	}
	if resp.GetUserId() != "alice" {
		t.Errorf("session caller: UserId = %q, want alice", resp.GetUserId())
	}
	if resp.GetRole() != "viewer" {
		t.Errorf("session caller: Role = %q, want viewer", resp.GetRole())
	}

	// Root-bearer caller: principal injected (as the interceptor would), Root.
	rootCtx := auth.ContextWithPrincipal(context.Background(), auth.RootPrincipal())
	rootResp, _ := h.WhoAmI(rootCtx, &apiv1.WhoAmIRequest{})
	if rootResp.GetSessionId() != "" {
		t.Errorf("root caller: SessionId = %q, want empty", rootResp.GetSessionId())
	}
	if rootResp.GetUserId() != "" {
		t.Errorf("root caller: UserId = %q, want empty (root signal)", rootResp.GetUserId())
	}
	if rootResp.GetRole() != "admin" {
		t.Errorf("root caller: Role = %q, want admin", rootResp.GetRole())
	}

	// Named-user token caller (no session): principal carries user + role.
	userCtx := auth.ContextWithPrincipal(context.Background(),
		auth.Principal{UserID: "bob", Role: auth.RoleEditor})
	userResp, _ := h.WhoAmI(userCtx, &apiv1.WhoAmIRequest{})
	if userResp.GetUserId() != "bob" || userResp.GetRole() != "editor" {
		t.Errorf("named-user caller: got %q/%q, want bob/editor", userResp.GetUserId(), userResp.GetRole())
	}
	if userResp.GetSessionId() != "" {
		t.Errorf("named-user caller: SessionId = %q, want empty", userResp.GetSessionId())
	}
}

func newSessionStoreForTest(t *testing.T) *auth.SessionStore {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return auth.NewSessionStore(db)
}
