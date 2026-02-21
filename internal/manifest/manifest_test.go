package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParse(t *testing.T) {
	yaml := `
apiVersion: proxmox.openctl.io/v1
kind: VirtualMachine
metadata:
  name: test-vm
  labels:
    env: test
spec:
  node: pve1
  cpu:
    cores: 4
  memory:
    size: 8192
`
	resource, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if resource.APIVersion != "proxmox.openctl.io/v1" {
		t.Errorf("expected apiVersion=proxmox.openctl.io/v1, got %s", resource.APIVersion)
	}

	if resource.Kind != "VirtualMachine" {
		t.Errorf("expected kind=VirtualMachine, got %s", resource.Kind)
	}

	if resource.Metadata.Name != "test-vm" {
		t.Errorf("expected name=test-vm, got %s", resource.Metadata.Name)
	}

	if resource.Metadata.Labels["env"] != "test" {
		t.Errorf("expected label env=test, got %s", resource.Metadata.Labels["env"])
	}

	if resource.Spec["node"] != "pve1" {
		t.Errorf("expected spec.node=pve1, got %v", resource.Spec["node"])
	}
}

func TestParse_MissingAPIVersion(t *testing.T) {
	yaml := `
kind: VirtualMachine
metadata:
  name: test-vm
`
	_, err := Parse([]byte(yaml))
	if err == nil {
		t.Error("expected error for missing apiVersion")
	}
}

func TestParse_MissingKind(t *testing.T) {
	yaml := `
apiVersion: proxmox.openctl.io/v1
metadata:
  name: test-vm
`
	_, err := Parse([]byte(yaml))
	if err == nil {
		t.Error("expected error for missing kind")
	}
}

func TestParse_MissingName(t *testing.T) {
	yaml := `
apiVersion: proxmox.openctl.io/v1
kind: VirtualMachine
metadata:
  labels:
    env: test
`
	_, err := Parse([]byte(yaml))
	if err == nil {
		t.Error("expected error for missing metadata.name")
	}
}

func TestLoad(t *testing.T) {
	tmpDir := t.TempDir()
	manifestFile := filepath.Join(tmpDir, "vm.yaml")

	content := `
apiVersion: proxmox.openctl.io/v1
kind: VirtualMachine
metadata:
  name: file-test-vm
spec:
  node: pve1
`
	if err := os.WriteFile(manifestFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	resource, err := Load(manifestFile)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if resource.Metadata.Name != "file-test-vm" {
		t.Errorf("expected name=file-test-vm, got %s", resource.Metadata.Name)
	}
}

func TestLoad_NotExists(t *testing.T) {
	_, err := Load("/nonexistent/manifest.yaml")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestParseMultiple(t *testing.T) {
	yaml := `
apiVersion: proxmox.openctl.io/v1
kind: VirtualMachine
metadata:
  name: vm-1
spec:
  node: pve1
---
apiVersion: proxmox.openctl.io/v1
kind: VirtualMachine
metadata:
  name: vm-2
spec:
  node: pve2
---
apiVersion: proxmox.openctl.io/v1
kind: VirtualMachine
metadata:
  name: vm-3
spec:
  node: pve1
`
	resources, err := ParseMultiple([]byte(yaml))
	if err != nil {
		t.Fatalf("ParseMultiple failed: %v", err)
	}

	if len(resources) != 3 {
		t.Errorf("expected 3 resources, got %d", len(resources))
	}

	expectedNames := []string{"vm-1", "vm-2", "vm-3"}
	for i, r := range resources {
		if r.Metadata.Name != expectedNames[i] {
			t.Errorf("expected name=%s, got %s", expectedNames[i], r.Metadata.Name)
		}
	}
}

func TestExtractProvider(t *testing.T) {
	tests := []struct {
		apiVersion string
		expected   string
	}{
		{"proxmox.openctl.io/v1", "proxmox"},
		{"aws.openctl.io/v1", "aws"},
		{"kubernetes.openctl.io/v1beta1", "kubernetes"},
		{"simple/v1", "simple/v1"}, // No dot, returns full string
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.apiVersion, func(t *testing.T) {
			result := ExtractProvider(tt.apiVersion)
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestParse_ComplexSpec(t *testing.T) {
	yaml := `
apiVersion: proxmox.openctl.io/v1
kind: VirtualMachine
metadata:
  name: complex-vm
spec:
  node: pve1
  template:
    name: ubuntu-22.04
  cpu:
    cores: 4
    sockets: 1
  memory:
    size: 8192
  disks:
    - name: scsi0
      storage: local-lvm
      size: 50G
  networks:
    - name: net0
      bridge: vmbr0
  cloudInit:
    user: ubuntu
    sshKeys:
      - ssh-ed25519 AAAA...
    ipConfig:
      net0:
        ip: dhcp
  startOnCreate: true
`
	resource, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// Verify nested structures
	template, ok := resource.Spec["template"].(map[string]any)
	if !ok {
		t.Fatal("expected template to be a map")
	}
	if template["name"] != "ubuntu-22.04" {
		t.Errorf("expected template.name=ubuntu-22.04, got %v", template["name"])
	}

	disks, ok := resource.Spec["disks"].([]any)
	if !ok {
		t.Fatal("expected disks to be a slice")
	}
	if len(disks) != 1 {
		t.Errorf("expected 1 disk, got %d", len(disks))
	}

	cloudInit, ok := resource.Spec["cloudInit"].(map[string]any)
	if !ok {
		t.Fatal("expected cloudInit to be a map")
	}
	if cloudInit["user"] != "ubuntu" {
		t.Errorf("expected cloudInit.user=ubuntu, got %v", cloudInit["user"])
	}
}
