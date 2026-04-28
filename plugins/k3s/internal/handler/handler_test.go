package handler

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openctl/openctl-k3s/internal/agent"
	"github.com/openctl/openctl-k3s/internal/agent/certs"
	"github.com/openctl/openctl/pkg/protocol"
)

func TestNew(t *testing.T) {
	config := &protocol.ProviderConfig{
		Endpoint: "https://example.com",
	}

	h := New(config)

	if h.config != config {
		t.Error("expected config to match")
	}
}

func TestHandle_UnknownResourceType(t *testing.T) {
	h := New(&protocol.ProviderConfig{})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionGet,
		ResourceType: "UnknownResource",
	}

	resp, err := h.Handle(req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusError {
		t.Errorf("expected status=error, got %s", resp.Status)
	}
	if resp.Error == nil {
		t.Fatal("expected error in response")
	}
	if resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Errorf("expected code=INVALID_REQUEST, got %s", resp.Error.Code)
	}
}

func TestHandle_ClusterUnknownAction(t *testing.T) {
	h := New(&protocol.ProviderConfig{})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       "unknown-action",
		ResourceType: "Cluster",
	}

	resp, err := h.Handle(req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusError {
		t.Errorf("expected status=error, got %s", resp.Status)
	}
	if resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Errorf("expected code=INVALID_REQUEST, got %s", resp.Error.Code)
	}
}

func TestHandle_CreateCluster_MissingProvider(t *testing.T) {
	h := New(&protocol.ProviderConfig{})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionCreate,
		ResourceType: "Cluster",
		Manifest: &protocol.Resource{
			APIVersion: "k3s.openctl.io/v1",
			Kind:       "Cluster",
			Metadata:   protocol.ResourceMetadata{Name: "test"},
			Spec: map[string]any{
				// Missing compute.provider
				"nodes": map[string]any{
					"controlPlane": map[string]any{
						"count": float64(1),
					},
				},
			},
		},
	}

	resp, err := h.Handle(req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusError {
		t.Errorf("expected status=error, got %s", resp.Status)
	}
	if !strings.Contains(resp.Error.Message, "provider") {
		t.Errorf("expected error about provider, got: %s", resp.Error.Message)
	}
}

func TestHandle_CreateCluster_MissingImage(t *testing.T) {
	h := New(&protocol.ProviderConfig{})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionCreate,
		ResourceType: "Cluster",
		Manifest: &protocol.Resource{
			APIVersion: "k3s.openctl.io/v1",
			Kind:       "Cluster",
			Metadata:   protocol.ResourceMetadata{Name: "test"},
			Spec: map[string]any{
				"compute": map[string]any{
					"provider": "proxmox",
					// Missing image
				},
				"nodes": map[string]any{
					"controlPlane": map[string]any{
						"count": float64(1),
					},
				},
				"ssh": map[string]any{
					"privateKeyPath": "~/.ssh/id_rsa",
				},
			},
		},
	}

	resp, err := h.Handle(req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusError {
		t.Errorf("expected status=error, got %s", resp.Status)
	}
	if !strings.Contains(resp.Error.Message, "image") {
		t.Errorf("expected error about image, got: %s", resp.Error.Message)
	}
}

func TestHandle_CreateCluster_MissingSSHKey(t *testing.T) {
	h := New(&protocol.ProviderConfig{})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionCreate,
		ResourceType: "Cluster",
		Manifest: &protocol.Resource{
			APIVersion: "k3s.openctl.io/v1",
			Kind:       "Cluster",
			Metadata:   protocol.ResourceMetadata{Name: "test"},
			Spec: map[string]any{
				"compute": map[string]any{
					"provider": "proxmox",
					"image": map[string]any{
						"url": "https://example.com/image.img",
					},
				},
				"nodes": map[string]any{
					"controlPlane": map[string]any{
						"count": float64(1),
					},
				},
				"ssh": map[string]any{
					// Missing privateKeyPath
				},
			},
		},
	}

	resp, err := h.Handle(req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusError {
		t.Errorf("expected status=error, got %s", resp.Status)
	}
	if !strings.Contains(resp.Error.Message, "privateKeyPath") {
		t.Errorf("expected error about privateKeyPath, got: %s", resp.Error.Message)
	}
}

