package client

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openctl/openctl-k3s/internal/agent"
	"github.com/openctl/openctl-k3s/internal/agent/certs"
)

// TestClientInfoAgainstRealServer wires the typed client against a real
// agent.Server to confirm the mTLS handshake works end-to-end.
func TestClientInfoAgainstRealServer(t *testing.T) {
	bundle, err := certs.GenerateBundle("test", []certs.NodeIdentity{
		{Name: "node1", IP: "127.0.0.1"},
	})
	if err != nil {
		t.Fatalf("certs: %v", err)
	}

	dir := t.TempDir()
	caPath := writeFile(t, dir, "ca.pem", bundle.CACertPEM)
	server := bundle.ServerCerts["node1"]
	serverCertPath := writeFile(t, dir, "server.pem", server.CertPEM)
	serverKeyPath := writeFile(t, dir, "server.key", server.KeyPEM)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	srv, err := agent.New(agent.Options{
		Listen:   addr,
		CertFile: serverCertPath,
		KeyFile:  serverKeyPath,
		CAFile:   caPath,
	})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	go func() { _ = srv.ListenAndServe() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	waitForPort(t, addr, 2*time.Second)

	c, err := New(Options{
		Endpoint:      addr,
		CACertPEM:     bundle.CACertPEM,
		ClientCertPEM: bundle.ClientCertPEM,
		ClientKeyPEM:  bundle.ClientKeyPEM,
		Timeout:       5 * time.Second,
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := c.Info(ctx)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.AgentVersion != agent.Version {
		t.Errorf("AgentVersion = %q, want %q", info.AgentVersion, agent.Version)
	}
}

// TestClientLogsAgainstRealServer drives the typed Logs() through a real
// agent.Server. The agent's real fetcher will fail (no k3s on the test host),
// so we expect a non-2xx surfaced as an error. This validates the client's
// HTTP plumbing + error path end-to-end with mTLS.
func TestClientLogsAgainstRealServer(t *testing.T) {
	bundle, err := certs.GenerateBundle("logs-test", []certs.NodeIdentity{
		{Name: "n1", IP: "127.0.0.1"},
	})
	if err != nil {
		t.Fatalf("certs: %v", err)
	}
	dir := t.TempDir()
	caPath := writeFile(t, dir, "ca.pem", bundle.CACertPEM)
	server := bundle.ServerCerts["n1"]
	serverCertPath := writeFile(t, dir, "server.pem", server.CertPEM)
	serverKeyPath := writeFile(t, dir, "server.key", server.KeyPEM)

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()

	srv, err := agent.New(agent.Options{
		Listen:   addr,
		CertFile: serverCertPath,
		KeyFile:  serverKeyPath,
		CAFile:   caPath,
	})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	go func() { _ = srv.ListenAndServe() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	waitForPort(t, addr, 2*time.Second)

	c, err := New(Options{
		Endpoint:      addr,
		CACertPEM:     bundle.CACertPEM,
		ClientCertPEM: bundle.ClientCertPEM,
		ClientKeyPEM:  bundle.ClientKeyPEM,
		Timeout:       5 * time.Second,
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = c.Logs(ctx, 50)

	// On macOS dev hosts (no journald, no k3s), the fetcher returns an error
	// and the handler emits 500. Either an error or a successful return is
	// acceptable — what we're really validating is that the HTTP call went
	// through, mTLS handshook, and the request was routed to the logs
	// handler. A nil-error case can happen in CI on Linux with k3s installed.
	if err != nil && !strings.Contains(err.Error(), "logs: status") {
		t.Errorf("Logs error should be wrapped from server status, got: %v", err)
	}
}

// TestClientServiceActionsAgainstRealServer validates that StartK3s,
// StopK3s, and RestartK3s reach the right paths via mTLS. On a dev host
// without a real k3s service, the agent's controller errors out and the
// client wraps the 500 — that's fine; what we're checking is wire-level
// reachability + path correctness.
func TestClientServiceActionsAgainstRealServer(t *testing.T) {
	bundle, err := certs.GenerateBundle("svc-test", []certs.NodeIdentity{
		{Name: "n1", IP: "127.0.0.1"},
	})
	if err != nil {
		t.Fatalf("certs: %v", err)
	}
	dir := t.TempDir()
	caPath := writeFile(t, dir, "ca.pem", bundle.CACertPEM)
	server := bundle.ServerCerts["n1"]
	serverCertPath := writeFile(t, dir, "server.pem", server.CertPEM)
	serverKeyPath := writeFile(t, dir, "server.key", server.KeyPEM)

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()

	srv, err := agent.New(agent.Options{
		Listen:   addr,
		CertFile: serverCertPath,
		KeyFile:  serverKeyPath,
		CAFile:   caPath,
	})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	go func() { _ = srv.ListenAndServe() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	waitForPort(t, addr, 2*time.Second)

	c, err := New(Options{
		Endpoint:      addr,
		CACertPEM:     bundle.CACertPEM,
		ClientCertPEM: bundle.ClientCertPEM,
		ClientKeyPEM:  bundle.ClientKeyPEM,
		Timeout:       5 * time.Second,
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	for name, fn := range map[string]func(context.Context) error{
		"start":   c.StartK3s,
		"stop":    c.StopK3s,
		"restart": c.RestartK3s,
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := fn(ctx)
		cancel()
		if err != nil && !strings.Contains(err.Error(), "service "+name) {
			t.Errorf("%s: error should be wrapped as 'service %s', got: %v", name, name, err)
		}
	}
}

func TestNewRejectsBadCA(t *testing.T) {
	_, err := New(Options{
		Endpoint:      "127.0.0.1:9443",
		CACertPEM:     []byte("not a PEM"),
		ClientCertPEM: []byte("nope"),
		ClientKeyPEM:  []byte("nope"),
	})
	if err == nil {
		t.Fatal("want error for bad CA")
	}
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func waitForPort(t *testing.T, addr string, _ time.Duration) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", addr)
}
