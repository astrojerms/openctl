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

func TestSessionHandlerLogoutRevokesCallersToken(t *testing.T) {
	store := newSessionStoreForTest(t)
	h := newSessionHandler(store)

	// Pre-mint a session and then call Logout with that token in metadata.
	sess, _ := store.Create(context.Background(), "", "x", 0)
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

	// Session-token caller: WhoAmI returns user_id + session_id.
	sess, _ := store.Create(context.Background(), "", "", 0)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"authorization", "Bearer "+sess.Token,
	))
	resp, _ := h.WhoAmI(ctx, &apiv1.WhoAmIRequest{})
	if resp.GetSessionId() != sess.ID {
		t.Errorf("session caller: SessionId = %q, want %q", resp.GetSessionId(), sess.ID)
	}
	if resp.GetUserId() != auth.DefaultUserID {
		t.Errorf("session caller: UserId = %q, want %q", resp.GetUserId(), auth.DefaultUserID)
	}

	// Root-bearer caller: token doesn't match any session.
	rootCtx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"authorization", "Bearer some-root-token-not-in-sessions",
	))
	rootResp, _ := h.WhoAmI(rootCtx, &apiv1.WhoAmIRequest{})
	if rootResp.GetSessionId() != "" {
		t.Errorf("root caller: SessionId = %q, want empty", rootResp.GetSessionId())
	}
	if rootResp.GetUserId() != "" {
		t.Errorf("root caller: UserId = %q, want empty", rootResp.GetUserId())
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
