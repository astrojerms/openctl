package handler

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/openctl/openctl/pkg/k3s/agent/certs"
	"github.com/openctl/openctl/pkg/protocol"
)

// startAgentStub mints a cert bundle for the named nodes, writes it to a
// per-cluster bundle dir under $HOME, and starts a single mTLS server that
// presents serverNode's cert and serves mux. It writes a cluster state file
// pointing every endpoint in endpoints at the stub's 127.0.0.1 port and
// returns the bundle dir. HOME must already be redirected via t.Setenv.
func startAgentStub(t *testing.T, clusterName, serverNode string, endpoints map[string]string, mux *http.ServeMux) {
	t.Helper()
	home, _ := os.UserHomeDir()
	bundleDir := filepath.Join(home, ".openctl", "state", "k3s", clusterName)
	stateDir := filepath.Join(home, ".openctl", "state", "k3s")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	ids := make([]certs.NodeIdentity, 0, len(endpoints))
	for name := range endpoints {
		ids = append(ids, certs.NodeIdentity{Name: name, IP: "127.0.0.1"})
	}
	bundle, err := certs.GenerateBundle(clusterName, ids)
	if err != nil {
		t.Fatalf("certs: %v", err)
	}
	if err := bundle.WriteTo(bundleDir); err != nil {
		t.Fatalf("write bundle: %v", err)
	}

	keypair, err := tls.X509KeyPair(bundle.ServerCerts[serverNode].CertPEM, bundle.ServerCerts[serverNode].KeyPEM)
	if err != nil {
		t.Fatalf("server keypair: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(bundle.CACertPEM)

	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{keypair},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	_, portStr, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)

	epLines := ""
	for name, ip := range endpoints {
		epLines += "        " + name + ": " + ip + "\n"
	}
	stateYAML := `apiVersion: k3s.openctl.io/v1
kind: Cluster
spec:
  compute:
    provider: proxmox
status:
  phase: Ready
  outputs:
    agent:
      caPath: ` + filepath.Join(bundleDir, "ca.pem") + `
      clientCertPath: ` + filepath.Join(bundleDir, "client.pem") + `
      clientKeyPath: ` + filepath.Join(bundleDir, "client.key") + `
      port: ` + strconv.Itoa(port) + `
      endpoints:
` + epLines
	if err := os.WriteFile(filepath.Join(stateDir, clusterName+".yaml"), []byte(stateYAML), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
}

func TestHandle_Logs_Success(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var gotLines string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/logs/k3s", func(w http.ResponseWriter, r *http.Request) {
		gotLines = r.URL.Query().Get("lines")
		_, _ = w.Write([]byte("line one\nline two\n"))
	})
	// Single node → no --node needed.
	startAgentStub(t, "logcl", "n1", map[string]string{"n1": "127.0.0.1"}, mux)

	h := New(&protocol.ProviderConfig{})
	resp, err := h.Handle(&protocol.Request{
		Version: protocol.ProtocolVersion,
		Action:  "logs",
		Args:    map[string]any{"cluster": "logcl", "lines": float64(50)}, // float64: JSON round-trip
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Status != protocol.StatusSuccess {
		t.Fatalf("status = %s (err=%v)", resp.Status, resp.Error)
	}
	if resp.Message != "line one\nline two\n" {
		t.Errorf("message = %q, want the log body", resp.Message)
	}
	if gotLines != "50" {
		t.Errorf("agent got lines=%q, want 50 (the --lines flag should pass through)", gotLines)
	}
}

func TestHandle_Logs_MultiNodeRequiresNode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/logs/k3s", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("logs"))
	})
	startAgentStub(t, "multi", "a", map[string]string{"a": "127.0.0.1", "b": "127.0.0.1"}, mux)

	h := New(&protocol.ProviderConfig{})
	resp, err := h.Handle(&protocol.Request{
		Version: protocol.ProtocolVersion,
		Action:  "logs",
		Args:    map[string]any{"cluster": "multi"}, // no --node, ambiguous
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Status != protocol.StatusError || resp.Error == nil {
		t.Fatalf("want error for ambiguous node, got %+v", resp)
	}
	if resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Errorf("code = %s, want %s", resp.Error.Code, protocol.ErrorCodeInvalidRequest)
	}
}

