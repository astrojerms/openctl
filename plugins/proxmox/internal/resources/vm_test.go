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

func TestVMSpec_ToProxmoxConfig_SSHKeysEncoded(t *testing.T) {
	spec := &VMSpec{
		CloudInit: &CloudInitSpec{
			SSHKeys: []string{
				"ssh-ed25519 AAAA+test user@host",
				"ssh-rsa BBBB another@host",
			},
		},
	}

	params := spec.ToProxmoxConfig()

	sshKeys, ok := params["sshkeys"].(string)
	if !ok {
		t.Fatal("expected sshkeys to be a string")
	}

	// SSH keys should be URL-encoded with %20 for spaces and %2B for + chars
	// Proxmox expects percent-encoding, not + for spaces
	expected := "ssh-ed25519%20AAAA%2Btest%20user%40host%0Assh-rsa%20BBBB%20another%40host"
	if sshKeys != expected {
		t.Errorf("expected sshkeys=%q, got %q", expected, sshKeys)
	}
}

func TestParseVMSpec_Image(t *testing.T) {
	resource := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata:   protocol.ResourceMetadata{Name: "image-vm"},
		Spec: map[string]any{
			"node": "pve1",
			"image": map[string]any{
				"storage":       "local",
				"file":          "ubuntu-22.04-cloudimg-amd64.img",
				"format":        "qcow2",
				"targetStorage": "local-lvm",
				"targetFormat":  "raw",
			},
			"cpu": map[string]any{
				"cores": float64(2),
			},
			"memory": map[string]any{
				"size": float64(4096),
			},
			"osType":  "l26",
			"bios":    "ovmf",
			"machine": "q35",
			"agent": map[string]any{
				"enabled": true,
			},
		},
	}

	spec, err := ParseVMSpec(resource)
	if err != nil {
		t.Fatalf("ParseVMSpec failed: %v", err)
	}

	if spec.Node != "pve1" {
		t.Errorf("expected node=pve1, got %s", spec.Node)
	}
	if spec.Template != nil {
		t.Errorf("expected template to be nil")
	}
	if spec.Image == nil {
		t.Fatal("expected image to be set")
	}
	if spec.Image.Storage != "local" {
		t.Errorf("expected image.storage=local, got %s", spec.Image.Storage)
	}
	if spec.Image.File != "ubuntu-22.04-cloudimg-amd64.img" {
		t.Errorf("expected image.file=ubuntu-22.04-cloudimg-amd64.img, got %s", spec.Image.File)
	}
	if spec.Image.Format != "qcow2" {
		t.Errorf("expected image.format=qcow2, got %s", spec.Image.Format)
	}
	if spec.Image.TargetStorage != "local-lvm" {
		t.Errorf("expected image.targetStorage=local-lvm, got %s", spec.Image.TargetStorage)
	}
	if spec.Image.TargetFormat != "raw" {
		t.Errorf("expected image.targetFormat=raw, got %s", spec.Image.TargetFormat)
	}
	if spec.OSType != "l26" {
		t.Errorf("expected osType=l26, got %s", spec.OSType)
	}
	if spec.BIOS != "ovmf" {
		t.Errorf("expected bios=ovmf, got %s", spec.BIOS)
	}
	if spec.Machine != "q35" {
		t.Errorf("expected machine=q35, got %s", spec.Machine)
	}
	if spec.Agent == nil || !spec.Agent.Enabled {
		t.Errorf("expected agent.enabled=true")
	}
}

func TestVMSpec_ToProxmoxConfig_WithAgent(t *testing.T) {
	spec := &VMSpec{
		CPU: &CPUSpec{
			Cores: 4,
		},
		Memory: &MemorySpec{
			Size: 8192,
		},
		OSType:  "l26",
		BIOS:    "ovmf",
		Machine: "q35",
		Agent: &AgentSpec{
			Enabled: true,
		},
	}

	params := spec.ToProxmoxConfig()

	if params["cores"] != 4 {
		t.Errorf("expected cores=4, got %v", params["cores"])
	}
	if params["memory"] != 8192 {
		t.Errorf("expected memory=8192, got %v", params["memory"])
	}
	if params["ostype"] != "l26" {
		t.Errorf("expected ostype=l26, got %v", params["ostype"])
	}
	if params["bios"] != "ovmf" {
		t.Errorf("expected bios=ovmf, got %v", params["bios"])
	}
	if params["machine"] != "q35" {
		t.Errorf("expected machine=q35, got %v", params["machine"])
	}
	if params["agent"] != "1" {
		t.Errorf("expected agent=1, got %v", params["agent"])
	}
}

