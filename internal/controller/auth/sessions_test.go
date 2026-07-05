package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	"github.com/openctl/openctl/internal/controller/storage"
)

func newTestSessionStore(t *testing.T) *SessionStore {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewSessionStore(db)
}

func TestSessionRoleRoundTrip(t *testing.T) {
	s := newTestSessionStore(t)
	ctx := context.Background()

	// A viewer session round-trips its role through Lookup and Principal.
	sess, err := s.Create(ctx, "alice", "browser", RoleViewer, time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.Role != RoleViewer {
		t.Errorf("created session role = %q, want viewer", sess.Role)
	}
	got, err := s.Lookup(ctx, sess.Token)
	if err != nil || got == nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.Role != RoleViewer {
		t.Errorf("looked-up role = %q, want viewer", got.Role)
	}
	if p := got.Principal(); p.UserID != "alice" || p.Role != RoleViewer {
		t.Errorf("Principal = %+v, want alice/viewer", p)
	}

	// An empty role defaults to admin at Create time.
	def, _ := s.Create(ctx, "bob", "", "", time.Hour)
	if def.Role != RoleAdmin {
		t.Errorf("empty role should default to admin, got %q", def.Role)
	}
}

// TestSessionLegacyRowDefaultsAdmin inserts a session row the pre-roles way
// (omitting the role column), which is how the migration's `ADD COLUMN role
// NOT NULL DEFAULT 'admin'` backfills existing rows. Lookup must surface admin.
func TestSessionLegacyRowDefaultsAdmin(t *testing.T) {
	s := newTestSessionStore(t)
	ctx := context.Background()

	token := "legacy-token"
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, token_hash, display_name, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"sess-legacy", "olduser", hashToken(token), "old",
		now.Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano),
	)
	if err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	got, err := s.Lookup(ctx, token)
	if err != nil || got == nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.Role != RoleAdmin {
		t.Errorf("legacy row role = %q, want admin (column default)", got.Role)
	}
	if got.Principal().Role != RoleAdmin {
		t.Errorf("legacy row principal role = %q, want admin", got.Principal().Role)
	}
}

func TestSessionCreateAndLookupRoundTrip(t *testing.T) {
	s := newTestSessionStore(t)
	ctx := context.Background()

	sess, err := s.Create(ctx, "", "browser-mac", RoleAdmin, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.Token == "" {
		t.Fatal("Create returned empty token")
	}
	if sess.UserID != DefaultUserID {
		t.Errorf("UserID = %q, want %q", sess.UserID, DefaultUserID)
	}

	got, err := s.Lookup(ctx, sess.Token)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got == nil {
		t.Fatal("Lookup returned nil for a session we just created")
	}
	if got.ID != sess.ID {
		t.Errorf("Lookup returned id %q, want %q", got.ID, sess.ID)
	}
	if got.DisplayName != "browser-mac" {
		t.Errorf("DisplayName = %q, want browser-mac", got.DisplayName)
	}
	// Lookup must NEVER echo the raw token back.
	if got.Token != "" {
		t.Error("Lookup leaked raw token in response")
	}
}

func TestSessionLookupMissReturnsNilNil(t *testing.T) {
	s := newTestSessionStore(t)
	got, err := s.Lookup(context.Background(), "not-a-real-token")
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if got != nil {
		t.Errorf("got = %v, want nil", got)
	}
}

func TestSessionLookupExpiredReturnsNil(t *testing.T) {
	s := newTestSessionStore(t)
	ctx := context.Background()

	// Create a session that expires in the past.
	sess, err := s.Create(ctx, "", "ephemeral", RoleAdmin, -time.Hour)
	if err == nil && sess != nil {
		// TTL was rejected (we coerce <= 0 to default). Force-expire via direct UPDATE.
		past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
		if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET expires_at = ? WHERE id = ?`, past, sess.ID); err != nil {
			t.Fatal(err)
		}
		got, err := s.Lookup(ctx, sess.Token)
		if err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		if got != nil {
			t.Error("expired session should look up to nil")
		}
	}
}

func TestSessionDeleteByTokenRevokes(t *testing.T) {
	s := newTestSessionStore(t)
	ctx := context.Background()

	sess, _ := s.Create(ctx, "", "throwaway", RoleAdmin, 0)
	if err := s.DeleteByToken(ctx, sess.Token); err != nil {
		t.Fatalf("DeleteByToken: %v", err)
	}
	got, _ := s.Lookup(ctx, sess.Token)
	if got != nil {
		t.Error("session should be gone after DeleteByToken")
	}
}

func TestSessionGCExpiredRemovesPastSessions(t *testing.T) {
	s := newTestSessionStore(t)
	ctx := context.Background()

	// Create one live + one we'll expire by hand.
	live, _ := s.Create(ctx, "", "live", RoleAdmin, time.Hour)
	dead, _ := s.Create(ctx, "", "dead", RoleAdmin, time.Hour)
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET expires_at = ? WHERE id = ?`, past, dead.ID); err != nil {
		t.Fatal(err)
	}

	n, err := s.GCExpired(ctx)
	if err != nil {
		t.Fatalf("GCExpired: %v", err)
	}
	if n != 1 {
		t.Errorf("GC removed %d sessions, want 1", n)
	}
	if got, _ := s.Lookup(ctx, live.Token); got == nil {
		t.Error("live session should survive GC")
	}
}

func TestValidatorAcceptsSessionTokenViaWithSessions(t *testing.T) {
	s := newTestSessionStore(t)
	sess, _ := s.Create(context.Background(), "", "", RoleAdmin, 0)

	v := NewValidator("root-bearer-token").WithSessions(s)
	// Root token still works.
	if err := v.checkRaw("Bearer root-bearer-token"); err != nil {
		t.Errorf("root token check failed: %v", err)
	}
	// Session token works too.
	if err := v.checkRaw("Bearer " + sess.Token); err != nil {
		t.Errorf("session token check failed: %v", err)
	}
	// Random token fails.
	if err := v.checkRaw("Bearer not-a-real-token"); err == nil {
		t.Error("random token should fail")
	}
}

// checkRaw exercises Validator.check via a synthetic metadata context.
// Lives here (not in auth.go) because it's only meaningful for tests.
func (v *Validator) checkRaw(authHeader string) error {
	md := metadata.Pairs("authorization", authHeader)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err := v.check(ctx)
	return err
}
