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

// TestGenerateDispatchRequests_CrossEndpoint verifies control-plane targets
// spread each VM across provider endpoints via spec.context (+ spec.node),
// so the multi-context Proxmox provider routes each replica to its endpoint.
func TestGenerateDispatchRequests_CrossEndpoint(t *testing.T) {
	spec := &resources.ClusterSpec{
		Compute: resources.ComputeSpec{
			Provider: "proxmox",
			Image:    resources.ImageSpec{Template: "ubuntu-template"},
			Default:  resources.DefaultSizeSpec{CPUs: 2, MemoryMB: 4096, DiskGB: 40},
		},
		Nodes: resources.NodesSpec{
			ControlPlane: resources.ControlPlaneSpec{
				Count: 3,
				Targets: []resources.PlacementTarget{
					{Context: "siteA", Node: "pve"},
					{Context: "siteB", Node: "pve"},
					{Context: "siteC", Node: "pve"},
				},
			},
		},
		SSH: resources.SSHSpec{User: "ubuntu"},
	}

	requests := NewCreator("dev", spec, &protocol.ProviderConfig{}).GenerateDispatchRequests()

	wantCtx := map[string]string{
		"vm-dev-cp-0": "siteA",
		"vm-dev-cp-1": "siteB",
		"vm-dev-cp-2": "siteC",
	}
	if len(requests) != len(wantCtx) {
		t.Fatalf("expected %d requests, got %d", len(wantCtx), len(requests))
	}
	for _, req := range requests {
		ctx, _ := req.Manifest.Spec["context"].(string)
		if ctx != wantCtx[req.ID] {
			t.Errorf("%s: spec.context = %q, want %q", req.ID, ctx, wantCtx[req.ID])
		}
		if node, _ := req.Manifest.Spec["node"].(string); node != "pve" {
			t.Errorf("%s: spec.node = %q, want pve", req.ID, node)
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
	if _, ok := requests[0].Manifest.Spec["context"]; ok {
		t.Errorf("expected spec.context to be unset without placement, got %v", requests[0].Manifest.Spec["context"])
	}
}

// TestGenerateDispatchRequests_GPU verifies a per-pool GPU request stamps the
// passthrough hardware (q35 + OVMF + efidisk + cpu host + hostpci) onto only
// that pool's node VMs, leaving control-plane and non-GPU workers untouched.
func TestGenerateDispatchRequests_GPU(t *testing.T) {
	spec := &resources.ClusterSpec{
		Compute: resources.ComputeSpec{
			Provider: "proxmox",
			Image:    resources.ImageSpec{Template: "ubuntu-template"},
			Default:  resources.DefaultSizeSpec{CPUs: 2, MemoryMB: 4096, DiskGB: 40},
		},
		Nodes: resources.NodesSpec{
			ControlPlane: resources.ControlPlaneSpec{Count: 1},
			Workers: []resources.WorkerSpec{
				{Name: "general", Count: 1},
				{Name: "gpu", Count: 1, Nodes: []string{"pve-gpu"}, GPU: &resources.GPUSpec{
					EFIStorage: "local-lvm",
					Devices: []resources.PCIDevice{
						{Mapping: "rtx4090", PrimaryGPU: true},
					},
				}},
			},
		},
		SSH: resources.SSHSpec{User: "ubuntu"},
	}

	byID := map[string]*protocol.Resource{}
	for _, req := range NewCreator("dev", spec, &protocol.ProviderConfig{}).GenerateDispatchRequests() {
		byID[req.ID] = req.Manifest
	}

	// The GPU worker gets the full passthrough hardware.
	gpu := byID["vm-dev-gpu-0"]
	if gpu == nil {
		t.Fatalf("missing gpu node request; got %v", byID)
	}
	if gpu.Spec["machine"] != "q35" || gpu.Spec["bios"] != "ovmf" {
		t.Errorf("gpu node not built for passthrough: machine=%v bios=%v", gpu.Spec["machine"], gpu.Spec["bios"])
	}
	if cpu, _ := gpu.Spec["cpu"].(map[string]any); cpu["type"] != "host" || cpu["cores"] != 2 {
		t.Errorf("gpu cpu wrong (want cores=2,type=host): %v", gpu.Spec["cpu"])
	}
	if efi, _ := gpu.Spec["efiDisk"].(map[string]any); efi["storage"] != "local-lvm" {
		t.Errorf("gpu efiDisk wrong: %v", gpu.Spec["efiDisk"])
	}
	devs, _ := gpu.Spec["hostPCI"].([]map[string]any)
	if len(devs) != 1 || devs[0]["mapping"] != "rtx4090" || devs[0]["pcie"] != true || devs[0]["primaryGPU"] != true {
		t.Errorf("gpu hostPCI wrong: %v", gpu.Spec["hostPCI"])
	}

	// The non-GPU worker and the control plane must NOT get passthrough fields.
	for _, id := range []string{"vm-dev-cp-0", "vm-dev-general-0"} {
		m := byID[id]
		if m == nil {
			t.Fatalf("missing %s", id)
		}
		if _, ok := m.Spec["hostPCI"]; ok {
			t.Errorf("%s should not have hostPCI", id)
		}
		if _, ok := m.Spec["machine"]; ok {
			t.Errorf("%s should not have machine set", id)
		}
		if cpu, _ := m.Spec["cpu"].(map[string]any); cpu["type"] != nil {
			t.Errorf("%s cpu.type should be unset, got %v", id, cpu["type"])
		}
	}
}

// TestGenerateDispatchRequests_NodePrep verifies a per-pool nodePrep block
// stamps cloud-init packages/runcmd onto only that pool's node VMs (via the
// operative create path), leaving other pools' cloudInit untouched.
func TestGenerateDispatchRequests_NodePrep(t *testing.T) {
	spec := &resources.ClusterSpec{
		Compute: resources.ComputeSpec{
			Provider: "proxmox",
			Image:    resources.ImageSpec{Template: "ubuntu-template"},
			Default:  resources.DefaultSizeSpec{CPUs: 2, MemoryMB: 4096, DiskGB: 40},
		},
		Nodes: resources.NodesSpec{
			ControlPlane: resources.ControlPlaneSpec{Count: 1},
			Workers: []resources.WorkerSpec{
				{Name: "general", Count: 1},
				{Name: "storage", Count: 1, NodePrep: &resources.NodePrepSpec{
					Packages: []string{"open-iscsi"},
					RunCmd:   []string{"systemctl enable iscsid"},
				}},
			},
		},
		SSH: resources.SSHSpec{User: "ubuntu"},
	}

	byID := map[string]*protocol.Resource{}
	for _, req := range NewCreator("dev", spec, &protocol.ProviderConfig{}).GenerateDispatchRequests() {
		byID[req.ID] = req.Manifest
	}

	// The storage worker's cloudInit carries the prereqs, preserving user.
	storage := byID["vm-dev-storage-0"]
	if storage == nil {
		t.Fatalf("missing storage node request; got %v", byID)
	}
	ci, _ := storage.Spec["cloudInit"].(map[string]any)
	if ci == nil || ci["user"] != "ubuntu" {
		t.Fatalf("storage cloudInit missing/clobbered: %v", storage.Spec["cloudInit"])
	}
	if pkgs, _ := ci["packages"].([]string); len(pkgs) != 1 || pkgs[0] != "open-iscsi" {
		t.Errorf("storage packages wrong: %v", ci["packages"])
	}
	if cmds, _ := ci["runcmd"].([]string); len(cmds) != 1 || cmds[0] != "systemctl enable iscsid" {
		t.Errorf("storage runcmd wrong: %v", ci["runcmd"])
	}

	// The general worker and control plane must NOT get packages/runcmd.
	for _, id := range []string{"vm-dev-cp-0", "vm-dev-general-0"} {
		m := byID[id]
		if m == nil {
			t.Fatalf("missing %s", id)
		}
		ci, _ := m.Spec["cloudInit"].(map[string]any)
		if ci == nil {
			t.Fatalf("%s missing cloudInit", id)
		}
		if _, ok := ci["packages"]; ok {
			t.Errorf("%s should not have cloudInit.packages", id)
		}
		if _, ok := ci["runcmd"]; ok {
			t.Errorf("%s should not have cloudInit.runcmd", id)
		}
	}
}
