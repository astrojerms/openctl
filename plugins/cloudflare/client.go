package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultEndpoint is the Cloudflare REST API v4 base. Overridable via the
// provider config's endpoint (used by tests to point at a fake server).
const defaultEndpoint = "https://api.cloudflare.com/client/v4"

// errNotFound is returned by cfClient.do for a 404 (or a Cloudflare
// "record/resource does not exist" error code) so callers can map it to
// pluginproto.NotFound / idempotent deletes.
var errNotFound = errors.New("cloudflare: not found")

// cfClient is a minimal, dependency-free Cloudflare API v4 client. It speaks
// the standard `{success, errors, result}` envelope and authenticates with a
// scoped API token (Bearer). We hand-roll it rather than vendor cloudflare-go
// to keep the plugin stdlib-only and the surface auditable.
type cfClient struct {
	http     *http.Client
	endpoint string
	token    string
}

func newClient(endpoint, token string) *cfClient {
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	return &cfClient{
		http:     &http.Client{Timeout: 30 * time.Second},
		endpoint: strings.TrimRight(endpoint, "/"),
		token:    token,
	}
}

// cfEnvelope is Cloudflare's standard JSON response envelope. Result is left
// raw so each caller decodes it into the shape it expects (object, array, or
// even a bare string for the tunnel token endpoint).
type cfEnvelope struct {
	Success bool            `json:"success"`
	Errors  []cfAPIError    `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type cfAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// notFoundCodes are Cloudflare error codes that mean "the addressed object
// doesn't exist" even when the HTTP status isn't a clean 404.
var notFoundCodes = map[int]bool{
	81044: true, // DNS record not found
	1049:  true, // tunnel not found
	7003:  true, // could not route to resource (nonexistent id)
}

// do performs one API call. body is JSON-encoded when non-nil; out (when
// non-nil) receives the decoded `result`. A 404 or a not-found error code
// returns errNotFound; any other non-success returns a descriptive error.
func (c *cfClient) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}
	u := c.endpoint + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}

	var env cfEnvelope
	if len(data) > 0 {
		// A non-JSON body on an error status is possible (gateway errors); fall
		// through to the status-based error below if it doesn't parse.
		_ = json.Unmarshal(data, &env)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !env.Success {
		for _, e := range env.Errors {
			if notFoundCodes[e.Code] {
				return errNotFound
			}
		}
		return fmt.Errorf("cloudflare %s %s: %s", method, path, envError(env, resp.StatusCode))
	}
	if out != nil && len(env.Result) > 0 && string(env.Result) != "null" {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("decode result: %w", err)
		}
	}
	return nil
}

// envError renders a Cloudflare error envelope into a single message.
func envError(env cfEnvelope, status int) string {
	if len(env.Errors) == 0 {
		return fmt.Sprintf("HTTP %d (no error detail)", status)
	}
	parts := make([]string, 0, len(env.Errors))
	for _, e := range env.Errors {
		parts = append(parts, fmt.Sprintf("%d: %s", e.Code, e.Message))
	}
	return strings.Join(parts, "; ")
}
