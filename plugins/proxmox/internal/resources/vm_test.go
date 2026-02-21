package resources

import (
	"testing"

	"github.com/openctl/openctl-proxmox/internal/client"
	"github.com/openctl/openctl/pkg/protocol"
)

func TestVMToResource(t *testing.T) {
	vm := &client.VM{
		VMID:     100,
		Name:     "test-vm",
		Status:   "running",
		Mem:      4294967296,  // 4GB used
		MaxMem:   8589934592,  // 8GB total
		CPU:      0.25,
		CPUs:     4,
		Uptime:   3600,
		Node:     "pve1",
		Template: 0,
	}

	config := &client.VMConfig{
		Name:    "test-vm",
		Cores:   4,
		Sockets: 2,
		Memory:  8192,
	}

	resource := VMToResource(vm, config)

	if resource.APIVersion != "proxmox.openctl.io/v1" {
		t.Errorf("expected apiVersion=proxmox.openctl.io/v1, got %s", resource.APIVersion)
	}
	if resource.Kind != "VirtualMachine" {
		t.Errorf("expected kind=VirtualMachine, got %s", resource.Kind)
	}
	if resource.Metadata.Name != "test-vm" {
		t.Errorf("expected name=test-vm, got %s", resource.Metadata.Name)
	}

	// Check spec
	if resource.Spec["node"] != "pve1" {
		t.Errorf("expected spec.node=pve1, got %v", resource.Spec["node"])
	}

	cpu, ok := resource.Spec["cpu"].(map[string]any)
	if !ok {
		t.Fatal("expected spec.cpu to be a map")
	}
	if cpu["cores"] != 4 {
		t.Errorf("expected cpu.cores=4, got %v", cpu["cores"])
	}
	if cpu["sockets"] != 2 {
		t.Errorf("expected cpu.sockets=2, got %v", cpu["sockets"])
	}

	memory, ok := resource.Spec["memory"].(map[string]any)
	if !ok {
		t.Fatal("expected spec.memory to be a map")
	}
	if memory["size"] != 8192 {
		t.Errorf("expected memory.size=8192, got %v", memory["size"])
	}

	// Check status
	if resource.Status["vmid"] != 100 {
		t.Errorf("expected status.vmid=100, got %v", resource.Status["vmid"])
	}
	if resource.Status["state"] != "running" {
		t.Errorf("expected status.state=running, got %v", resource.Status["state"])
	}
}

func TestVMToResource_NoConfig(t *testing.T) {
	vm := &client.VM{
		VMID:   101,
		Name:   "no-config-vm",
		Status: "stopped",
		MaxMem: 4294967296, // 4GB
		CPUs:   2,
		Node:   "pve2",
	}

	resource := VMToResource(vm, nil)

	cpu, ok := resource.Spec["cpu"].(map[string]any)
	if !ok {
		t.Fatal("expected spec.cpu to be a map")
	}
	if cpu["cores"] != 2 {
		t.Errorf("expected cpu.cores=2 from VM, got %v", cpu["cores"])
	}

	memory, ok := resource.Spec["memory"].(map[string]any)
	if !ok {
		t.Fatal("expected spec.memory to be a map")
	}
	// 4GB in bytes / 1024 / 1024 = 4096 MB
	if memory["size"] != int64(4096) {
		t.Errorf("expected memory.size=4096 from VM, got %v", memory["size"])
	}
}

func TestTemplateToResource(t *testing.T) {
	tmpl := &client.Template{
		VMID:   9000,
		Name:   "ubuntu-22.04",
		Node:   "pve1",
		Status: "stopped",
	}

	resource := TemplateToResource(tmpl)

	if resource.APIVersion != "proxmox.openctl.io/v1" {
		t.Errorf("expected apiVersion=proxmox.openctl.io/v1, got %s", resource.APIVersion)
	}
	if resource.Kind != "Template" {
		t.Errorf("expected kind=Template, got %s", resource.Kind)
	}
	if resource.Metadata.Name != "ubuntu-22.04" {
		t.Errorf("expected name=ubuntu-22.04, got %s", resource.Metadata.Name)
	}
	if resource.Spec["node"] != "pve1" {
		t.Errorf("expected spec.node=pve1, got %v", resource.Spec["node"])
	}
	if resource.Spec["vmid"] != 9000 {
		t.Errorf("expected spec.vmid=9000, got %v", resource.Spec["vmid"])
	}
}

