package secrets

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeInfisical serves a minimal Universal Auth login + v3 raw secret read. It
// mints a fixed access token from the given client credentials and serves one
// secret (DB_PASSWORD=hunter2 at path "/") for either that access token or a
// static bearer token.
func fakeInfisical(t *testing.T, clientID, clientSecret, staticToken string) *httptest.Server {
	t.Helper()
	const accessToken = "eyJ-access-token"
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/auth/universal-auth/login", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ ClientID, ClientSecret string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.ClientID != clientID || body.ClientSecret != clientSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": accessToken, "tokenType": "Bearer"})
	})

	mux.HandleFunc("/api/v3/secrets/raw/", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		wantUA := "Bearer " + accessToken
		wantStatic := "Bearer " + staticToken
		if auth != wantUA && (staticToken == "" || auth != wantStatic) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("workspaceId") != "proj-1" || r.URL.Query().Get("environment") != "prod" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/api/v3/secrets/raw/")
		if name != "DB_PASSWORD" || r.URL.Query().Get("secretPath") != "/" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"secret": map[string]any{"secretKey": "DB_PASSWORD", "secretValue": "hunter2"},
		})
	})
	return httptest.NewServer(mux)
}

func TestInfisicalProvider_UniversalAuth(t *testing.T) {
	srv := fakeInfisical(t, "cid", "csecret", "")
	defer srv.Close()
	p := NewInfisicalProvider("infisical", srv.URL, "cid", "csecret", "proj-1", "prod")

	got, err := p.Resolve(context.Background(), "DB_PASSWORD")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "hunter2" {
		t.Errorf("got %q, want hunter2", got)
	}
}

func TestInfisicalProvider_StaticToken(t *testing.T) {
	srv := fakeInfisical(t, "", "", "st.service-token")
	defer srv.Close()
	// No clientID → the token is used directly as a Bearer token.
	p := NewInfisicalProvider("infisical", srv.URL, "", "st.service-token", "proj-1", "prod")

	got, err := p.Resolve(context.Background(), "/#DB_PASSWORD")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "hunter2" {
		t.Errorf("got %q, want hunter2", got)
	}
}

func TestInfisicalProvider_BadCredentials(t *testing.T) {
	srv := fakeInfisical(t, "cid", "csecret", "")
	defer srv.Close()
	p := NewInfisicalProvider("infisical", srv.URL, "cid", "wrong", "proj-1", "prod")
	if _, err := p.Resolve(context.Background(), "DB_PASSWORD"); err == nil {
		t.Fatal("expected a login failure with the wrong client secret")
	}
}

func TestInfisicalProvider_MissingSecret(t *testing.T) {
	srv := fakeInfisical(t, "cid", "csecret", "")
	defer srv.Close()
	p := NewInfisicalProvider("infisical", srv.URL, "cid", "csecret", "proj-1", "prod")
	_, err := p.Resolve(context.Background(), "NONEXISTENT")
	if err == nil || !strings.Contains(err.Error(), "status 404") {
		t.Fatalf("want a 404 status error, got %v", err)
	}
}

func TestInfisicalProvider_MalformedKey(t *testing.T) {
	p := NewInfisicalProvider("infisical", "http://x", "cid", "csecret", "proj-1", "prod")
	for _, key := range []string{"", "/path#"} {
		if _, err := p.Resolve(context.Background(), key); err == nil {
			t.Errorf("key %q should be rejected", key)
		}
	}
}

func TestInfisicalProvider_NoAuthConfigured(t *testing.T) {
	// No clientID and no token → cannot authenticate.
	p := NewInfisicalProvider("infisical", "http://x", "", "", "proj-1", "prod")
	if _, err := p.Resolve(context.Background(), "DB_PASSWORD"); err == nil {
		t.Fatal("expected an error when no auth is configured")
	}
}

// End-to-end: a $secret marker naming the infisical provider resolves through
// the registry + resolver.
func TestInfisicalProvider_ThroughResolver(t *testing.T) {
	srv := fakeInfisical(t, "cid", "csecret", "")
	defer srv.Close()
	reg := NewRegistry()
	reg.Register(NewInfisicalProvider("infisical", srv.URL, "cid", "csecret", "proj-1", "prod"))

	in := map[string]any{
		"password": map[string]any{SecretMarker: map[string]any{"provider": "infisical", "key": "DB_PASSWORD"}},
	}
	out, err := New(reg).Resolve(context.Background(), in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if out["password"] != "hunter2" {
		t.Errorf("resolved %v, want hunter2", out["password"])
	}
}
