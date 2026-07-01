package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"embed"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"

	"github.com/openctl/openctl/internal/controller/manifests"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

// sessionCookieName is the HttpOnly cookie set by Login. The HTTP gateway
// strips it from the request and rewrites it as an Authorization: Bearer
// header before forwarding to the gRPC server, so the existing
// Bearer-based auth interceptor doesn't need to know about cookies.
const sessionCookieName = "openctl_session"

// uiAssets holds the built UI's static assets (Vite output). Empty in
// dev — the package returns a friendly "run `make ui`" page when no
// assets are present, so the binary always works even pre-build.
//
// We embed `uiassets/dist` (not `uiassets/`) so Vite can blow the dist
// directory away on every build without disturbing the parent's git
// metadata (`.gitkeep`, `.gitignore`).
//
//go:embed all:uiassets/dist
var uiAssets embed.FS

// NewHTTPGateway constructs an HTTP handler combining the grpc-gateway
// REST/JSON surface at /v1/*, the UI assets at /ui/* (served from
// embed.FS — pre-Vite-build this is just the placeholder page), and
// session-cookie auth middleware that translates an openctl_session
// cookie into the Authorization: Bearer header the gRPC layer expects.
//
// caCertPEM is the controller's CA cert; the gateway dials the gRPC
// server over TLS so we never speak plaintext on the wire. grpcAddr is
// the gRPC listen address (e.g. 127.0.0.1:9444) — typically the same
// loopback host:port the controller itself listens on, since the HTTP
// gateway runs alongside in the same process.
func NewHTTPGateway(ctx context.Context, grpcAddr string, caCertPEM []byte, serverName string) (http.Handler, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCertPEM) {
		return nil, fmt.Errorf("invalid CA cert PEM")
	}
	creds := credentials.NewTLS(&tls.Config{
		RootCAs:    pool,
		ServerName: serverName,
		MinVersion: tls.VersionTLS12,
	})
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("dial gRPC for gateway: %w", err)
	}

	// WithMetadata: stamp every gateway-proxied request with the
	// "x-openctl-source: ui" gRPC metadata header so resource handlers
	// can tell browser ops from CLI ops (used by the git layer to label
	// commits "via UI" vs "via CLI"). CLI clients call gRPC directly and
	// don't set this header, so absence = CLI.
	gw := runtime.NewServeMux(
		runtime.WithMetadata(func(_ context.Context, _ *http.Request) metadata.MD {
			return metadata.Pairs(sourceMetadataKey, manifests.SourceUI)
		}),
	)
	if err := apiv1.RegisterPingServiceHandler(ctx, gw, conn); err != nil {
		return nil, err
	}
	if err := apiv1.RegisterResourceServiceHandler(ctx, gw, conn); err != nil {
		return nil, err
	}
	if err := apiv1.RegisterOperationServiceHandler(ctx, gw, conn); err != nil {
		return nil, err
	}
	if err := apiv1.RegisterSchemaServiceHandler(ctx, gw, conn); err != nil {
		return nil, err
	}
	if err := apiv1.RegisterSessionServiceHandler(ctx, gw, conn); err != nil {
		return nil, err
	}
	if err := apiv1.RegisterRepoServiceHandler(ctx, gw, conn); err != nil {
		return nil, err
	}
	if err := apiv1.RegisterTemplateServiceHandler(ctx, gw, conn); err != nil {
		return nil, err
	}
	if err := apiv1.RegisterConfigServiceHandler(ctx, gw, conn); err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	// API surface — wrapped in cookie→bearer middleware, then Login-response
	// cookie-setter so the browser persists the session token automatically.
	mux.Handle("/v1/", cookieToBearer(setCookieOnLogin(gw)))

	// UI assets. embed.FS roots at "uiassets/dist/"; strip to serve at /ui/.
	mux.Handle("/ui/", http.StripPrefix("/ui/", uiHandler()))
	mux.Handle("/ui", http.RedirectHandler("/ui/", http.StatusMovedPermanently))

	// Bare / → /ui/ for convenience.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/ui/", http.StatusMovedPermanently)
			return
		}
		http.NotFound(w, r)
	})
	return mux, nil
}

// uiHandler serves UI assets out of the embedded FS when present, or
// returns a friendly placeholder page in dev (when `make ui` hasn't built
// yet). Parameterized over assets so tests can inject an empty FS to
// exercise the placeholder path even after a real build is baked in.
func uiHandler() http.Handler {
	sub, err := fs.Sub(uiAssets, "uiassets/dist")
	if err != nil {
		return http.HandlerFunc(uiPlaceholder)
	}
	return uiHandlerFor(sub)
}

func uiHandlerFor(assets fs.FS) http.Handler {
	entries, _ := fs.ReadDir(assets, ".")
	hasContent := false
	for _, e := range entries {
		// Anything other than the .gitkeep / .gitignore / README placeholder
		// counts as a real build. .gitignore exists so Vite output stays
		// untracked in git; it's not user-facing content.
		switch e.Name() {
		case ".gitkeep", ".gitignore", "README.md":
			continue
		}
		hasContent = true
		break
	}
	if !hasContent {
		return http.HandlerFunc(uiPlaceholder)
	}
	return http.FileServer(http.FS(assets))
}

