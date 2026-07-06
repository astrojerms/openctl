package k3s

import (
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

// A cluster with network.perContext stamps each VM's net0 bridge from its
// placement context's block (separate-L2 spread), while single-L2 clusters
// keep the cluster-wide bridge.
func TestPlan_PerContextBridge(t *testing.T) {
	m := clusterManifest("dev", func(r *protocol.Resource) {
		nodes := r.Spec["nodes"].(map[string]any)
		nodes["controlPlane"] = map[string]any{
			"count": float64(2),
			"targets": []any{
				map[string]any{"context": "siteA", "node": "pve"},
				map[string]any{"context": "siteB", "node": "pve"},
			},
		}
		net := r.Spec["network"].(map[string]any)
		net["dhcp"] = false
		net["perContext"] = map[string]any{
			"siteA": map[string]any{
				"bridge":    "vmbr0",
				"staticIPs": map[string]any{"startIP": "10.1.0.10", "gateway": "10.1.0.1", "netmask": "24"},
			},
			"siteB": map[string]any{
				"bridge":    "vmbr1",
				"staticIPs": map[string]any{"startIP": "10.2.0.10", "gateway": "10.2.0.1", "netmask": "24"},
			},
		}
	})

	children := planFor(t, m)

	// cp-0 → siteA (vmbr0, 10.1.x), cp-1 → siteB (vmbr1, 10.2.x).
	wantBridge := map[string]string{"dev-cp-0": "vmbr0", "dev-cp-1": "vmbr1"}
	for name, bridge := range wantBridge {
		vm := findByKindName(children, "VirtualMachine", name)
		if vm == nil {
			t.Errorf("missing VM %s", name)
			continue
		}
		nets, ok := vm.Spec["networks"].([]any)
		if !ok || len(nets) == 0 {
			t.Errorf("%s: no networks", name)
			continue
		}
		net0, _ := nets[0].(map[string]any)
		if got := net0["bridge"]; got != bridge {
			t.Errorf("%s: net0.bridge = %v, want %q", name, got, bridge)
		}
	}
}
