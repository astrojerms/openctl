package server

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"time"

	"golang.org/x/oauth2"

	"github.com/openctl/openctl/internal/controller/auth"
)

// OIDC login is a browser front door that mints an existing session. It adds
// two HTTP routes to the gateway mux; everything downstream (session lookup,
// RBAC, cookie) is unchanged — the callback just sources the principal from
// verified IdP claims instead of a pre-shared token.

const (
	oidcStateCookie    = "openctl_oidc_state"
	oidcVerifierCookie = "openctl_oidc_verifier"
	oidcFlowTTL        = 10 * time.Minute
)

// OIDCHandler holds the dependencies of the two OIDC handlers.
type OIDCHandler struct {
	authn    *auth.OIDCAuthenticator
	sessions *auth.SessionStore
	secure   bool
}

// NewOIDCHandler builds the OIDC login handler. secure controls the Secure
// flag on the cookies (true behind TLS, which the gateway always is).
func NewOIDCHandler(authn *auth.OIDCAuthenticator, sessions *auth.SessionStore, secure bool) *OIDCHandler {
	return &OIDCHandler{authn: authn, sessions: sessions, secure: secure}
}

// register mounts the login + callback routes on the mux.
func (h *OIDCHandler) register(mux *http.ServeMux) {
	mux.HandleFunc("/auth/oidc/login", h.login)
	mux.HandleFunc("/auth/oidc/callback", h.callback)
}

// login starts the Authorization Code + PKCE flow: generate a state + PKCE
// verifier, stash them in short-lived HttpOnly cookies, and redirect to the IdP.
func (h *OIDCHandler) login(w http.ResponseWriter, r *http.Request) {
	state, err := randToken()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	verifier := oauth2.GenerateVerifier()
	h.setFlowCookie(w, oidcStateCookie, state)
	h.setFlowCookie(w, oidcVerifierCookie, verifier)
	http.Redirect(w, r, h.authn.AuthCodeURL(state, verifier), http.StatusFound)
}

// callback verifies state, exchanges the code, maps claims → principal, mints a
// session, sets the session cookie, and redirects to the UI. A denied user
// (no role) gets a clear 403 and no session.
func (h *OIDCHandler) callback(w http.ResponseWriter, r *http.Request) {
	// CSRF: the state query param must match the state cookie.
	stateCookie, err := r.Cookie(oidcStateCookie)
	if err != nil || stateCookie.Value == "" || r.URL.Query().Get("state") != stateCookie.Value {
		http.Error(w, "invalid or expired login state — please retry", http.StatusBadRequest)
		return
	}
	verifierCookie, err := r.Cookie(oidcVerifierCookie)
	if err != nil || verifierCookie.Value == "" {
		http.Error(w, "invalid or expired login state — please retry", http.StatusBadRequest)
		return
	}
	// Clear the flow cookies regardless of outcome.
	h.clearFlowCookie(w, oidcStateCookie)
	h.clearFlowCookie(w, oidcVerifierCookie)

	if e := r.URL.Query().Get("error"); e != "" {
		http.Error(w, "login failed at the identity provider: "+e, http.StatusUnauthorized)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing authorization code", http.StatusBadRequest)
		return
	}

	claims, err := h.authn.Exchange(r.Context(), code, verifierCookie.Value)
	if err != nil {
		log.Printf("oidc callback: exchange/verify failed: %v", err)
		http.Error(w, "login failed — could not verify the identity token", http.StatusUnauthorized)
		return
	}
	userID, role, err := h.authn.Principal(claims)
	if err != nil {
		if errors.Is(err, auth.ErrOIDCDenied) {
			http.Error(w, "your account authenticated but has no role assigned in openctl", http.StatusForbidden)
			return
		}
		http.Error(w, "login failed", http.StatusUnauthorized)
		return
	}

	sess, err := h.sessions.Create(r.Context(), userID, userID, role, auth.DefaultSessionTTL)
	if err != nil {
		http.Error(w, "internal error creating session", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sess.Token,
		Path:     "/",
		MaxAge:   int(auth.DefaultSessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/ui/", http.StatusFound)
}

func (h *OIDCHandler) setFlowCookie(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/auth/oidc/",
		MaxAge:   int(oidcFlowTTL.Seconds()),
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteLaxMode, // Lax so the cookie survives the IdP redirect back
	})
}

func (h *OIDCHandler) clearFlowCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/auth/oidc/", MaxAge: -1,
		HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteLaxMode,
	})
}

// randToken returns a URL-safe random string for the OAuth state parameter.
func randToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
