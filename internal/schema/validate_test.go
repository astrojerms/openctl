package schema

import (
	"strings"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

func TestValidatePassesValidVMManifest(t *testing.T) {
	r := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata: protocol.ResourceMetadata{
			Name: "web-01",
		},
		Spec: map[string]any{
			"node": "pve1",
			"template": map[string]any{
				"name": "ubuntu-22.04-cloudinit",
			},
			"cpu":    map[string]any{"cores": 2},
			"memory": map[string]any{"size": 4096},
		},
	}
	if err := Validate(r); err != nil {
		t.Errorf("want nil, got: %v", err)
	}
}

func TestValidateRejectsTooLittleMemory(t *testing.T) {
	r := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata:   protocol.ResourceMetadata{Name: "tiny"},
		Spec: map[string]any{
			"node":   "pve1",
			"cpu":    map[string]any{"cores": 2},
			"memory": map[string]any{"size": 64}, // schema requires >=512
		},
	}
	err := Validate(r)
	if err == nil {
		t.Fatal("want validation error, got nil")
	}
	if !strings.Contains(err.Error(), "does not match schema") {
		t.Errorf("error should mention schema mismatch: %v", err)
	}
}

func TestValidatePassesUnknownAPIVersion(t *testing.T) {
	// Unknown providers/kinds should pass through (no embedded schema).
	r := &protocol.Resource{
		APIVersion: "aws.openctl.io/v1",
		Kind:       "EC2Instance",
		Metadata:   protocol.ResourceMetadata{Name: "i-1234"},
		Spec:       map[string]any{},
	}
	if err := Validate(r); err != nil {
		t.Errorf("want nil for unknown provider, got: %v", err)
	}
}

func TestValidateRejectsMissingAPIVersion(t *testing.T) {
	r := &protocol.Resource{
		Kind:     "VirtualMachine",
		Metadata: protocol.ResourceMetadata{Name: "x"},
	}
	if err := Validate(r); err == nil {
		t.Error("want error for missing apiVersion")
	}
}

func TestValidateRejectsNil(t *testing.T) {
	if err := Validate(nil); err == nil {
		t.Error("want error for nil resource")
	}
}

// TestValidatePassesClusterWithNodePlacement proves the k3s Cluster CUE
// schema accepts the placement host lists at all three levels
// (compute-wide, control-plane, worker pool).
func TestValidatePassesClusterWithNodePlacement(t *testing.T) {
	r := &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       "Cluster",
		Metadata:   protocol.ResourceMetadata{Name: "ha"},
		Spec: map[string]any{
			"compute": map[string]any{
				"provider": "proxmox",
				"image":    map[string]any{"template": "ubuntu-2204"},
				"nodes":    []any{"pve1", "pve2", "pve3"},
			},
			"nodes": map[string]any{
				"controlPlane": map[string]any{
					"count": 3,
					"nodes": []any{"pve1", "pve2", "pve3"},
				},
				"workers": []any{
					map[string]any{
						"name":  "gpu",
						"count": 1,
						"nodes": []any{"pve3"},
					},
				},
			},
			"ssh": map[string]any{"privateKeyPath": "/root/.ssh/id_ed25519"},
		},
	}
	if err := Validate(r); err != nil {
		t.Errorf("want nil for cluster with node placement, got: %v", err)
	}
}
