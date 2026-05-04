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
