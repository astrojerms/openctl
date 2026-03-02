package handler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
