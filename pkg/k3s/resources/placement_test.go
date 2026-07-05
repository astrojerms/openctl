package resources

import (
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

func TestPlacementTargets_NoPlacement(t *testing.T) {
	spec := &ClusterSpec{
		Nodes: NodesSpec{
			ControlPlane: ControlPlaneSpec{Count: 3},
			Workers:      []WorkerSpec{{Name: "general", Count: 2}},
		},
	}
	got := PlacementTargets("c", spec)
	if len(got) != 0 {
		t.Fatalf("expected no placement without any context/host, got %v", got)
	}
}

// Host lists with no context reproduce the same-endpoint spread: node set,
// context empty (provider default endpoint).
func TestPlacementTargets_HostsOnlyRoundRobin(t *testing.T) {
	spec := &ClusterSpec{
		Compute: ComputeSpec{Nodes: []string{"pve1", "pve2", "pve3"}},
		Nodes: NodesSpec{
			ControlPlane: ControlPlaneSpec{Count: 3},
			Workers:      []WorkerSpec{{Name: "general", Count: 2}},
		},
	}
	want := map[string]PlacementTarget{
		"dev-cp-0":      {Node: "pve1"},
		"dev-cp-1":      {Node: "pve2"},
		"dev-cp-2":      {Node: "pve3"},
		"dev-general-0": {Node: "pve1"},
		"dev-general-1": {Node: "pve2"},
	}
	assertTargets(t, want, PlacementTargets("dev", spec))
}

// compute.context applies to every pool that doesn't override it: nodes get a
// context but no host (endpoint default host).
func TestPlacementTargets_ComputeContextDefault(t *testing.T) {
	spec := &ClusterSpec{
		Compute: ComputeSpec{Context: "siteA"},
		Nodes: NodesSpec{
			ControlPlane: ControlPlaneSpec{Count: 2},
			Workers:      []WorkerSpec{{Name: "w", Count: 1}},
		},
	}
	want := map[string]PlacementTarget{
		"dev-cp-0": {Context: "siteA"},
		"dev-cp-1": {Context: "siteA"},
		"dev-w-0":  {Context: "siteA"},
	}
	assertTargets(t, want, PlacementTargets("dev", spec))
}

// The headline case: spread the control plane across endpoints for HA quorum,
// while a worker pool pins to a single endpoint via per-pool context.
func TestPlacementTargets_CrossEndpointControlPlane(t *testing.T) {
	spec := &ClusterSpec{
		Nodes: NodesSpec{
			ControlPlane: ControlPlaneSpec{
				Count: 3,
				Targets: []PlacementTarget{
					{Context: "siteA", Node: "pve"},
					{Context: "siteB", Node: "pve"},
					{Context: "siteC", Node: "pve"},
				},
			},
			Workers: []WorkerSpec{
				{Name: "gpu", Count: 2, Context: "siteB"},
			},
		},
	}
	want := map[string]PlacementTarget{
		"dev-cp-0":  {Context: "siteA", Node: "pve"},
		"dev-cp-1":  {Context: "siteB", Node: "pve"},
		"dev-cp-2":  {Context: "siteC", Node: "pve"},
		"dev-gpu-0": {Context: "siteB"},
		"dev-gpu-1": {Context: "siteB"},
	}
	assertTargets(t, want, PlacementTargets("dev", spec))
}

// A target with an empty context inherits the pool context, which itself
// falls back to the cluster compute context.
func TestPlacementTargets_TargetContextInheritance(t *testing.T) {
	spec := &ClusterSpec{
		Compute: ComputeSpec{Context: "siteDefault"},
		Nodes: NodesSpec{
			ControlPlane: ControlPlaneSpec{
				Count: 2,
				Targets: []PlacementTarget{
					{Node: "pveX"},                   // inherits siteDefault
					{Context: "siteB", Node: "pveY"}, // explicit
				},
			},
		},
	}
	want := map[string]PlacementTarget{
		"dev-cp-0": {Context: "siteDefault", Node: "pveX"},
		"dev-cp-1": {Context: "siteB", Node: "pveY"},
	}
	assertTargets(t, want, PlacementTargets("dev", spec))
}

// Per-pool context + host list: hosts spread within the pool's endpoint.
func TestPlacementTargets_PoolContextWithHosts(t *testing.T) {
	spec := &ClusterSpec{
		Nodes: NodesSpec{
			ControlPlane: ControlPlaneSpec{
				Count:   2,
				Context: "siteA",
				Nodes:   []string{"pve1", "pve2"},
			},
		},
	}
	want := map[string]PlacementTarget{
		"dev-cp-0": {Context: "siteA", Node: "pve1"},
		"dev-cp-1": {Context: "siteA", Node: "pve2"},
	}
	assertTargets(t, want, PlacementTargets("dev", spec))
}

// TestParseClusterSpec_Placement verifies context/nodes/targets survive the
// map[string]any -> struct decode at every level.
func TestParseClusterSpec_Placement(t *testing.T) {
	r := &protocol.Resource{
		Spec: map[string]any{
			"compute": map[string]any{
				"provider": "proxmox",
				"context":  "siteDefault",
				"nodes":    []any{"pve1"},
			},
			"nodes": map[string]any{
				"controlPlane": map[string]any{
					"count": float64(3),
					"targets": []any{
						map[string]any{"context": "siteA", "node": "pve"},
						map[string]any{"context": "siteB", "node": "pve"},
					},
				},
				"workers": []any{
					map[string]any{
						"name":    "gpu",
						"count":   float64(1),
						"context": "siteC",
						"nodes":   []any{"pveGPU"},
					},
				},
			},
		},
	}
	spec, err := ParseClusterSpec(r)
	if err != nil {
		t.Fatalf("ParseClusterSpec: %v", err)
	}
	if spec.Compute.Context != "siteDefault" {
		t.Errorf("compute.context = %q", spec.Compute.Context)
	}
	if ts := spec.Nodes.ControlPlane.Targets; len(ts) != 2 || ts[0].Context != "siteA" || ts[1].Node != "pve" {
		t.Errorf("controlPlane.targets = %+v", ts)
	}
	if w := spec.Nodes.Workers[0]; w.Context != "siteC" || len(w.Nodes) != 1 || w.Nodes[0] != "pveGPU" {
		t.Errorf("workers[0] = %+v", w)
	}
}

func assertTargets(t *testing.T, want, got map[string]PlacementTarget) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("placement size = %d, want %d (got %+v)", len(got), len(want), got)
	}
	for name, target := range want {
		if got[name] != target {
			t.Errorf("%s -> %+v, want %+v", name, got[name], target)
		}
	}
}
