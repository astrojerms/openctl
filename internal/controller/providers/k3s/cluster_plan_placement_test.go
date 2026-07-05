package k3s

import (
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

// TestPlan_Placement verifies the planner threads a per-pool host list
// onto each VirtualMachine child's spec.node, spreading control-plane
// replicas across hosts round-robin while a worker pool honors its own
// override.
func TestPlan_Placement(t *testing.T) {
	m := clusterManifest("dev", func(r *protocol.Resource) {
		compute := r.Spec["compute"].(map[string]any)
		compute["nodes"] = []any{"pve1", "pve2", "pve3"}
		nodes := r.Spec["nodes"].(map[string]any)
		nodes["controlPlane"] = map[string]any{"count": float64(3)}
		nodes["workers"] = []any{
			map[string]any{"name": "gpu", "count": float64(1), "nodes": []any{"pveGPU"}},
		}
	})

	children := planFor(t, m)

	want := map[string]string{
		"dev-cp-0":  "pve1",
		"dev-cp-1":  "pve2",
		"dev-cp-2":  "pve3",
		"dev-gpu-0": "pveGPU",
	}
	for name, host := range want {
		vm := findByKindName(children, "VirtualMachine", name)
		if vm == nil {
			t.Errorf("missing VirtualMachine %s", name)
			continue
		}
		got, ok := vm.Spec["node"].(string)
		if !ok {
			t.Errorf("%s: spec.node not set", name)
			continue
		}
		if got != host {
			t.Errorf("%s: spec.node = %q, want %q", name, got, host)
		}
	}
}

// TestPlan_NoPlacement verifies that without a host list the planner
// leaves spec.node unset, preserving the provider-default behavior.
func TestPlan_NoPlacement(t *testing.T) {
	children := planFor(t, clusterManifest("dev"))
	vm := findByKindName(children, "VirtualMachine", "dev-cp-0")
	if vm == nil {
		t.Fatal("missing VirtualMachine dev-cp-0")
	}
	if _, ok := vm.Spec["node"]; ok {
		t.Errorf("expected spec.node unset without placement, got %v", vm.Spec["node"])
	}
}