func uiPlaceholder(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(uiPlaceholderBody))
}

const uiPlaceholderBody = `<!doctype html>
<html><head><meta charset="utf-8"><title>openctl</title>
<style>body{font:14px -apple-system,sans-serif;max-width:720px;margin:80px auto;padding:0 20px;color:#222}</style>
</head><body>
<h1>openctl controller</h1>
<p>The API is up at <code>/v1/*</code> (try <code>GET /v1/ping</code>).</p>
<p>The UI is not built yet. Run <code>make ui</code> from the openctl repo to populate
<code>internal/controller/server/uiassets/dist/</code> with the Vite build output, then
restart the controller.</p>
<p>UI implementation tracker: <code>UI.md</code>, Phase U3 onward.</p>
</body></html>`

// cookieToBearer rewrites an openctl_session cookie into the
// Authorization: Bearer header the gRPC interceptor expects. The
// cookie name lives in sessionCookieName; this is the only place
// the HTTP layer talks to the auth layer.
func cookieToBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only rewrite when no explicit Authorization header is present —
		// curl --header overrides cookie auth, which is the right
		// fallback for debug.
		if r.Header.Get("Authorization") == "" {
			if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
				r.Header.Set("Authorization", "Bearer "+c.Value)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// setCookieOnLogin intercepts the response to POST /v1/session/login and
// sets the session token as an HttpOnly cookie so subsequent requests
// from the browser auto-authenticate without JavaScript touching the
// raw token. The cookie is set BEFORE the body is written through, so
// the browser sees Set-Cookie in the response headers.
func setCookieOnLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/session/login" {
			next.ServeHTTP(w, r)
			return
		}
		rec := &loginRecorder{ResponseWriter: w, secure: r.TLS != nil}
		next.ServeHTTP(rec, r)
		rec.flush()
	})
}

// loginRecorder wraps an http.ResponseWriter. It buffers the response
// long enough to extract the LoginResponse.token from the JSON body, then
// writes Set-Cookie + the buffered body through to the real writer. This
// has to be buffer-then-flush (not stream-through) because http.SetCookie
// must run before WriteHeader; once WriteHeader is called we can't add
// headers anymore.
type loginRecorder struct {
	http.ResponseWriter
	secure  bool
	status  int
	headers http.Header
	body    []byte
	written bool
}

func (r *loginRecorder) Header() http.Header {
	if r.headers == nil {
		r.headers = http.Header{}
	}
	return r.headers
}

func (r *loginRecorder) WriteHeader(code int) {
	if r.written {
		return
	}
	r.status = code
}

func (r *loginRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.body = append(r.body, b...)
	return len(b), nil
}

// flush copies buffered headers + status + body through to the underlying
// writer, after first setting the session cookie if we captured a token.
// Called from the parent handler after next.ServeHTTP returns.
func (r *loginRecorder) flush() {
	if r.written {
		return
	}
	r.written = true

	// Copy buffered headers through.
	dst := r.ResponseWriter.Header()
	for k, vs := range r.headers {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}

	// Set the session cookie if Login succeeded.
	if r.status == http.StatusOK {
		token := extractJSONString(string(r.body), "token")
		expiresAt := extractJSONString(string(r.body), "expiresAt")
		if token != "" {
			maxAge := 7 * 24 * 60 * 60
			if expiresAt != "" {
				if exp, err := time.Parse(time.RFC3339Nano, expiresAt); err == nil {
					if d := time.Until(exp); d > 0 {
						maxAge = int(d.Seconds())
					}
				}
			}
			http.SetCookie(r.ResponseWriter, &http.Cookie{
				Name:     sessionCookieName,
				Value:    token,
				Path:     "/",
				MaxAge:   maxAge,
				HttpOnly: true,
				Secure:   r.secure,
				SameSite: http.SameSiteStrictMode,
			})
		}
	}

	status := r.status
	if status == 0 {
		status = http.StatusOK
	}
	r.ResponseWriter.WriteHeader(status)
	_, _ = r.ResponseWriter.Write(r.body)
}

// extractJSONString plucks a top-level "name":"value" string field out
// of a small JSON object without pulling in encoding/json (avoids
// double-decoding). Returns "" if not found.
func extractJSONString(body, name string) string {
	needle := `"` + name + `":"`
	i := strings.Index(body, needle)
	if i < 0 {
		return ""
	}
	rest := body[i+len(needle):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// ServeHTTPGateway is the top-level helper main.go calls. Spins up an
// HTTP listener at addr that handles /v1/*, /ui/*, and / routes; returns
// once the listener errors or ctx cancels.
func ServeHTTPGateway(ctx context.Context, addr, grpcAddr string, caCertPEM []byte, serverName string) error {
	h, err := NewHTTPGateway(ctx, grpcAddr, caCertPEM, serverName)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	return srv.Serve(ln)
}
