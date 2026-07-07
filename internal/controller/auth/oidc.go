package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// ErrOIDCDenied is returned when an authenticated user maps to no role
// (fail-closed). The caller surfaces it as an access-denied response, never as
// a silent grant.
var ErrOIDCDenied = errors.New("oidc: authenticated but no role assigned")

// OIDCConfig is the plain-parameter form of the OIDC settings, decoupled from
// the config package so this package doesn't import it.
type OIDCConfig struct {
	Issuer        string
	ClientID      string
	ClientSecret  string
	RedirectURL   string
	RoleClaim     string            // default "groups"
	RoleMapping   map[string]string // claim value → role name
	DefaultRole   string            // "" = deny
	UsernameClaim string            // default "email"
}

// OIDCAuthenticator wraps go-oidc + oauth2 to run Authorization Code + PKCE and
// map ID-token claims to an openctl {UserID, Role}. It produces sessions via
// the existing SessionStore — it adds an identity source, nothing downstream.
type OIDCAuthenticator struct {
	verifier      *oidc.IDTokenVerifier
	oauth         *oauth2.Config
	roleClaim     string
	roleMapping   map[string]Role
	defaultRole   Role // "" = deny
	usernameClaim string
}

// NewOIDCAuthenticator discovers the IdP (<issuer>/.well-known/openid-
// configuration) and builds the verifier + oauth2 config. Returns an error on
// an unreachable/invalid issuer or an unknown role in the mapping.
func NewOIDCAuthenticator(ctx context.Context, cfg OIDCConfig) (*OIDCAuthenticator, error) {
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for %q: %w", cfg.Issuer, err)
	}
	mapping := make(map[string]Role, len(cfg.RoleMapping))
	for claim, roleStr := range cfg.RoleMapping {
		role, err := parseRole(roleStr)
		if err != nil {
			return nil, fmt.Errorf("roleMapping[%q]: %w", claim, err)
		}
		mapping[claim] = role
	}
	var defaultRole Role
	if cfg.DefaultRole != "" {
		if defaultRole, err = parseRole(cfg.DefaultRole); err != nil {
			return nil, fmt.Errorf("defaultRole: %w", err)
		}
	}
	roleClaim := cfg.RoleClaim
	if roleClaim == "" {
		roleClaim = "groups"
	}
	usernameClaim := cfg.UsernameClaim
	if usernameClaim == "" {
		usernameClaim = "email"
	}
	return &OIDCAuthenticator{
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  cfg.RedirectURL,
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
		roleClaim:     roleClaim,
		roleMapping:   mapping,
		defaultRole:   defaultRole,
		usernameClaim: usernameClaim,
	}, nil
}

// AuthCodeURL builds the IdP authorization URL with a state value and a PKCE
// S256 challenge derived from verifier.
func (a *OIDCAuthenticator) AuthCodeURL(state, verifier string) string {
	return a.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
}

// Exchange trades an authorization code (+ PKCE verifier) for tokens and
// verifies the ID token (signature against the IdP JWKS, issuer, audience,
// expiry). Returns the verified claims.
func (a *OIDCAuthenticator) Exchange(ctx context.Context, code, verifier string) (map[string]any, error) {
	tok, err := a.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("oidc token exchange: %w", err)
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok || rawID == "" {
		return nil, errors.New("oidc: token response had no id_token")
	}
	idToken, err := a.verifier.Verify(ctx, rawID)
	if err != nil {
		return nil, fmt.Errorf("oidc: verify id_token: %w", err)
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("oidc: decode claims: %w", err)
	}
	return claims, nil
}

// Principal maps verified claims to a {userID, role}. Fail-closed: a user
// matching no role and with no defaultRole returns ErrOIDCDenied.
func (a *OIDCAuthenticator) Principal(claims map[string]any) (userID string, role Role, err error) {
	userID, _ = claims[a.usernameClaim].(string)
	role = a.mapRole(claims)
	if role == "" {
		return "", "", ErrOIDCDenied
	}
	return userID, role, nil
}

// mapRole resolves the highest-privilege role among the caller's role-claim
// values, falling back to defaultRole (which may be "" = deny). The role claim
// may be a single string or a list of strings (the common "groups" shape).
func (a *OIDCAuthenticator) mapRole(claims map[string]any) Role {
	best := a.defaultRole
	consider := func(v string) {
		if r, ok := a.roleMapping[v]; ok && r.rank() > best.rank() {
			best = r
		}
	}
	switch v := claims[a.roleClaim].(type) {
	case string:
		consider(v)
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				consider(s)
			}
		}
	}
	return best
}

// parseRole validates a role string.
func parseRole(s string) (Role, error) {
	switch Role(s) {
	case RoleViewer, RoleEditor, RoleAdmin:
		return Role(s), nil
	default:
		return "", fmt.Errorf("unknown role %q (want viewer|editor|admin)", s)
	}
}
