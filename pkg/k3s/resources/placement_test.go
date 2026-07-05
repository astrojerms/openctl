package resources

import (
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

func TestPlacementHosts_NoHosts(t *testing.T) {
	spec := &ClusterSpec{
		Nodes: NodesSpec{
			ControlPlane: ControlPlaneSpec{Count: 3},
			Workers:      []WorkerSpec{{Name: "general", Count: 2}},
		},
	}
	got := PlacementHosts("c", spec)
	if len(got) != 0 {
		t.Fatalf("expected no placements without a host list, got %v", got)
	}
}

func TestPlacementHosts_ComputeWideRoundRobin(t *testing.T) {
	spec := &ClusterSpec{
		Compute: ComputeSpec{Nodes: []string{"pve1", "pve2", "pve3"}},
		Nodes: NodesSpec{
			ControlPlane: ControlPlaneSpec{Count: 3},
			Workers:      []WorkerSpec{{Name: "general", Count: 2}},
		},
	}
	got := PlacementHosts("dev", spec)

	// Control plane spreads one replica per host.
	want := map[string]string{
		"dev-cp-0":      "pve1",
		"dev-cp-1":      "pve2",
		"dev-cp-2":      "pve3",
		"dev-general-0": "pve1", // worker pool round-robins independently
		"dev-general-1": "pve2",
	}
	assertPlacement(t, want, got)
}

func TestPlacementHosts_PerPoolOverride(t *testing.T) {
	spec := &ClusterSpec{
		Compute: ComputeSpec{Nodes: []string{"pve1", "pve2", "pve3"}},
		Nodes: NodesSpec{
			// CP override pins the control plane to two dedicated hosts.
			ControlPlane: ControlPlaneSpec{Count: 2, Nodes: []string{"pveA", "pveB"}},
			Workers: []WorkerSpec{
				// This pool inherits the compute-wide default.
				{Name: "general", Count: 2},
				// This pool overrides onto a single host.
				{Name: "gpu", Count: 2, Nodes: []string{"pveGPU"}},
			},
		},
	}
	got := PlacementHosts("dev", spec)

	want := map[string]string{
		"dev-cp-0":      "pveA",
		"dev-cp-1":      "pveB",
		"dev-general-0": "pve1",
		"dev-general-1": "pve2",
		"dev-gpu-0":     "pveGPU",
		"dev-gpu-1":     "pveGPU",
	}
	assertPlacement(t, want, got)
}

func TestPlacementHosts_WrapsWhenFewerHostsThanNodes(t *testing.T) {
	spec := &ClusterSpec{
		Compute: ComputeSpec{Nodes: []string{"pve1", "pve2"}},
		Nodes: NodesSpec{
			ControlPlane: ControlPlaneSpec{Count: 3},
		},
	}
	got := PlacementHosts("dev", spec)
	want := map[string]string{
		"dev-cp-0": "pve1",
		"dev-cp-1": "pve2",
		"dev-cp-2": "pve1", // wraps back round-robin
	}
	assertPlacement(t, want, got)
}

func TestPlacementHosts_UnnamedWorkerPool(t *testing.T) {
	spec := &ClusterSpec{
		Compute: ComputeSpec{Nodes: []string{"pve1"}},
		Nodes: NodesSpec{
			ControlPlane: ControlPlaneSpec{Count: 0},
			Workers:      []WorkerSpec{{Count: 1}}, // no name -> "worker"
		},
	}
	got := PlacementHosts("dev", spec)
	if got["dev-worker-0"] != "pve1" {
		t.Fatalf("expected dev-worker-0 on pve1, got %v", got)
	}
}

// TestParseClusterSpec_Nodes verifies the placement host lists survive
// the map[string]any -> struct decode at all three levels.
func TestParseClusterSpec_Nodes(t *testing.T) {
	r := &protocol.Resource{
		Spec: map[string]any{
			"compute": map[string]any{
				"provider": "proxmox",
				"nodes":    []any{"pve1", "pve2"},
			},
			"nodes": map[string]any{
				"controlPlane": map[string]any{
					"count": float64(2),
					"nodes": []any{"pveA", "pveB"},
				},
				"workers": []any{
					map[string]any{
						"name":  "gpu",
						"count": float64(1),
						"nodes": []any{"pveGPU"},
					},
				},
			},
		},
	}
	spec, err := ParseClusterSpec(r)
	if err != nil {
		t.Fatalf("ParseClusterSpec: %v", err)
	}
	if got := spec.Compute.Nodes; len(got) != 2 || got[0] != "pve1" || got[1] != "pve2" {
		t.Errorf("compute.nodes = %v", got)
	}
	if got := spec.Nodes.ControlPlane.Nodes; len(got) != 2 || got[0] != "pveA" {
		t.Errorf("controlPlane.nodes = %v", got)
	}
	if got := spec.Nodes.Workers[0].Nodes; len(got) != 1 || got[0] != "pveGPU" {
		t.Errorf("workers[0].nodes = %v", got)
	}
}

func assertPlacement(t *testing.T, want, got map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("placement size = %d, want %d (got %v)", len(got), len(want), got)
	}
	for name, host := range want {
		if got[name] != host {
			t.Errorf("%s -> %q, want %q", name, got[name], host)
		}
	}
}