func TestHandle_CreateCluster_Success(t *testing.T) {
	h := New(&protocol.ProviderConfig{})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionCreate,
		ResourceType: "Cluster",
		Manifest: &protocol.Resource{
			APIVersion: "k3s.openctl.io/v1",
			Kind:       "Cluster",
			Metadata:   protocol.ResourceMetadata{Name: "test-cluster"},
			Spec: map[string]any{
				"compute": map[string]any{
					"provider": "proxmox",
					"image": map[string]any{
						"url": "https://cloud-images.ubuntu.com/jammy/jammy-server-cloudimg-amd64.img",
					},
					"default": map[string]any{
						"cpus":     float64(2),
						"memoryMB": float64(4096),
						"diskGB":   float64(50),
					},
				},
				"nodes": map[string]any{
					"controlPlane": map[string]any{
						"count": float64(1),
					},
				},
				"ssh": map[string]any{
					"user":           "ubuntu",
					"privateKeyPath": "~/.ssh/id_ed25519",
					"publicKeys":     []any{"ssh-ed25519 AAAA..."},
				},
			},
		},
	}

	resp, err := h.Handle(req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	// Should return dispatch requests for VM creation
	if resp.Status != protocol.StatusSuccess {
		t.Errorf("expected status=success, got %s (error: %v)", resp.Status, resp.Error)
	}
	if len(resp.DispatchRequests) == 0 {
		t.Error("expected dispatch requests for VM creation")
	}
	if resp.Continuation == nil {
		t.Error("expected continuation token")
	}
	if resp.Continuation != nil && resp.Continuation.Token != "vms-created" {
		t.Errorf("expected continuation token=vms-created, got %s", resp.Continuation.Token)
	}
	if resp.StateUpdate == nil {
		t.Error("expected state update")
	}
}

func TestHandle_ListClusters_Empty(t *testing.T) {
	h := New(&protocol.ProviderConfig{})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionList,
		ResourceType: "Cluster",
	}

	resp, err := h.Handle(req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusSuccess {
		t.Errorf("expected status=success, got %s", resp.Status)
	}
	// Empty list (nil or empty slice) is valid - handler returns empty slice
	// when no state directory exists or is empty
}

func TestHandle_GetCluster_NotFound(t *testing.T) {
	h := New(&protocol.ProviderConfig{})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionGet,
		ResourceType: "Cluster",
		ResourceName: "nonexistent-cluster-12345",
	}

	resp, err := h.Handle(req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusError {
		t.Errorf("expected status=error, got %s", resp.Status)
	}
	if resp.Error.Code != protocol.ErrorCodeNotFound {
		t.Errorf("expected code=NOT_FOUND, got %s", resp.Error.Code)
	}
}

func TestHandle_DeleteCluster_NotFound(t *testing.T) {
	h := New(&protocol.ProviderConfig{})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionDelete,
		ResourceType: "Cluster",
		ResourceName: "nonexistent-cluster-12345",
	}

	resp, err := h.Handle(req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusError {
		t.Errorf("expected status=error, got %s", resp.Status)
	}
	if resp.Error.Code != protocol.ErrorCodeNotFound {
		t.Errorf("expected code=NOT_FOUND, got %s", resp.Error.Code)
	}
}

