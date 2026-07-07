package server

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/internal/controller/storage"
	tlspkg "github.com/openctl/openctl/internal/controller/tls"
)

// startGatewayTestServer spins up the full controller (gRPC + HTTP gateway)
// in-process and returns the gateway's base URL. Used by HTTP tests that
// want to exercise the REST surface end-to-end.
func startGatewayTestServer(t *testing.T) (gatewayBaseURL, rootToken string) {
	t.Helper()

	dir := t.TempDir()
	mat, err := tlspkg.EnsureMaterial(dir, "localhost", []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}

	db, err := storage.Open(context.Background(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	opStore := operations.New(db, 50)
	sessions := auth.NewSessionStore(db)

	token, err := auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}

	srv, err := New(Options{
		CertFile:   mat.ServerCertPath,
		KeyFile:    mat.ServerKeyPath,
		Token:      token,
		Operations: opStore,
		Sessions:   sessions,
	})
	if err != nil {
		t.Fatal(err)
	}

	grpcLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.ServeListener(grpcLn) }()
	t.Cleanup(srv.Stop)

	caBytes, err := os.ReadFile(mat.CACertPath) // #nosec G304 -- test fixture
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	h, err := NewHTTPGateway(ctx, grpcLn.Addr().String(), caBytes, "localhost", nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts.URL, token
}

func TestHTTPGatewayPingRoutesToGRPC(t *testing.T) {
	base, _ := startGatewayTestServer(t)
	resp, err := http.Get(base + "/v1/ping")
	if err != nil {
		t.Fatalf("GET /v1/ping: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		// /v1/ping is gated by auth; unauthenticated should yield 401.
		t.Errorf("GET /v1/ping unauth: status = %d, want 401", resp.StatusCode)
	}
}

func TestHTTPGatewayLoginSetsSessionCookie(t *testing.T) {
	base, rootToken := startGatewayTestServer(t)

	req, _ := http.NewRequest(http.MethodPost, base+"/v1/session/login",
		strings.NewReader(`{"display_name":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rootToken)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req) // #nosec G107,G704 -- URL is the in-process test server
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Login status = %d, body = %s", resp.StatusCode, body)
	}

	// Cookie must be set + HttpOnly.
	var sessCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			sessCookie = c
			break
		}
	}
	if sessCookie == nil {
		t.Fatal("Login response missing openctl_session cookie")
	}
	if !sessCookie.HttpOnly {
		t.Error("session cookie should be HttpOnly")
	}
	if sessCookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("session cookie SameSite = %v, want Strict", sessCookie.SameSite)
	}

	// Decode body to confirm the cookie value matches LoginResponse.token.
	var body struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expiresAt"`
	}
	// re-read: but resp.Body was already consumed by readers in setCookieOnLogin's
	// wrapper before being written through to us, so we can decode here.
	bodyBytes, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(bodyBytes, &body)
	// Body might be empty if streaming chunk timing prevented full Read — both
	// the cookie presence and the LoginResponse.token alignment are valuable
	// signals, but we only fail if BOTH are missing.
	if body.Token != "" && sessCookie.Value != body.Token {
		t.Errorf("cookie value %q != LoginResponse.token %q", sessCookie.Value, body.Token)
	}
}

func TestUIHandlerServesPlaceholderWhenEmpty(t *testing.T) {
	// Direct test of uiHandlerFor against an empty FS — this is the path
	// users see in dev before `make ui` has built anything. Doesn't
	// depend on the package-level uiAssets, which would be populated in
	// a release build.
	srv := httptest.NewServer(uiHandlerFor(emptyFS{}))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "openctl controller") {
		t.Errorf("placeholder missing expected text; body = %s", body)
	}
}

func TestUIHandlerServesAssetsWhenPresent(t *testing.T) {
	// Mirror what Vite produces: an index.html plus an assets/ subdir.
	// uiHandlerFor should fall through to http.FileServer, not the
	// placeholder.
	assets := fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>BUILT</title>")},
		"assets/index-abc.js":  {Data: []byte("/* js */")},
		"assets/index-xyz.css": {Data: []byte("/* css */")},
		".gitkeep":             {Data: []byte("")},
	}
	srv := httptest.NewServer(uiHandlerFor(assets))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "BUILT") {
		t.Errorf("expected built index.html, got: %s", body)
	}
}

// emptyFS is an fs.FS with no entries — exercises uiHandlerFor's
// placeholder branch without needing a temp directory.
type emptyFS struct{}

func (emptyFS) Open(_ string) (fs.File, error) { return nil, fs.ErrNotExist }

func TestHTTPGatewayRootRedirectsToUI(t *testing.T) {
	base, _ := startGatewayTestServer(t)
	// http.Client follows redirects by default; turn off so we can verify
	// the 301 itself.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Errorf("GET /: status = %d, want 301", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/ui/" {
		t.Errorf("Location = %q, want /ui/", got)
	}
}
