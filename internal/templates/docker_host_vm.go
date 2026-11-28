package templates

import (
	"fmt"

	"github.com/openctl/openctl/internal/guestbootstrap"
	"github.com/openctl/openctl/pkg/protocol"
)

// DockerHostVM provisions a dedicated Docker guest from an operator-owned template.
func DockerHostVM() *Template {
	params := []ParamDef{
		{Name: "name", Type: "string", Description: "Resource and Proxmox VM name.", Required: true},
		{Name: "context", Type: "string", Description: "Configured Proxmox provider context.", Required: true},
		{Name: "node", Type: "string", Description: "Proxmox node hosting the template and guest.", Required: true, OptionsKind: "ProxmoxNode"},
		{Name: "templateVMID", Type: "int", Description: "Existing Ubuntu 24.04 amd64 cloud-init template VMID.", Required: true},
		{Name: "cpuCores", Type: "int", Description: "Positive whole number of CPU cores.", Default: 2},
		{Name: "memoryMiB", Type: "int", Description: "Memory in MiB; minimum 512.", Default: 2048},
		{Name: "diskGiB", Type: "int", Description: "Root disk in GiB; minimum 8 and at least the source template disk size.", Default: 16},
		{Name: "storage", Type: "string", Description: "Proxmox disk storage.", Default: "local-lvm"},
		{Name: "bridge", Type: "string", Description: "Proxmox bridge for the DHCP guest NIC.", Default: "vmbr0"},
		{Name: "sshKey", Type: "string", Description: "SSH public key for the ubuntu guest user (not a private key).", Required: true},
	}
	return &Template{
		Name:        "docker-host-vm",
		DisplayName: "Docker Host VM",
		Description: "Dedicated Docker guest; requires an existing Ubuntu 24.04 amd64 cloud-init template. The disk must be at least the source template size. First boot installs official Docker CE and verified Compose v5.8.1; editing cloud-init does not rerun first-boot setup.",
		APIVersion:  "proxmox.openctl.io/v1",
		Kind:        "VirtualMachine",
		Parameters:  params,
		Render: func(p map[string]any) (*protocol.Resource, error) {
			if err := validateStarterParams(params, p); err != nil {
				return nil, err
			}
			templateVMID, err := starterInt(p, params, "templateVMID", 1, 999999999)
			if err != nil {
				return nil, err
			}
			cores, err := starterInt(p, params, "cpuCores", 1, 1<<53-1)
			if err != nil {
				return nil, err
			}
			memory, err := starterInt(p, params, "memoryMiB", 512, 1<<53-1)
			if err != nil {
				return nil, err
			}
			disk, err := starterInt(p, params, "diskGiB", 8, 1<<53-1)
			if err != nil {
				return nil, err
			}
			return &protocol.Resource{
				APIVersion: "proxmox.openctl.io/v1",
				Kind:       "VirtualMachine",
				Metadata: protocol.ResourceMetadata{
					Name:        getString(p, params, "name"),
					Annotations: map[string]string{"openctl.io/template": "docker-host-vm"},
				},
				Spec: map[string]any{
					"context":  getString(p, params, "context"),
					"node":     getString(p, params, "node"),
					"template": map[string]any{"vmid": templateVMID},
					"cpu":      map[string]any{"cores": cores, "sockets": 1},
					"memory":   map[string]any{"size": memory},
					"disks": []any{map[string]any{
						"name": "scsi0", "size": fmt.Sprintf("%dG", disk), "storage": getString(p, params, "storage"),
					}},
					"networks": []any{map[string]any{
						"name": "net0", "model": "virtio", "bridge": getString(p, params, "bridge"),
					}},
					"agent": map[string]any{"enabled": true},
					"cloudInit": map[string]any{
						"user":           "ubuntu",
						"sshKeys":        []any{getString(p, params, "sshKey")},
						"ipConfig":       map[string]any{"net0": map[string]any{"ip": "dhcp"}},
						"packageUpgrade": false,
						"packages":       []any{"qemu-guest-agent"},
						"runcmd":         []any{guestbootstrap.DockerHostCommand()},
					},
					"osType":        "l26",
					"startOnCreate": true,
				},
			}, nil
		},
	}
}