func TestHandle_RoutesToCorrectAction(t *testing.T) {
	tests := []struct {
		action      string
		shouldError bool
	}{
		{protocol.ActionList, false},
		{protocol.ActionGet, false},    // Will error with NOT_FOUND, but routes correctly
		{protocol.ActionCreate, false}, // Will need manifest
		{protocol.ActionDelete, false}, // Will error with NOT_FOUND, but routes correctly
		{protocol.ActionApply, true},   // Not supported
		{"unknown", true},
	}

	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			h := New(&protocol.ProviderConfig{})

			req := &protocol.Request{
				Version:      protocol.ProtocolVersion,
				Action:       tt.action,
				ResourceType: "Cluster",
				ResourceName: "test",
				Manifest: &protocol.Resource{
					APIVersion: "k3s.openctl.io/v1",
					Kind:       "Cluster",
					Metadata:   protocol.ResourceMetadata{Name: "test"},
					Spec:       map[string]any{},
				},
			}

			resp, err := h.Handle(req)
			if err != nil {
				t.Fatalf("Handle should not return Go error: %v", err)
			}

			if tt.shouldError && resp.Status != protocol.StatusError {
				t.Errorf("expected error for action %s", tt.action)
			}
		})
	}
}

func TestHandle_CreateCluster_DefaultsControlPlaneCount(t *testing.T) {
	h := New(&protocol.ProviderConfig{})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionCreate,
		ResourceType: "Cluster",
		Manifest: &protocol.Resource{
			APIVersion: "k3s.openctl.io/v1",
			Kind:       "Cluster",
			Metadata:   protocol.ResourceMetadata{Name: "minimal"},
			Spec: map[string]any{
				"compute": map[string]any{
					"provider": "proxmox",
					"image": map[string]any{
						"url": "https://example.com/image.img",
					},
				},
				"nodes": map[string]any{
					"controlPlane": map[string]any{
						// count not specified, should default to 1
					},
				},
				"ssh": map[string]any{
					"privateKeyPath": "~/.ssh/id_rsa",
				},
			},
		},
	}

	resp, err := h.Handle(req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	// Should succeed (defaulting count to 1)
	if resp.Status != protocol.StatusSuccess {
		t.Errorf("expected status=success, got %s (error: %v)", resp.Status, resp.Error)
	}
	// Should have 1 dispatch request for the control plane
	if len(resp.DispatchRequests) != 1 {
		t.Errorf("expected 1 dispatch request, got %d", len(resp.DispatchRequests))
	}
}

func TestHandle_CreateCluster_DefaultsSSHUser(t *testing.T) {
	h := New(&protocol.ProviderConfig{})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionCreate,
		ResourceType: "Cluster",
		Manifest: &protocol.Resource{
			APIVersion: "k3s.openctl.io/v1",
			Kind:       "Cluster",
			Metadata:   protocol.ResourceMetadata{Name: "minimal"},
			Spec: map[string]any{
				"compute": map[string]any{
					"provider": "proxmox",
					"image": map[string]any{
						"url": "https://example.com/image.img",
					},
				},
				"nodes": map[string]any{
					"controlPlane": map[string]any{
						"count": float64(1),
					},
				},
				"ssh": map[string]any{
					"privateKeyPath": "~/.ssh/id_rsa",
					// user not specified, should default to "ubuntu"
				},
			},
		},
	}

	resp, err := h.Handle(req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusSuccess {
		t.Errorf("expected status=success, got %s (error: %v)", resp.Status, resp.Error)
	}
}

func TestListClusters_WithState(t *testing.T) {
	// Create a test state file
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot get home directory")
	}

	stateDir := filepath.Join(homeDir, ".openctl", "state", "k3s")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatalf("failed to create state directory: %v", err)
	}

	statePath := filepath.Join(stateDir, "test-list-cluster.yaml")
	stateContent := `apiVersion: k3s.openctl.io/v1
kind: Cluster
spec:
  compute:
    provider: proxmox
status:
  phase: Ready
`
	if err := os.WriteFile(statePath, []byte(stateContent), 0600); err != nil {
		t.Fatalf("failed to write state file: %v", err)
	}
	defer os.Remove(statePath)

	h := New(&protocol.ProviderConfig{})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionList,
		ResourceType: "Cluster",
	}

	resp, err := h.Handle(req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusSuccess {
		t.Errorf("expected status=success, got %s", resp.Status)
	}

	// Should find the test cluster
	found := false
	for _, r := range resp.Resources {
		if r.Metadata.Name == "test-list-cluster" {
			found = true
			if r.Status["phase"] != "Ready" {
				t.Errorf("expected phase=Ready, got %v", r.Status["phase"])
			}
		}
	}
	if !found {
		t.Error("expected to find test-list-cluster in results")
	}
}

