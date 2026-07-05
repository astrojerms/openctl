package cluster

import (
	"testing"

	"github.com/openctl/openctl/pkg/k3s/resources"
	"github.com/openctl/openctl/pkg/protocol"
)

// TestGenerateDispatchRequests_Placement verifies that a per-pool host
// list is threaded onto each VM manifest's spec.node, so the Proxmox
// handler creates the VM on that host instead of its configured default.
func TestGenerateDispatchRequests_Placement(t *testing.T) {
	spec := &resources.ClusterSpec{
		Compute: resources.ComputeSpec{
			Provider: "proxmox",
			Image:    resources.ImageSpec{Template: "ubuntu-template"},
			Default:  resources.DefaultSizeSpec{CPUs: 2, MemoryMB: 4096, DiskGB: 40},
			Nodes:    []string{"pve1", "pve2", "pve3"},
		},
		Nodes: resources.NodesSpec{
			ControlPlane: resources.ControlPlaneSpec{Count: 3},
			Workers:      []resources.WorkerSpec{{Name: "general", Count: 1, Nodes: []string{"pveW"}}},
		},
		SSH: resources.SSHSpec{User: "ubuntu"},
	}

	creator := NewCreator("dev", spec, &protocol.ProviderConfig{})
	requests := creator.GenerateDispatchRequests()

	want := map[string]string{
		"vm-dev-cp-0":      "pve1",
		"vm-dev-cp-1":      "pve2",
		"vm-dev-cp-2":      "pve3",
		"vm-dev-general-0": "pveW",
	}
	if len(requests) != len(want) {
		t.Fatalf("expected %d requests, got %d", len(want), len(requests))
	}
	for _, req := range requests {
		node, ok := req.Manifest.Spec["node"].(string)
		if !ok {
			t.Errorf("%s: spec.node not set", req.ID)
			continue
		}
		if node != want[req.ID] {
			t.Errorf("%s: spec.node = %q, want %q", req.ID, node, want[req.ID])
		}
	}
}

// TestGenerateDispatchRequests_NoPlacement verifies backward
// compatibility: with no host list, spec.node is left unset so the
// provider falls back to its configured default node.
func TestGenerateDispatchRequests_NoPlacement(t *testing.T) {
	spec := &resources.ClusterSpec{
		Compute: resources.ComputeSpec{
			Provider: "proxmox",
			Image:    resources.ImageSpec{Template: "ubuntu-template"},
			Default:  resources.DefaultSizeSpec{CPUs: 2, MemoryMB: 4096, DiskGB: 40},
		},
		Nodes: resources.NodesSpec{ControlPlane: resources.ControlPlaneSpec{Count: 1}},
		SSH:   resources.SSHSpec{User: "ubuntu"},
	}

	creator := NewCreator("dev", spec, &protocol.ProviderConfig{})
	requests := creator.GenerateDispatchRequests()

	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if _, ok := requests[0].Manifest.Spec["node"]; ok {
		t.Errorf("expected spec.node to be unset without placement, got %v", requests[0].Manifest.Spec["node"])
	}
}
