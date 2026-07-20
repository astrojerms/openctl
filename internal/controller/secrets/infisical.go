package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// InfisicalProvider resolves secrets from a self-hosted (or cloud) Infisical
// instance over its HTTP API. Like the Vault provider it is dependency-free
// (net/http, no SDK) — a Tier 2 backend only needs Resolve.
//
// A $secret marker names a secret by (optional) path and name:
//
//	{$secret: {provider: "infisical", key: "DB_PASSWORD"}}
//	{$secret: {provider: "infisical", key: "/app/prod#DB_PASSWORD"}}
//
// The key is "[<secretPath>#]<secretName>" — the path defaults to "/". The
// project and environment are fixed per configured provider (register multiple
// named providers for multiple environments), mirroring how the Vault provider
// takes a namespace.
//
// Auth is one of two modes, chosen by whether a clientID is configured:
//   - Universal Auth (machine identity): clientID + secret (the client secret)
//     are exchanged for a short-lived access token via the universal-auth login
//     endpoint, then used as a Bearer token.
//   - Static token: secret is used directly as a Bearer token (a service token
//     or a pre-minted access token).
type InfisicalProvider struct {
	name        string
	host        string // base URL, no trailing slash
	clientID    string // set → Universal Auth; empty → static bearer token
	secret      string // client secret (universal auth) or bearer token (static)
	projectID   string
	environment string
	client      *http.Client
}

// NewInfisicalProvider builds an Infisical-backed SecretProvider. When clientID
// is non-empty, secret is the Universal Auth client secret; otherwise secret is
// used directly as a Bearer token (service/access token).
func NewInfisicalProvider(name, host, clientID, secret, projectID, environment string) *InfisicalProvider {
	return &InfisicalProvider{
		name:        name,
		host:        strings.TrimRight(host, "/"),
		clientID:    clientID,
		secret:      secret,
		projectID:   projectID,
		environment: environment,
		client:      &http.Client{Timeout: 15 * time.Second},
	}
}

func (p *InfisicalProvider) Name() string { return p.name }

func (p *InfisicalProvider) Resolve(ctx context.Context, key string) (string, error) {
	secretPath, secretName := "/", key
	if path, name, ok := strings.Cut(key, "#"); ok {
		if name == "" {
			return "", fmt.Errorf("infisical key %q: secret name after # is empty", key)
		}
		secretName = name
		if path != "" {
			secretPath = path
		}
	}
	if secretName == "" {
		return "", fmt.Errorf("infisical key %q: secret name is empty", key)
	}

	token, err := p.accessToken(ctx)
	if err != nil {
		return "", err
	}

	q := url.Values{}
	q.Set("workspaceId", p.projectID)
	q.Set("environment", p.environment)
	q.Set("secretPath", secretPath)
	reqURL := fmt.Sprintf("%s/api/v3/secrets/raw/%s?%s", p.host, url.PathEscape(secretName), q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("infisical request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("infisical %s (env %s): status %d", secretName, p.environment, resp.StatusCode)
	}
	var parsed struct {
		Secret struct {
			SecretValue string `json:"secretValue"`
		} `json:"secret"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("infisical %s: decode response: %w", secretName, err)
	}
	if parsed.Secret.SecretValue == "" {
		return "", fmt.Errorf("infisical %s: empty or missing secretValue", secretName)
	}
	return parsed.Secret.SecretValue, nil
}

// accessToken returns the Bearer token to use: the static token as-is when no
// clientID is configured, or a fresh Universal Auth access token otherwise.
// (Not cached — resolution happens a handful of times per apply, and a stale
// cached token would be worse than a re-login.)
func (p *InfisicalProvider) accessToken(ctx context.Context) (string, error) {
	if p.clientID == "" {
		if p.secret == "" {
			return "", fmt.Errorf("infisical %q: no clientId (Universal Auth) or token configured", p.name)
		}
		return p.secret, nil
	}
	reqBody, _ := json.Marshal(map[string]string{
		"clientId":     p.clientID,
		"clientSecret": p.secret,
	})
	reqURL := p.host + "/api/v1/auth/universal-auth/login"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("infisical login: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("infisical login: status %d", resp.StatusCode)
	}
	var parsed struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("infisical login: decode response: %w", err)
	}
	if parsed.AccessToken == "" {
		return "", fmt.Errorf("infisical login: no accessToken in response")
	}
	return parsed.AccessToken, nil
}