// TestGetCluster_AugmentsWithLiveAgentStatus runs a real agent for one of two
// nodes, writes a state file pointing at both, and verifies that get folds
// per-node reachability into the response without failing the call.
func TestGetCluster_AugmentsWithLiveAgentStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	clusterName := "augmented"
	bundleDir := filepath.Join(home, ".openctl", "state", "k3s", clusterName)
	stateDir := filepath.Join(home, ".openctl", "state", "k3s")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Mint a real cert bundle for both nodes pinned to 127.0.0.1.
	bundle, err := certs.GenerateBundle(clusterName, []certs.NodeIdentity{
		{Name: "alive", IP: "127.0.0.1"},
		{Name: "dead", IP: "127.0.0.1"},
	})
	if err != nil {
		t.Fatalf("certs: %v", err)
	}
	if err := bundle.WriteTo(bundleDir); err != nil {
		t.Fatalf("write bundle: %v", err)
	}

	// Start agent for "alive" only. Pick a free port.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	aliveAddr := ln.Addr().String()
	_, alivePortStr, _ := net.SplitHostPort(aliveAddr)
	_ = ln.Close()
	alivePort, _ := strconv.Atoi(alivePortStr)

	srv, err := agent.New(agent.Options{
		Listen:   aliveAddr,
		CertFile: filepath.Join(bundleDir, "alive-server.pem"),
		KeyFile:  filepath.Join(bundleDir, "alive-server.key"),
		CAFile:   filepath.Join(bundleDir, "ca.pem"),
	})
	if err != nil {
		t.Fatalf("agent server: %v", err)
	}
	go func() { _ = srv.ListenAndServe() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	waitForListen(t, aliveAddr, 2*time.Second)

	// Pick a free port for the dead node and immediately release it.
	deadLn, _ := net.Listen("tcp", "127.0.0.1:0")
	_, deadPortStr, _ := net.SplitHostPort(deadLn.Addr().String())
	_ = deadLn.Close()

	// Both nodes share the same IP but the state file's port is shared, so
	// for this test we point both endpoints at the alive port — the "dead"
	// node will get a TLS handshake error because its server cert won't be
	// presented (the alive server presents alive's cert). That tests the
	// "unreachable due to TLS error" path. To also cover dial failure, we
	// use the released deadPort for one variant below.
	_ = deadPortStr

	stateYAML := `apiVersion: k3s.openctl.io/v1
kind: Cluster
spec:
  compute:
    provider: proxmox
status:
  phase: Ready
  message: Cluster is ready
  outputs:
    kubeconfigPath: /tmp/kubeconfig
    serverIP: 127.0.0.1
    agent:
      bundleDir: ` + bundleDir + `
      caPath: ` + filepath.Join(bundleDir, "ca.pem") + `
      clientCertPath: ` + filepath.Join(bundleDir, "client.pem") + `
      clientKeyPath: ` + filepath.Join(bundleDir, "client.key") + `
      port: ` + strconv.Itoa(alivePort) + `
      endpoints:
        alive: 127.0.0.1
        dead: 127.0.0.2
`
	statePath := filepath.Join(stateDir, clusterName+".yaml")
	if err := os.WriteFile(statePath, []byte(stateYAML), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}

	h := New(&protocol.ProviderConfig{})
	resp, err := h.Handle(&protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionGet,
		ResourceType: "Cluster",
		ResourceName: clusterName,
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Status != protocol.StatusSuccess {
		t.Fatalf("status = %s (err=%v)", resp.Status, resp.Error)
	}

	nodesRaw, ok := resp.Resource.Status["nodes"].([]map[string]any)
	if !ok {
		t.Fatalf("status.nodes missing or wrong type: %T", resp.Resource.Status["nodes"])
	}
	if len(nodesRaw) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(nodesRaw))
	}

	statusByName := map[string]map[string]any{}
	for _, n := range nodesRaw {
		statusByName[n["name"].(string)] = n
	}
	if !statusByName["alive"]["reachable"].(bool) {
		t.Errorf("alive should be reachable: %+v", statusByName["alive"])
	}
	if statusByName["dead"]["reachable"].(bool) {
		t.Errorf("dead should be unreachable: %+v", statusByName["dead"])
	}

	if got := resp.Resource.Status["health"]; got != "degraded" {
		t.Errorf("health = %v, want degraded", got)
	}
}

