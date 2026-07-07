package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// VaultProvider resolves secrets from a HashiCorp Vault (or OpenBao) KV store
// over its HTTP API. It is deliberately dependency-free (net/http, no Vault
// SDK) — Tier 2 backends only need to implement Resolve, and the KV read is a
// single authenticated GET.
//
// A $secret marker names a Vault path and a field within it:
//
//	{$secret: {provider: "vault", key: "secret/data/db#password"}}
//
// The key is "<vault-path>#<field>". The provider GETs {address}/v1/<path> with
// the token, and reads <field> from the response. Both KV engine versions are
// handled: KV v2 nests values under data.data, KV v1 under data — the provider
// tries data.data first, then data.
type VaultProvider struct {
	name      string
	address   string // base URL, no trailing slash
	token     string
	namespace string
	client    *http.Client
}

// NewVaultProvider builds a Vault-backed SecretProvider. name is what a marker
// references; address is the Vault base URL; token authenticates (X-Vault-Token);
// namespace is optional (Vault Enterprise).
func NewVaultProvider(name, address, token, namespace string) *VaultProvider {
	return &VaultProvider{
		name:      name,
		address:   strings.TrimRight(address, "/"),
		token:     token,
		namespace: namespace,
		client:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (p *VaultProvider) Name() string { return p.name }

func (p *VaultProvider) Resolve(ctx context.Context, key string) (string, error) {
	path, field, ok := strings.Cut(key, "#")
	if !ok || path == "" || field == "" {
		return "", fmt.Errorf("vault key must be \"<path>#<field>\" (got %q)", key)
	}
	url := fmt.Sprintf("%s/v1/%s", p.address, strings.TrimLeft(path, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	if p.token != "" {
		req.Header.Set("X-Vault-Token", p.token)
	}
	if p.namespace != "" {
		req.Header.Set("X-Vault-Namespace", p.namespace)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("vault request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("vault %s: status %d", path, resp.StatusCode)
	}

	// KV v2: {"data":{"data":{field:val}}}; KV v1: {"data":{field:val}}.
	var parsed struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		if v, ok := stringFromAny(parsed.Data.Data[field]); ok {
			return v, nil
		}
	}
	var flat struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &flat); err == nil {
		if v, ok := stringFromAny(flat.Data[field]); ok {
			return v, nil
		}
	}
	return "", fmt.Errorf("vault %s: field %q not found", path, field)
}

// stringFromAny coerces a JSON value to a string when it is one, so a secret
// stored as a plain string resolves; non-string values (numbers, objects) are
// rejected rather than stringified.
func stringFromAny(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}
