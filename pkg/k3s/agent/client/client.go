// Package client is the plugin-side typed HTTP client for talking to
// openctl-k3s-agent over mTLS. It hides the cert plumbing and gives the
// plugin typed methods like Info().
package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/openctl/openctl/pkg/k3s/agent"
)

// Options configures a Client.
type Options struct {
	Endpoint      string // host[:port]; e.g. "192.168.1.50:9443"
	CACertPEM     []byte
	ClientCertPEM []byte
	ClientKeyPEM  []byte
	Timeout       time.Duration // per-request; default 10s
}

// Client talks to one node's agent.
type Client struct {
	baseURL string
	http    *http.Client
}

// New constructs a Client. The cert material is loaded eagerly so config
// errors surface here, not on first request.
func New(opts Options) (*Client, error) {
	if opts.Endpoint == "" {
		return nil, fmt.Errorf("endpoint required")
	}
	if opts.Timeout == 0 {
		opts.Timeout = 10 * time.Second
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(opts.CACertPEM) {
		return nil, fmt.Errorf("parse CA: no PEM blocks found")
	}
	keypair, err := tls.X509KeyPair(opts.ClientCertPEM, opts.ClientKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse client keypair: %w", err)
	}

	host, port, err := net.SplitHostPort(opts.Endpoint)
	if err != nil {
		// Allow bare host; default to agent's well-known port.
		host = opts.Endpoint
		port = strconv.Itoa(9443)
	}

	return &Client{
		baseURL: "https://" + net.JoinHostPort(host, port),
		http: &http.Client{
			Timeout: opts.Timeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					RootCAs:      pool,
					Certificates: []tls.Certificate{keypair},
					MinVersion:   tls.VersionTLS12,
				},
			},
		},
	}, nil
}

// StartK3s, StopK3s, and RestartK3s call POST /v1/service/k3s/{action}. They
// return nil on the agent's 204 No Content; otherwise they wrap the body.
// RestartK3s backs `openctl k3s restart` (see DESIGN.md "Plugin-defined CLI
// subcommands"); Start/Stop have no CLI surface yet.
func (c *Client) StartK3s(ctx context.Context) error   { return c.serviceAction(ctx, "start") }
func (c *Client) StopK3s(ctx context.Context) error    { return c.serviceAction(ctx, "stop") }
func (c *Client) RestartK3s(ctx context.Context) error { return c.serviceAction(ctx, "restart") }

func (c *Client) serviceAction(ctx context.Context, action string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/service/k3s/"+action, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req) // #nosec G107,G704 -- baseURL is from typed Options.Endpoint
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("service %s: status %d: %s", action, resp.StatusCode, string(body))
}

// Logs calls GET /v1/logs/k3s. lines == 0 lets the agent pick its default.
// It backs `openctl k3s logs <cluster>` (see DESIGN.md "Plugin-defined CLI
// subcommands").
func (c *Client) Logs(ctx context.Context, lines int) (string, error) {
	u := c.baseURL + "/v1/logs/k3s"
	if lines > 0 {
		u += "?lines=" + strconv.Itoa(lines)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req) // #nosec G107,G704 -- baseURL is from typed Options.Endpoint
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("logs: status %d: %s", resp.StatusCode, string(body))
	}
	return string(body), nil
}

// Info calls GET /v1/info.
func (c *Client) Info(ctx context.Context) (*agent.Info, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/info", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req) // #nosec G107,G704 -- baseURL is derived from typed Options.Endpoint, not user input
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("info: status %d: %s", resp.StatusCode, string(body))
	}
	var info agent.Info
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode info: %w", err)
	}
	return &info, nil
}