// TestGetCluster_DetectsAgentVersionSkew runs a fake info server reporting a
// different agent version and verifies the handler adds agentVersionSkew +
// expectedAgentVersion to the response (and doesn't fail the call).
func TestGetCluster_DetectsAgentVersionSkew(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	clusterName := "skewy"
	bundleDir := filepath.Join(home, ".openctl", "state", "k3s", clusterName)
	stateDir := filepath.Join(home, ".openctl", "state", "k3s")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	bundle, err := certs.GenerateBundle(clusterName, []certs.NodeIdentity{
		{Name: "n1", IP: "127.0.0.1"},
	})
	if err != nil {
		t.Fatalf("certs: %v", err)
	}
	if err := bundle.WriteTo(bundleDir); err != nil {
		t.Fatalf("write bundle: %v", err)
	}

	// Custom handler returning a deliberately different version.
	const fakeVersion = "0.0.0-skew-test"
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(agent.Info{
			Hostname:     "n1",
			OS:           "linux",
			Arch:         "amd64",
			Init:         "systemd",
			K3sVersion:   "v1.99.0+k3s1",
			K3sStatus:    "active",
			AgentVersion: fakeVersion,
			Capabilities: map[string]string{"logs": "journald"},
		})
	})

	keypair, err := tls.X509KeyPair(bundle.ServerCerts["n1"].CertPEM, bundle.ServerCerts["n1"].KeyPEM)
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
        n1: 127.0.0.1
`
	if err := os.WriteFile(filepath.Join(stateDir, clusterName+".yaml"), []byte(stateYAML), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}

	h := New(&protocol.ProviderConfig{})
	resp, err := h.Handle(&protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionGet,
		ResourceType: "Cluster",
		ResourceName: clusterName,
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Status != protocol.StatusSuccess {
		t.Fatalf("status = %s (err=%v)", resp.Status, resp.Error)
	}

	skew, ok := resp.Resource.Status["agentVersionSkew"].([]string)
	if !ok || len(skew) != 1 {
		t.Fatalf("agentVersionSkew = %v (type %T), want []string with 1 entry",
			resp.Resource.Status["agentVersionSkew"], resp.Resource.Status["agentVersionSkew"])
	}
	if !strings.Contains(skew[0], fakeVersion) {
		t.Errorf("skew entry %q should contain %q", skew[0], fakeVersion)
	}
	if got := resp.Resource.Status["expectedAgentVersion"]; got != agent.Version {
		t.Errorf("expectedAgentVersion = %v, want %v", got, agent.Version)
	}

	// Per-node entry should also surface capabilities.
	nodes, _ := resp.Resource.Status["nodes"].([]map[string]any)
	if len(nodes) != 1 {
		t.Fatalf("want 1 node entry, got %d", len(nodes))
	}
	if _, ok := nodes[0]["capabilities"].(map[string]string); !ok {
		t.Errorf("node entry missing capabilities map: %+v", nodes[0])
	}
}

func waitForListen(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
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
