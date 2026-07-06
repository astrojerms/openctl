package schema

import (
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

// A k3s Cluster manifest using network.perContext validates against the schema.
func TestValidate_ClusterPerContext(t *testing.T) {
	r := &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       "Cluster",
		Metadata:   protocol.ResourceMetadata{Name: "dev"},
		Spec: map[string]any{
			"compute": map[string]any{
				"provider": "proxmox",
				"image":    map[string]any{"template": "ubuntu-2204"},
				"default":  map[string]any{"cpus": 2, "memoryMB": 4096, "diskGB": 40},
			},
			"nodes": map[string]any{"controlPlane": map[string]any{"count": 2}},
			"ssh":   map[string]any{"user": "ubuntu", "privateKeyPath": "/root/.ssh/id_ed25519"},
			"network": map[string]any{
				"dhcp": false,
				"perContext": map[string]any{
					"siteA": map[string]any{"bridge": "vmbr0", "staticIPs": map[string]any{"startIP": "10.1.0.10", "gateway": "10.1.0.1", "netmask": "24"}},
					"siteB": map[string]any{"bridge": "vmbr1", "staticIPs": map[string]any{"startIP": "10.2.0.10"}},
				},
			},
		},
	}
	if err := Validate(r); err != nil {
		t.Fatalf("perContext cluster should validate: %v", err)
	}
}
