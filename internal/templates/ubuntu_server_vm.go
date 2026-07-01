package templates

import (
	"fmt"

	"github.com/openctl/openctl/pkg/protocol"
)

// UbuntuServerVM is the starter template for "a Ubuntu server VM on
// Proxmox with cloud-init". Six user inputs (name / node / size /
// diskGB / sshKey / cloudImageStorage); everything else is defaulted
// to what a homelab user would pick on their own (jammy cloud image,
// scsi disk with SSD + discard flags, single virtio NIC on vmbr0,
// QEMU guest agent enabled, ubuntu user).
func UbuntuServerVM() *Template {
	params := []ParamDef{
		{
			Name:        "name",
			Type:        "string",
			Description: "Resource name (also the VM name in Proxmox).",
			Required:    true,
		},
		{
			Name:        "node",
			Type:        "string",
			Description: "Proxmox node to host the VM.",
			Required:    true,
			OptionsKind: "ProxmoxNode",
		},
		{
			Name:        "size",
			Type:        "string",
			Description: "Preset sizing: small (2c/2G), medium (4c/4G), large (8c/16G).",
			Default:     "small",
			Enum:        []string{"small", "medium", "large"},
		},
		{
			Name:        "diskGB",
			Type:        "int",
			Description: "Root disk size in GiB. Minimum 8.",
			Default:     32,
		},
		{
			Name:        "sshKey",
			Type:        "string",
			Description: "SSH public key to inject via cloud-init.",
			Required:    true,
		},
		{
			Name:        "storage",
			Type:        "string",
			Description: "Proxmox storage for the cloud image and disks.",
			Default:     "local-lvm",
		},
	}

	sizes := map[string]struct{ cores, memMB int }{
		"small":  {2, 2048},
		"medium": {4, 4096},
		"large":  {8, 16384},
	}

	return &Template{
		Name:        "ubuntu-server-vm",
		DisplayName: "Ubuntu Server VM",
		Description: "Minimal Ubuntu 22.04 server on Proxmox with cloud-init and QEMU guest agent.",
		APIVersion:  "proxmox.openctl.io/v1",
		Kind:        "VirtualMachine",
		Parameters:  params,
		Render: func(p map[string]any) (*protocol.Resource, error) {
			size := getString(p, params, "size")
			sz, ok := sizes[size]
			if !ok {
				return nil, fmt.Errorf("unknown size %q", size)
			}
			diskGB := max(getInt(p, params, "diskGB"), 8)
			storage := getString(p, params, "storage")
			return &protocol.Resource{
				APIVersion: "proxmox.openctl.io/v1",
				Kind:       "VirtualMachine",
				Metadata: protocol.ResourceMetadata{
					Name: getString(p, params, "name"),
					Annotations: map[string]string{
						"openctl.io/template": "ubuntu-server-vm",
					},
				},
				Spec: map[string]any{
					"node": getString(p, params, "node"),
					"cpu": map[string]any{
						"cores":   sz.cores,
						"sockets": 1,
					},
					"memory": map[string]any{
						"size": sz.memMB,
					},
					"cloudImage": map[string]any{
						"url":     "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img",
						"storage": storage,
					},
					"disks": []any{
						map[string]any{
							"name":     "scsi0",
							"storage":  storage,
							"size":     fmt.Sprintf("%dG", diskGB),
							"ssd":      true,
							"discard":  true,
							"iothread": true,
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
						"user":    "ubuntu",
						"sshKeys": []any{getString(p, params, "sshKey")},
					},
					"agent": map[string]any{
						"enabled": true,
					},
					"osType":        "l26",
					"startOnCreate": true,
				},
			}, nil
		},
	}
}
