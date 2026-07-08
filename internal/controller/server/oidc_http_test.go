package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The enabled probe returns 200 + {"enabled":true}. It's registered only when
// OIDC is configured, so its mere presence signals SSO availability to the
// login page.
func TestOIDCEnabledProbe(t *testing.T) {
	h := &OIDCHandler{}
	rec := httptest.NewRecorder()
	h.enabled(rec, httptest.NewRequest(http.MethodGet, "/auth/oidc/enabled", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Errorf("body = %q, want it to report enabled:true", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
}

// register mounts the enabled route (so a login page can probe it).
func TestOIDCRegisterMountsEnabled(t *testing.T) {
	mux := http.NewServeMux()
	(&OIDCHandler{}).register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/oidc/enabled", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /auth/oidc/enabled = %d, want 200", rec.Code)
	}
}
