package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCUE_ValidVM(t *testing.T) {
	tmpDir := t.TempDir()
	cueFile := filepath.Join(tmpDir, "vm.cue")

	content := `
import "openctl.io/schemas/proxmox"

proxmox.#VirtualMachine & {
	metadata: name: "test-vm"
	spec: {
		node: "pve1"
		cpu: cores: 4
		memory: size: 8192
	}
}
`
	if err := os.WriteFile(cueFile, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write CUE file: %v", err)
	}

	resources, err := LoadCUE(cueFile)
	if err != nil {
		t.Fatalf("LoadCUE failed: %v", err)
	}

	if len(resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(resources))
	}

	r := resources[0]
	if r.APIVersion != "proxmox.openctl.io/v1" {
		t.Errorf("expected apiVersion=proxmox.openctl.io/v1, got %s", r.APIVersion)
	}

	if r.Kind != "VirtualMachine" {
		t.Errorf("expected kind=VirtualMachine, got %s", r.Kind)
	}

	if r.Metadata.Name != "test-vm" {
		t.Errorf("expected name=test-vm, got %s", r.Metadata.Name)
	}

	if r.Spec["node"] != "pve1" {
		t.Errorf("expected spec.node=pve1, got %v", r.Spec["node"])
	}
}

func TestLoadCUE_ValidCluster(t *testing.T) {
	tmpDir := t.TempDir()
	cueFile := filepath.Join(tmpDir, "cluster.cue")

	content := `
import "openctl.io/schemas/k3s"

k3s.#Cluster & {
	metadata: name: "dev"
	spec: {
		compute: {
			provider: "proxmox"
			image: url: "https://example.com/image.img"
		}
		nodes: {
			controlPlane: count: 1
		}
		ssh: {
			privateKeyPath: "~/.ssh/id_ed25519"
		}
	}
}
`
	if err := os.WriteFile(cueFile, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write CUE file: %v", err)
	}

	resources, err := LoadCUE(cueFile)
	if err != nil {
		t.Fatalf("LoadCUE failed: %v", err)
	}

	if len(resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(resources))
	}

	r := resources[0]
	if r.APIVersion != "k3s.openctl.io/v1" {
		t.Errorf("expected apiVersion=k3s.openctl.io/v1, got %s", r.APIVersion)
	}

	if r.Kind != "Cluster" {
		t.Errorf("expected kind=Cluster, got %s", r.Kind)
	}

	if r.Metadata.Name != "dev" {
		t.Errorf("expected name=dev, got %s", r.Metadata.Name)
	}
}

func TestLoadCUE_MultipleResources(t *testing.T) {
	tmpDir := t.TempDir()
	cueFile := filepath.Join(tmpDir, "multi.cue")

	content := `
import "openctl.io/schemas/proxmox"

vm1: proxmox.#VirtualMachine & {
	metadata: name: "vm-1"
	spec: {
		node: "pve1"
	}
}

vm2: proxmox.#VirtualMachine & {
	metadata: name: "vm-2"
	spec: {
		node: "pve2"
	}
}
`
	if err := os.WriteFile(cueFile, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write CUE file: %v", err)
	}

	resources, err := LoadCUE(cueFile)
	if err != nil {
		t.Fatalf("LoadCUE failed: %v", err)
	}

	if len(resources) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(resources))
	}
}

func TestLoadCUE_InvalidSchema(t *testing.T) {
	tmpDir := t.TempDir()
	cueFile := filepath.Join(tmpDir, "invalid.cue")

	// Missing required field 'node' in spec
	content := `
import "openctl.io/schemas/proxmox"

proxmox.#VirtualMachine & {
	metadata: name: "test-vm"
	spec: {
		cpu: cores: 4
	}
}
`
	if err := os.WriteFile(cueFile, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write CUE file: %v", err)
	}

	_, err := LoadCUE(cueFile)
	if err == nil {
		t.Error("expected validation error for missing required field")
	}
}

func TestLoadCUE_MissingName(t *testing.T) {
	tmpDir := t.TempDir()
	cueFile := filepath.Join(tmpDir, "noname.cue")

	content := `
import "openctl.io/schemas/proxmox"

proxmox.#VirtualMachine & {
	spec: {
		node: "pve1"
	}
}
`
	if err := os.WriteFile(cueFile, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write CUE file: %v", err)
	}

	_, err := LoadCUE(cueFile)
	if err == nil {
		t.Error("expected error for missing metadata.name")
	}
}

func TestLoadCUE_NonexistentFile(t *testing.T) {
	_, err := LoadCUE("/nonexistent/file.cue")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestLoadCUE_InvalidCUESyntax(t *testing.T) {
	tmpDir := t.TempDir()
	cueFile := filepath.Join(tmpDir, "syntax.cue")

	content := `
this is not valid CUE syntax {{{
`
	if err := os.WriteFile(cueFile, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write CUE file: %v", err)
	}

	_, err := LoadCUE(cueFile)
	if err == nil {
		t.Error("expected error for invalid CUE syntax")
	}
}
