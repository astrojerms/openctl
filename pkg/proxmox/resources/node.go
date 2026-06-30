package resources

import (
	"github.com/openctl/openctl/pkg/proxmox/client"
	"github.com/openctl/openctl/pkg/protocol"
)

// NodeToResource converts a Proxmox cluster member into a protocol Resource.
// ProxmoxNode is observed-only — there's no user-editable spec, the node
// exists because the Proxmox host is racked and reachable. All interesting
// data lives in status.
func NodeToResource(n *client.Node) *protocol.Resource {
	return &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "ProxmoxNode",
		Metadata: protocol.ResourceMetadata{
			Name: n.Node,
		},
		Spec: map[string]any{},
		Status: map[string]any{
			"state":  n.Status,
			"uptime": n.Uptime,
			"level":  n.Level,
			"cpu": map[string]any{
				"cores": n.MaxCPU,
				"used":  n.CPU,
			},
			"memory": map[string]any{
				"total": n.MaxMem,
				"used":  n.Mem,
			},
			"storage": map[string]any{
				"total": n.MaxDisk,
				"used":  n.Disk,
			},
		},
	}
}
