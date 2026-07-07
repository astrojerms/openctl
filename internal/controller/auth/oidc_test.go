package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// fakeIDP is a minimal OIDC provider for tests — the OIDC analog of
// plugins/tf-fake. It serves discovery + JWKS + a token endpoint that issues an
// ID token signed with a test key, so the full go-oidc verification path runs
// without an external provider.
type fakeIDP struct {
	srv      *httptest.Server
	signer   jose.Signer
	clientID string
	// idClaims is what the token endpoint bakes into the next id_token.
	idClaims map[string]any
}

func newFakeIDP(t *testing.T, clientID string) *fakeIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const kid = "test-key"
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithHeader("kid", kid).WithType("JWT"),
	)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIDP{signer: signer, clientID: clientID}

	jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key: key.Public(), KeyID: kid, Algorithm: "RS256", Use: "sig",
	}}}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		iss := f.srv.URL
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                iss,
			"authorization_endpoint":                iss + "/authorize",
			"token_endpoint":                        iss + "/token",
			"jwks_uri":                              iss + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jwks)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		idToken := f.signID(t)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fake-access",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     idToken,
		})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIDP) signID(t *testing.T) string {
	t.Helper()
	claims := map[string]any{
		"iss": f.srv.URL,
		"aud": f.clientID,
		"sub": "user-123",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
	maps.Copy(claims, f.idClaims)
	payload, _ := json.Marshal(claims)
	obj, err := f.signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	s, err := obj.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newAuthnForIDP(t *testing.T, f *fakeIDP, cfg OIDCConfig) *OIDCAuthenticator {
	t.Helper()
	cfg.Issuer = f.srv.URL
	cfg.ClientID = f.clientID
	a, err := NewOIDCAuthenticator(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewOIDCAuthenticator: %v", err)
	}
	return a
}

// Happy path: a valid code exchange verifies the ID token and maps the group
// claim to a role.
func TestOIDC_ExchangeAndMapRole(t *testing.T) {
	f := newFakeIDP(t, "openctl")
	f.idClaims = map[string]any{"email": "alice@example.com", "groups": []any{"openctl-editors"}}
	a := newAuthnForIDP(t, f, OIDCConfig{
		RoleClaim:   "groups",
		RoleMapping: map[string]string{"openctl-editors": "editor", "openctl-admins": "admin"},
	})

	claims, err := a.Exchange(context.Background(), "any-code", "verifier")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	userID, role, err := a.Principal(claims)
	if err != nil {
		t.Fatalf("Principal: %v", err)
	}
	if userID != "alice@example.com" {
		t.Errorf("userID = %q", userID)
	}
	if role != RoleEditor {
		t.Errorf("role = %q, want editor", role)
	}
}

// The highest-privilege matching group wins.
func TestOIDC_HighestRoleWins(t *testing.T) {
	f := newFakeIDP(t, "openctl")
	f.idClaims = map[string]any{"email": "a@x", "groups": []any{"openctl-viewers", "openctl-admins"}}
	a := newAuthnForIDP(t, f, OIDCConfig{
		RoleClaim:   "groups",
		RoleMapping: map[string]string{"openctl-viewers": "viewer", "openctl-admins": "admin"},
	})
	claims, err := a.Exchange(context.Background(), "c", "v")
	if err != nil {
		t.Fatal(err)
	}
	if _, role, _ := a.Principal(claims); role != RoleAdmin {
		t.Errorf("role = %q, want admin (highest of viewer+admin)", role)
	}
}

// Fail-closed: an authenticated user matching no mapping and no defaultRole is
// denied.
func TestOIDC_DenyByDefault(t *testing.T) {
	f := newFakeIDP(t, "openctl")
	f.idClaims = map[string]any{"email": "nobody@x", "groups": []any{"some-other-group"}}
	a := newAuthnForIDP(t, f, OIDCConfig{
		RoleClaim:   "groups",
		RoleMapping: map[string]string{"openctl-admins": "admin"},
		// DefaultRole empty → deny
	})
	claims, err := a.Exchange(context.Background(), "c", "v")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Principal(claims); !errors.Is(err, ErrOIDCDenied) {
		t.Errorf("err = %v, want ErrOIDCDenied", err)
	}
}

// A defaultRole is granted when nothing matches.
func TestOIDC_DefaultRole(t *testing.T) {
	f := newFakeIDP(t, "openctl")
	f.idClaims = map[string]any{"email": "guest@x"}
	a := newAuthnForIDP(t, f, OIDCConfig{
		RoleClaim:   "groups",
		DefaultRole: "viewer",
	})
	claims, err := a.Exchange(context.Background(), "c", "v")
	if err != nil {
		t.Fatal(err)
	}
	if _, role, err := a.Principal(claims); err != nil || role != RoleViewer {
		t.Errorf("role=%q err=%v, want viewer", role, err)
	}
}

// A wrong-audience token is rejected by go-oidc verification.
func TestOIDC_WrongAudienceRejected(t *testing.T) {
	f := newFakeIDP(t, "openctl")
	f.idClaims = map[string]any{"aud": "some-other-client", "email": "a@x"}
	a := newAuthnForIDP(t, f, OIDCConfig{RoleClaim: "groups", DefaultRole: "viewer"})
	if _, err := a.Exchange(context.Background(), "c", "v"); err == nil {
		t.Fatal("expected verification to reject a wrong-audience token")
	}
}

// An expired token is rejected.
func TestOIDC_ExpiredRejected(t *testing.T) {
	f := newFakeIDP(t, "openctl")
	f.idClaims = map[string]any{"exp": time.Now().Add(-time.Hour).Unix(), "email": "a@x"}
	a := newAuthnForIDP(t, f, OIDCConfig{RoleClaim: "groups", DefaultRole: "viewer"})
	if _, err := a.Exchange(context.Background(), "c", "v"); err == nil {
		t.Fatal("expected verification to reject an expired token")
	}
}

// An unknown role in the mapping is a construction error.
func TestOIDC_BadRoleMapping(t *testing.T) {
	f := newFakeIDP(t, "openctl")
	_, err := NewOIDCAuthenticator(context.Background(), OIDCConfig{
		Issuer:      f.srv.URL,
		ClientID:    "openctl",
		RoleMapping: map[string]string{"g": "superuser"},
	})
	if err == nil {
		t.Fatal("expected an error for an unknown role")
	}
}
