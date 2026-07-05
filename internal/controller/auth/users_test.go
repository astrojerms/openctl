package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/metadata"
)

func writeUsersFile(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, usersFileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadUsers_MissingFileIsNotAnError(t *testing.T) {
	users, err := LoadUsers(t.TempDir())
	if err != nil {
		t.Fatalf("missing users.yaml should be fine, got %v", err)
	}
	if users != nil {
		t.Errorf("want nil users, got %v", users)
	}
}

func TestLoadUsers_MintsAndReadsTokens(t *testing.T) {
	dir := t.TempDir()
	// alice's token file is pre-created; bob's must be minted by LoadUsers.
	if err := os.WriteFile(filepath.Join(dir, "alice.token"), []byte("alice-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeUsersFile(t, dir, `
users:
  - name: alice
    role: editor
    tokenFile: alice.token
  - name: bob
    role: viewer
    tokenFile: bob.token
`)

	users, err := LoadUsers(dir)
	if err != nil {
		t.Fatalf("LoadUsers: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("got %d users, want 2", len(users))
	}
	byName := map[string]User{users[0].UserID: users[0], users[1].UserID: users[1]}
	if got := byName["alice"]; got.Role != RoleEditor || got.Token != "alice-secret" {
		t.Errorf("alice = %+v, want editor/alice-secret", got)
	}
	if got := byName["bob"]; got.Role != RoleViewer || got.Token == "" {
		t.Errorf("bob = %+v, want viewer with a minted token", got)
	}
	// bob's token file should now exist on disk (minted, mode 0600).
	info, err := os.Stat(filepath.Join(dir, "bob.token"))
	if err != nil {
		t.Fatalf("bob token file not minted: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("bob token file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestLoadUsers_Rejects(t *testing.T) {
	cases := map[string]string{
		"invalid role":  `users: [{name: a, role: superuser, tokenFile: a.token}]`,
		"empty name":    `users: [{name: "", role: viewer, tokenFile: a.token}]`,
		"reserved root": `users: [{name: root, role: admin, tokenFile: a.token}]`,
		"duplicate name": `users:
  - {name: a, role: viewer, tokenFile: a.token}
  - {name: a, role: editor, tokenFile: b.token}`,
		"missing tokenFile": `users: [{name: a, role: viewer}]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeUsersFile(t, dir, body)
			if _, err := LoadUsers(dir); err == nil {
				t.Errorf("want error for %s", name)
			}
		})
	}
}

func TestLoadUsers_AbsoluteTokenFile(t *testing.T) {
	dir := t.TempDir()
	tokPath := filepath.Join(t.TempDir(), "elsewhere.token")
	if err := os.WriteFile(tokPath, []byte("abs-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeUsersFile(t, dir, "users: [{name: c, role: admin, tokenFile: "+tokPath+"}]")
	users, err := LoadUsers(dir)
	if err != nil {
		t.Fatalf("LoadUsers: %v", err)
	}
	if len(users) != 1 || users[0].Token != "abs-token" || users[0].Role != RoleAdmin {
		t.Errorf("got %+v, want one admin with abs-token", users)
	}
}

func TestValidatorResolvesUserTokens(t *testing.T) {
	v := NewValidator("root-token").WithUsers([]User{
		{UserID: "alice", Role: RoleEditor, Token: "alice-token"},
		{UserID: "bob", Role: RoleViewer, Token: "bob-token"},
	})

	check := func(bearer string) (Principal, error) {
		ctx := metadata.NewIncomingContext(context.Background(),
			metadata.Pairs("authorization", "Bearer "+bearer))
		return v.check(ctx)
	}

	// Root still resolves to admin.
	if p, err := check("root-token"); err != nil || p.Role != RoleAdmin || !p.Root {
		t.Errorf("root: p=%+v err=%v", p, err)
	}
	// alice → editor.
	if p, err := check("alice-token"); err != nil || p.UserID != "alice" || p.Role != RoleEditor {
		t.Errorf("alice: p=%+v err=%v", p, err)
	}
	// bob → viewer.
	if p, err := check("bob-token"); err != nil || p.UserID != "bob" || p.Role != RoleViewer {
		t.Errorf("bob: p=%+v err=%v", p, err)
	}
	// Unknown token → error.
	if _, err := check("nope"); err == nil {
		t.Error("unknown token should fail")
	}
}
