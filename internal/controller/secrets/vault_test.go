package secrets

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeVault serves a minimal KV API: it echoes a fixed secret at a KV v2 path
// and a KV v1 path, checks the token header, and 404s everything else.
func fakeVault(t *testing.T, wantToken string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/secret/data/db", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != wantToken {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		// KV v2 shape: data.data.<field>
		_, _ = w.Write([]byte(`{"data":{"data":{"password":"hunter2","user":"admin"}}}`))
	})
	mux.HandleFunc("/v1/kv1/app", func(w http.ResponseWriter, _ *http.Request) {
		// KV v1 shape: data.<field>
		_, _ = w.Write([]byte(`{"data":{"apikey":"abc123"}}`))
	})
	return httptest.NewServer(mux)
}

func TestVaultProvider_KVv2(t *testing.T) {
	srv := fakeVault(t, "s.token")
	defer srv.Close()
	p := NewVaultProvider("vault", srv.URL, "s.token", "")

	got, err := p.Resolve(context.Background(), "secret/data/db#password")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "hunter2" {
		t.Errorf("got %q, want hunter2", got)
	}
}

func TestVaultProvider_KVv1(t *testing.T) {
	srv := fakeVault(t, "")
	defer srv.Close()
	p := NewVaultProvider("vault", srv.URL, "", "")

	got, err := p.Resolve(context.Background(), "kv1/app#apikey")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "abc123" {
		t.Errorf("got %q, want abc123", got)
	}
}

func TestVaultProvider_WrongToken(t *testing.T) {
	srv := fakeVault(t, "right")
	defer srv.Close()
	p := NewVaultProvider("vault", srv.URL, "wrong", "")
	if _, err := p.Resolve(context.Background(), "secret/data/db#password"); err == nil {
		t.Fatal("expected a 403 error with the wrong token")
	}
}

func TestVaultProvider_MissingField(t *testing.T) {
	srv := fakeVault(t, "t")
	defer srv.Close()
	p := NewVaultProvider("vault", srv.URL, "t", "")
	_, err := p.Resolve(context.Background(), "secret/data/db#nonexistent")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want 'not found' error, got %v", err)
	}
}

func TestVaultProvider_MalformedKey(t *testing.T) {
	p := NewVaultProvider("vault", "http://x", "t", "")
	for _, key := range []string{"nofield", "#field", "path#"} {
		if _, err := p.Resolve(context.Background(), key); err == nil {
			t.Errorf("key %q should be rejected", key)
		}
	}
}

// The Vault provider integrates with the resolver + registry end to end: a
// $secret marker naming the vault provider resolves through it.
func TestVaultProvider_ThroughResolver(t *testing.T) {
	srv := fakeVault(t, "t")
	defer srv.Close()
	reg := NewRegistry()
	reg.Register(NewVaultProvider("vault", srv.URL, "t", ""))

	in := map[string]any{
		"password": map[string]any{SecretMarker: map[string]any{"provider": "vault", "key": "secret/data/db#password"}},
	}
	out, err := New(reg).Resolve(context.Background(), in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if out["password"] != "hunter2" {
		t.Errorf("resolved %v, want hunter2", out["password"])
	}
}