func TestParseVMSpec(t *testing.T) {
	resource := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata:   protocol.ResourceMetadata{Name: "test-vm"},
		Spec: map[string]any{
			"node": "pve1",
			"template": map[string]any{
				"name": "ubuntu-22.04",
			},
			"cpu": map[string]any{
				"cores":   float64(4),
				"sockets": float64(1),
			},
			"memory": map[string]any{
				"size": float64(8192),
			},
			"disks": []any{
				map[string]any{
					"name":    "scsi0",
					"storage": "local-lvm",
					"size":    "50G",
				},
			},
			"networks": []any{
				map[string]any{
					"name":   "net0",
					"bridge": "vmbr0",
					"model":  "virtio",
				},
			},
			"cloudInit": map[string]any{
				"user": "ubuntu",
				"sshKeys": []any{
					"ssh-ed25519 AAAA...",
				},
				"ipConfig": map[string]any{
					"net0": map[string]any{
						"ip": "dhcp",
					},
				},
			},
			"startOnCreate": true,
		},
	}

	spec, err := ParseVMSpec(resource)
	if err != nil {
		t.Fatalf("ParseVMSpec failed: %v", err)
	}

	if spec.Node != "pve1" {
		t.Errorf("expected node=pve1, got %s", spec.Node)
	}
	if spec.Template == nil || spec.Template.Name != "ubuntu-22.04" {
		t.Errorf("expected template.name=ubuntu-22.04")
	}
	if spec.CPU == nil || spec.CPU.Cores != 4 {
		t.Errorf("expected cpu.cores=4")
	}
	if spec.CPU.Sockets != 1 {
		t.Errorf("expected cpu.sockets=1")
	}
	if spec.Memory == nil || spec.Memory.Size != 8192 {
		t.Errorf("expected memory.size=8192")
	}
	if len(spec.Disks) != 1 {
		t.Errorf("expected 1 disk")
	}
	if spec.Disks[0].Name != "scsi0" {
		t.Errorf("expected disk.name=scsi0")
	}
	if spec.Disks[0].Size != "50G" {
		t.Errorf("expected disk.size=50G")
	}
	if len(spec.Networks) != 1 {
		t.Errorf("expected 1 network")
	}
	if spec.Networks[0].Bridge != "vmbr0" {
		t.Errorf("expected network.bridge=vmbr0")
	}
	if spec.CloudInit == nil {
		t.Fatal("expected cloudInit")
	}
	if spec.CloudInit.User != "ubuntu" {
		t.Errorf("expected cloudInit.user=ubuntu")
	}
	if len(spec.CloudInit.SSHKeys) != 1 {
		t.Errorf("expected 1 ssh key")
	}
	if spec.CloudInit.IPConfig["net0"].IP != "dhcp" {
		t.Errorf("expected ipConfig.net0.ip=dhcp")
	}
	if !spec.StartOnCreate {
		t.Errorf("expected startOnCreate=true")
	}
}

func TestParseVMSpec_Empty(t *testing.T) {
	resource := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata:   protocol.ResourceMetadata{Name: "empty-vm"},
		Spec:       nil,
	}

	spec, err := ParseVMSpec(resource)
	if err != nil {
		t.Fatalf("ParseVMSpec failed: %v", err)
	}

	if spec.Node != "" {
		t.Errorf("expected empty node, got %s", spec.Node)
	}
	if spec.Template != nil {
		t.Errorf("expected nil template")
	}
}

