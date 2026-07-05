package client

import (
	"context"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/openctl/openctl/pkg/k3s/agent"
)

// NodeStatus is the result of probing one node's agent. Reachable is false if
// any step (cert load, dial, TLS, RPC) failed; Error carries the reason.
type NodeStatus struct {
	Name      string
	IP        string
	Reachable bool
	Error     string
	Info      *agent.Info
}

// ProbeOptions points at the cert material and tunes timing.
type ProbeOptions struct {
	CAPath         string
	ClientCertPath string
	ClientKeyPath  string
	Port           int
	Timeout        time.Duration // per-node; default 2s
}

// NewFromProbeOptions builds a Client for a single endpoint using the cert
// file paths in opts — the same material ProbeNodes reads. It reads the PEM
// files eagerly so config errors surface here rather than on first request.
// endpoint is host[:port]; if it carries no port, opts.Port (or the agent's
// default) is applied.
func NewFromProbeOptions(opts ProbeOptions, endpoint string) (*Client, error) {
	caPEM, err := os.ReadFile(opts.CAPath) // #nosec G304 -- path comes from saved cluster state, not user input
	if err != nil {
		return nil, fmt.Errorf("read CA: %w", err)
	}
	certPEM, err := os.ReadFile(opts.ClientCertPath) // #nosec G304 -- saved cluster state
	if err != nil {
		return nil, fmt.Errorf("read client cert: %w", err)
	}
	keyPEM, err := os.ReadFile(opts.ClientKeyPath) // #nosec G304 -- saved cluster state
	if err != nil {
		return nil, fmt.Errorf("read client key: %w", err)
	}

	if _, _, err := net.SplitHostPort(endpoint); err != nil {
		port := opts.Port
		if port == 0 {
			port = 9443
		}
		endpoint = net.JoinHostPort(endpoint, strconv.Itoa(port))
	}

	return New(Options{
		Endpoint:      endpoint,
		CACertPEM:     caPEM,
		ClientCertPEM: certPEM,
		ClientKeyPEM:  keyPEM,
		Timeout:       opts.Timeout,
	})
}

// ProbeNodes calls /v1/info on every endpoint in parallel. Per-node failures
// are folded into NodeStatus.Error; the function only returns an error if it
// cannot read the cert files (a configuration problem affecting all nodes).
func ProbeNodes(ctx context.Context, opts ProbeOptions, endpoints map[string]string) ([]NodeStatus, error) {
	caPEM, err := os.ReadFile(opts.CAPath) // #nosec G304 -- path comes from saved cluster state, not user input
	if err != nil {
		return nil, fmt.Errorf("read CA: %w", err)
	}
	certPEM, err := os.ReadFile(opts.ClientCertPath) // #nosec G304 -- saved cluster state
	if err != nil {
		return nil, fmt.Errorf("read client cert: %w", err)
	}
	keyPEM, err := os.ReadFile(opts.ClientKeyPath) // #nosec G304 -- saved cluster state
	if err != nil {
		return nil, fmt.Errorf("read client key: %w", err)
	}

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	port := opts.Port
	if port == 0 {
		port = 9443
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results = make([]NodeStatus, 0, len(endpoints))
	)

	for name, ip := range endpoints {
		wg.Add(1)
		go func(name, ip string) {
			defer wg.Done()
			s := probeOne(ctx, name, ip, port, caPEM, certPEM, keyPEM, timeout)
			mu.Lock()
			results = append(results, s)
			mu.Unlock()
		}(name, ip)
	}
	wg.Wait()

	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	return results, nil
}

func probeOne(ctx context.Context, name, ip string, port int, caPEM, certPEM, keyPEM []byte, timeout time.Duration) NodeStatus {
	s := NodeStatus{Name: name, IP: ip}

	c, err := New(Options{
		Endpoint:      net.JoinHostPort(ip, strconv.Itoa(port)),
		CACertPEM:     caPEM,
		ClientCertPEM: certPEM,
		ClientKeyPEM:  keyPEM,
		Timeout:       timeout,
	})
	if err != nil {
		s.Error = err.Error()
		return s
	}

	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	info, err := c.Info(probeCtx)
	if err != nil {
		s.Error = err.Error()
		return s
	}
	s.Reachable = true
	s.Info = info
	return s
}