func TestHandle_Logs_NodeNotFound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/logs/k3s", func(w http.ResponseWriter, _ *http.Request) {})
	startAgentStub(t, "cl", "n1", map[string]string{"n1": "127.0.0.1"}, mux)

	h := New(&protocol.ProviderConfig{})
	resp, err := h.Handle(&protocol.Request{
		Version: protocol.ProtocolVersion,
		Action:  "logs",
		Args:    map[string]any{"cluster": "cl", "node": "ghost"},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Status != protocol.StatusError || resp.Error.Code != protocol.ErrorCodeNotFound {
		t.Fatalf("want NotFound for unknown node, got %+v", resp)
	}
}

func TestHandle_Logs_ClusterNotFound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	h := New(&protocol.ProviderConfig{})
	resp, err := h.Handle(&protocol.Request{
		Version: protocol.ProtocolVersion,
		Action:  "logs",
		Args:    map[string]any{"cluster": "nope"},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Status != protocol.StatusError || resp.Error.Code != protocol.ErrorCodeNotFound {
		t.Fatalf("want NotFound for unknown cluster, got %+v", resp)
	}
}

func TestHandle_Logs_MissingClusterArg(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	h := New(&protocol.ProviderConfig{})
	resp, err := h.Handle(&protocol.Request{
		Version: protocol.ProtocolVersion,
		Action:  "logs",
		Args:    map[string]any{},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Status != protocol.StatusError || resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("want InvalidRequest for missing cluster, got %+v", resp)
	}
}

func TestHandle_Logs_NoAgentBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	stateDir := filepath.Join(home, ".openctl", "state", "k3s")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Pre-agent cluster: state file with no outputs.agent block.
	stateYAML := "apiVersion: k3s.openctl.io/v1\nkind: Cluster\nstatus:\n  phase: Ready\n"
	if err := os.WriteFile(filepath.Join(stateDir, "old.yaml"), []byte(stateYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	h := New(&protocol.ProviderConfig{})
	resp, err := h.Handle(&protocol.Request{
		Version: protocol.ProtocolVersion,
		Action:  "logs",
		Args:    map[string]any{"cluster": "old"},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Status != protocol.StatusError || resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Fatalf("want InvalidRequest for cluster with no agent, got %+v", resp)
	}
}

func TestHandle_Restart_Success(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var hit string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/service/k3s/restart", func(w http.ResponseWriter, r *http.Request) {
		hit = r.Method
		w.WriteHeader(http.StatusNoContent)
	})
	startAgentStub(t, "rcl", "worker", map[string]string{"worker": "127.0.0.1"}, mux)

	h := New(&protocol.ProviderConfig{})
	resp, err := h.Handle(&protocol.Request{
		Version: protocol.ProtocolVersion,
		Action:  "restart",
		Args:    map[string]any{"cluster": "rcl", "node": "worker"},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Status != protocol.StatusSuccess {
		t.Fatalf("status = %s (err=%v)", resp.Status, resp.Error)
	}
	if hit != http.MethodPost {
		t.Errorf("agent restart endpoint hit with %q, want POST", hit)
	}
	if resp.Message == "" {
		t.Error("expected a confirmation message")
	}
}

func TestHandle_Restart_SelectsNamedNode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var hit bool
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/service/k3s/restart", func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusNoContent)
	})
	// Two endpoints share the stub's port; the stub presents "cp"'s cert, so
	// selecting "cp" must succeed (proves --node routes to the right endpoint).
	startAgentStub(t, "sel", "cp", map[string]string{"cp": "127.0.0.1", "wk": "127.0.0.1"}, mux)

	h := New(&protocol.ProviderConfig{})
	resp, err := h.Handle(&protocol.Request{
		Version: protocol.ProtocolVersion,
		Action:  "restart",
		Args:    map[string]any{"cluster": "sel", "node": "cp"},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Status != protocol.StatusSuccess {
		t.Fatalf("status = %s (err=%v)", resp.Status, resp.Error)
	}
	if !hit {
		t.Error("named node's agent was not called")
	}
}