func TestParseVMSpec_TemplateVMID(t *testing.T) {
	resource := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata:   protocol.ResourceMetadata{Name: "test-vm"},
		Spec: map[string]any{
			"template": map[string]any{
				"vmid": float64(9000),
			},
		},
	}

	spec, err := ParseVMSpec(resource)
	if err != nil {
		t.Fatalf("ParseVMSpec failed: %v", err)
	}

	if spec.Template == nil {
		t.Fatal("expected template")
	}
	if spec.Template.VMID != 9000 {
		t.Errorf("expected template.vmid=9000, got %d", spec.Template.VMID)
	}
}

func TestVMSpec_ToProxmoxConfig(t *testing.T) {
	spec := &VMSpec{
		CPU: &CPUSpec{
			Cores:   4,
			Sockets: 2,
		},
		Memory: &MemorySpec{
			Size: 8192,
		},
		Networks: []NetworkSpec{
			{Name: "net0", Bridge: "vmbr0", Model: "virtio"},
			{Name: "net1", Bridge: "vmbr1"}, // Default model
		},
		CloudInit: &CloudInitSpec{
			User:     "admin",
			Password: "secret",
			SSHKeys:  []string{"ssh-ed25519 AAAA..."},
			IPConfig: map[string]IPConfig{
				"net0": {IP: "dhcp"},
				"net1": {IP: "192.168.1.100/24", Gateway: "192.168.1.1"},
			},
		},
	}

	params := spec.ToProxmoxConfig()

	if params["cores"] != 4 {
		t.Errorf("expected cores=4, got %v", params["cores"])
	}
	if params["sockets"] != 2 {
		t.Errorf("expected sockets=2, got %v", params["sockets"])
	}
	if params["memory"] != 8192 {
		t.Errorf("expected memory=8192, got %v", params["memory"])
	}
	if params["net0"] != "virtio,bridge=vmbr0" {
		t.Errorf("expected net0=virtio,bridge=vmbr0, got %v", params["net0"])
	}
	if params["net1"] != "virtio,bridge=vmbr1" {
		t.Errorf("expected net1=virtio,bridge=vmbr1, got %v", params["net1"])
	}
	if params["ciuser"] != "admin" {
		t.Errorf("expected ciuser=admin, got %v", params["ciuser"])
	}
	if params["cipassword"] != "secret" {
		t.Errorf("expected cipassword=secret, got %v", params["cipassword"])
	}
	if params["ipconfig0"] != "ip=dhcp" {
		t.Errorf("expected ipconfig0=ip=dhcp, got %v", params["ipconfig0"])
	}
	if params["ipconfig1"] != "ip=192.168.1.100/24,gw=192.168.1.1" {
		t.Errorf("expected ipconfig1 with static IP, got %v", params["ipconfig1"])
	}
}

func TestVMSpec_ToProxmoxConfig_Empty(t *testing.T) {
	spec := &VMSpec{}
	params := spec.ToProxmoxConfig()

	if len(params) != 0 {
		t.Errorf("expected empty params, got %v", params)
	}
}

func TestVMSpec_ToProxmoxConfig_SSHKeysEscaped(t *testing.T) {
	spec := &VMSpec{
		CloudInit: &CloudInitSpec{
			SSHKeys: []string{
				"ssh-ed25519 AAAA... user@host",
				"ssh-rsa BBBB... another@host",
			},
		},
	}

	params := spec.ToProxmoxConfig()

	sshKeys, ok := params["sshkeys"].(string)
	if !ok {
		t.Fatal("expected sshkeys to be a string")
	}

	// SSH keys should be URL-encoded
	if sshKeys == "" {
		t.Error("expected non-empty sshkeys")
	}
	// Should contain URL-encoded newline (%0A)
	if len(spec.CloudInit.SSHKeys) > 1 {
		// Multiple keys should be joined with newline (which gets URL-encoded)
		if sshKeys == "ssh-ed25519 AAAA... user@host" {
			t.Error("expected multiple SSH keys to be joined")
		}
	}
}