func TestParseVMSpec_ImageMinimal(t *testing.T) {
	resource := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata:   protocol.ResourceMetadata{Name: "minimal-image-vm"},
		Spec: map[string]any{
			"image": map[string]any{
				"storage": "local",
				"file":    "debian-12-cloud.img",
			},
		},
	}

	spec, err := ParseVMSpec(resource)
	if err != nil {
		t.Fatalf("ParseVMSpec failed: %v", err)
	}

	if spec.Image == nil {
		t.Fatal("expected image to be set")
	}
	if spec.Image.Storage != "local" {
		t.Errorf("expected image.storage=local, got %s", spec.Image.Storage)
	}
	if spec.Image.File != "debian-12-cloud.img" {
		t.Errorf("expected image.file=debian-12-cloud.img, got %s", spec.Image.File)
	}
	if spec.Image.Format != "" {
		t.Errorf("expected empty format, got %s", spec.Image.Format)
	}
	if spec.Image.TargetStorage != "" {
		t.Errorf("expected empty targetStorage, got %s", spec.Image.TargetStorage)
	}
}

func TestParseVMSpec_CloudImage(t *testing.T) {
	resource := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata:   protocol.ResourceMetadata{Name: "cloud-image-vm"},
		Spec: map[string]any{
			"node": "pve1",
			"cloudImage": map[string]any{
				"url":          "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img",
				"storage":      "local",
				"diskStorage":  "local-lvm",
				"templateName": "ubuntu-jammy-template",
				"checksum":     "sha256:abc123",
			},
			"cpu": map[string]any{
				"cores": float64(2),
			},
			"memory": map[string]any{
				"size": float64(4096),
			},
		},
	}

	spec, err := ParseVMSpec(resource)
	if err != nil {
		t.Fatalf("ParseVMSpec failed: %v", err)
	}

	if spec.Node != "pve1" {
		t.Errorf("expected node=pve1, got %s", spec.Node)
	}
	if spec.CloudImage == nil {
		t.Fatal("expected cloudImage to be set")
	}
	if spec.CloudImage.URL != "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img" {
		t.Errorf("unexpected cloudImage.url: %s", spec.CloudImage.URL)
	}
	if spec.CloudImage.Storage != "local" {
		t.Errorf("expected cloudImage.storage=local, got %s", spec.CloudImage.Storage)
	}
	if spec.CloudImage.DiskStorage != "local-lvm" {
		t.Errorf("expected cloudImage.diskStorage=local-lvm, got %s", spec.CloudImage.DiskStorage)
	}
	if spec.CloudImage.TemplateName != "ubuntu-jammy-template" {
		t.Errorf("expected cloudImage.templateName=ubuntu-jammy-template, got %s", spec.CloudImage.TemplateName)
	}
	if spec.CloudImage.Checksum != "sha256:abc123" {
		t.Errorf("expected cloudImage.checksum=sha256:abc123, got %s", spec.CloudImage.Checksum)
	}
	if spec.CPU == nil || spec.CPU.Cores != 2 {
		t.Errorf("expected cpu.cores=2")
	}
	if spec.Memory == nil || spec.Memory.Size != 4096 {
		t.Errorf("expected memory.size=4096")
	}
}

func TestParseVMSpec_CloudImageMinimal(t *testing.T) {
	resource := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata:   protocol.ResourceMetadata{Name: "minimal-cloud-image-vm"},
		Spec: map[string]any{
			"cloudImage": map[string]any{
				"url":     "https://example.com/image.img",
				"storage": "local",
			},
		},
	}

	spec, err := ParseVMSpec(resource)
	if err != nil {
		t.Fatalf("ParseVMSpec failed: %v", err)
	}

	if spec.CloudImage == nil {
		t.Fatal("expected cloudImage to be set")
	}
	if spec.CloudImage.URL != "https://example.com/image.img" {
		t.Errorf("expected cloudImage.url, got %s", spec.CloudImage.URL)
	}
	if spec.CloudImage.Storage != "local" {
		t.Errorf("expected cloudImage.storage=local, got %s", spec.CloudImage.Storage)
	}
	// Optional fields should be empty
	if spec.CloudImage.DiskStorage != "" {
		t.Errorf("expected empty diskStorage, got %s", spec.CloudImage.DiskStorage)
	}
	if spec.CloudImage.TemplateName != "" {
		t.Errorf("expected empty templateName, got %s", spec.CloudImage.TemplateName)
	}
	if spec.CloudImage.Checksum != "" {
		t.Errorf("expected empty checksum, got %s", spec.CloudImage.Checksum)
	}
}

