package templates

import (
	"fmt"

	"github.com/openctl/openctl/pkg/protocol"
)

// SmallK3sCluster is the starter template for "give me a working k3s
// cluster on my Proxmox with sensible defaults". Nine user inputs
// cover the choices that actually matter (name, node, worker count,
// per-node size, network mode, ssh identity); everything else — image
// URL, storage names, install args — is defaulted.
func SmallK3sCluster() *Template {
	params := []ParamDef{
		{Name: "name", Type: "string", Description: "Cluster name.", Required: true},
		{Name: "node", Type: "string", Description: "Proxmox node to host all VMs.", Required: true, OptionsKind: "ProxmoxNode"},
		{Name: "controlPlaneCount", Type: "int", Description: "Number of control-plane nodes (1 = dev, 3 = HA).", Default: 1},
		{Name: "workerCount", Type: "int", Description: "Number of worker nodes.", Default: 2},
		{Name: "size", Type: "string", Description: "Per-node sizing.", Default: "small",
			Enum: []string{"small", "medium", "large"}},
		{Name: "storage", Type: "string", Description: "Proxmox storage for VM disks.", Default: "local-lvm"},
		{Name: "startIP", Type: "string",
			Description: "First IP for static allocation (e.g. 192.168.1.100). Nodes get sequential IPs from here.",
			Required:    true},
		{Name: "gateway", Type: "string", Description: "Default gateway (e.g. 192.168.1.1).", Required: true},
		{Name: "sshPrivateKeyPath", Type: "string",
			Description: "Absolute path to SSH private key on the controller host.",
			Required:    true},
		{Name: "sshPublicKey", Type: "string",
			Description: "SSH public key (matches privateKeyPath) injected into every node.",
			Required:    true},
	}

	sizes := map[string]struct{ cpus, memMB, diskGB int }{
		"small":  {2, 2048, 20},
		"medium": {4, 4096, 40},
		"large":  {8, 16384, 80},
	}

	return &Template{
		Name:        "small-k3s-cluster",
		DisplayName: "Small k3s Cluster",
		Description: "k3s cluster on Proxmox with static-IP networking, cloud-init, and sensible defaults for a homelab.",
		APIVersion:  "k3s.openctl.io/v1",
		Kind:        "Cluster",
		Parameters:  params,
		Render: func(p map[string]any) (*protocol.Resource, error) {
			sizeName := getString(p, params, "size")
			sz, ok := sizes[sizeName]
			if !ok {
				return nil, fmt.Errorf("unknown size %q", sizeName)
			}
			// node is captured for future per-VM placement (spec.compute
			// doesn't currently expose it). Discarded for now.
			_ = getString(p, params, "node")
			storage := getString(p, params, "storage")
			cpCount := getInt(p, params, "controlPlaneCount")
			if cpCount < 1 {
				cpCount = 1
			}
			workers := getInt(p, params, "workerCount")
			if workers < 0 {
				workers = 0
			}
			return &protocol.Resource{
				APIVersion: "k3s.openctl.io/v1",
				Kind:       "Cluster",
				Metadata: protocol.ResourceMetadata{
					Name: getString(p, params, "name"),
					Annotations: map[string]string{
						"openctl.io/template": "small-k3s-cluster",
					},
				},
				Spec: map[string]any{
					"compute": map[string]any{
						"provider": "proxmox",
						"image": map[string]any{
							"url":         "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img",
							"storage":     storage,
							"diskStorage": storage,
						},
						"default": map[string]any{
							"cpus":     sz.cpus,
							"memoryMB": sz.memMB,
							"diskGB":   sz.diskGB,
						},
					},
					"nodes": map[string]any{
						"controlPlane": map[string]any{
							"count": cpCount,
						},
						"workers": []any{
							map[string]any{
								"name":  "worker",
								"count": workers,
							},
						},
					},
					"network": map[string]any{
						"bridge": "vmbr0",
						"dhcp":   false,
						"staticIPs": map[string]any{
							"startIP": getString(p, params, "startIP"),
							"gateway": getString(p, params, "gateway"),
							"netmask": "24",
						},
					},
					"ssh": map[string]any{
						"user":           "ubuntu",
						"privateKeyPath": getString(p, params, "sshPrivateKeyPath"),
						"publicKeys":     []any{getString(p, params, "sshPublicKey")},
					},
				},
				Status: map[string]any{},
			}, nil
		},
	}
}