func TestParseVMSpec_ImageContentType(t *testing.T) {
	resource := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata:   protocol.ResourceMetadata{Name: "image-with-content-type"},
		Spec: map[string]any{
			"image": map[string]any{
				"storage":     "local",
				"file":        "image.img",
				"contentType": "iso",
			},
		},
	}

	spec, err := ParseVMSpec(resource)
	if err != nil {
		t.Fatalf("ParseVMSpec failed: %v", err)
	}

	if spec.Image == nil {
		t.Fatal("expected image to be set")
	}
	if spec.Image.ContentType != "iso" {
		t.Errorf("expected image.contentType=iso, got %s", spec.Image.ContentType)
	}
}

func TestParseVMSpec_StaticIP(t *testing.T) {
	resource := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata:   protocol.ResourceMetadata{Name: "static-ip-vm"},
		Spec: map[string]any{
			"cloudInit": map[string]any{
				"user": "admin",
				"ipConfig": map[string]any{
					"net0": map[string]any{
						"ip":      "192.168.1.100/24",
						"gateway": "192.168.1.1",
					},
				},
			},
		},
	}

	spec, err := ParseVMSpec(resource)
	if err != nil {
		t.Fatalf("ParseVMSpec failed: %v", err)
	}

	if spec.CloudInit == nil {
		t.Fatal("expected cloudInit to be set")
	}
	ipConfig, ok := spec.CloudInit.IPConfig["net0"]
	if !ok {
		t.Fatal("expected net0 ipConfig")
	}
	if ipConfig.IP != "192.168.1.100/24" {
		t.Errorf("expected ip=192.168.1.100/24, got %s", ipConfig.IP)
	}
	if ipConfig.Gateway != "192.168.1.1" {
		t.Errorf("expected gateway=192.168.1.1, got %s", ipConfig.Gateway)
	}
}

func TestVMSpec_ToProxmoxConfig_StaticIP(t *testing.T) {
	spec := &VMSpec{
		CloudInit: &CloudInitSpec{
			User: "admin",
			IPConfig: map[string]IPConfig{
				"net0": {IP: "192.168.1.100/24", Gateway: "192.168.1.1"},
			},
		},
	}

	params := spec.ToProxmoxConfig()

	if params["ipconfig0"] != "ip=192.168.1.100/24,gw=192.168.1.1" {
		t.Errorf("expected ipconfig0=ip=192.168.1.100/24,gw=192.168.1.1, got %v", params["ipconfig0"])
	}
}

func TestParseVMSpec_MultipleNetworks(t *testing.T) {
	resource := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata:   protocol.ResourceMetadata{Name: "multi-network-vm"},
		Spec: map[string]any{
			"networks": []any{
				map[string]any{"name": "net0", "bridge": "vmbr0", "model": "virtio"},
				map[string]any{"name": "net1", "bridge": "vmbr1", "model": "e1000"},
				map[string]any{"name": "net2", "bridge": "vmbr2"},
			},
		},
	}

	spec, err := ParseVMSpec(resource)
	if err != nil {
		t.Fatalf("ParseVMSpec failed: %v", err)
	}

	if len(spec.Networks) != 3 {
		t.Fatalf("expected 3 networks, got %d", len(spec.Networks))
	}
	if spec.Networks[0].Model != "virtio" {
		t.Errorf("expected net0.model=virtio, got %s", spec.Networks[0].Model)
	}
	if spec.Networks[1].Model != "e1000" {
		t.Errorf("expected net1.model=e1000, got %s", spec.Networks[1].Model)
	}
	if spec.Networks[2].Model != "" {
		t.Errorf("expected net2.model to be empty, got %s", spec.Networks[2].Model)
	}
}

func TestParseVMSpec_MultipleDisks(t *testing.T) {
	resource := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata:   protocol.ResourceMetadata{Name: "multi-disk-vm"},
		Spec: map[string]any{
			"disks": []any{
				map[string]any{"name": "scsi0", "storage": "local-lvm", "size": "50G"},
				map[string]any{"name": "scsi1", "storage": "local-lvm", "size": "100G"},
			},
		},
	}

	spec, err := ParseVMSpec(resource)
	if err != nil {
		t.Fatalf("ParseVMSpec failed: %v", err)
	}

	if len(spec.Disks) != 2 {
		t.Fatalf("expected 2 disks, got %d", len(spec.Disks))
	}
	if spec.Disks[0].Name != "scsi0" {
		t.Errorf("expected disk 0 name=scsi0, got %s", spec.Disks[0].Name)
	}
	if spec.Disks[0].Size != "50G" {
		t.Errorf("expected disk 0 size=50G, got %s", spec.Disks[0].Size)
	}
	if spec.Disks[1].Name != "scsi1" {
		t.Errorf("expected disk 1 name=scsi1, got %s", spec.Disks[1].Name)
	}
	if spec.Disks[1].Size != "100G" {
		t.Errorf("expected disk 1 size=100G, got %s", spec.Disks[1].Size)
	}
}
