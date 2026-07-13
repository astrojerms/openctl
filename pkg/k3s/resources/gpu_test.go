package resources

import (
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

// TestParseAndResolveGPU covers parsing a per-pool GPU spec and resolving it to
// the right node index across the flat control-plane-then-workers ordering.
func TestParseAndResolveGPU(t *testing.T) {
	r := &protocol.Resource{Spec: map[string]any{
		"compute": map[string]any{"provider": "proxmox"},
		"nodes": map[string]any{
			"controlPlane": map[string]any{"count": float64(1)},
			"workers": []any{
				map[string]any{"name": "general", "count": float64(2)},
				map[string]any{"name": "gpu", "count": float64(1), "gpu": map[string]any{
					"efiStorage": "local-lvm",
					"cpuType":    "host",
					"devices": []any{
						map[string]any{"mapping": "rtx4090", "primaryGPU": true},
						map[string]any{"device": "0000:02:00.0", "mdev": "nvidia-35"},
					},
				}},
			},
		},
	}}

	spec, err := ParseClusterSpec(r)
	if err != nil {
		t.Fatalf("ParseClusterSpec: %v", err)
	}
	if spec.Nodes.Workers[0].GPU != nil {
		t.Errorf("general pool should have no GPU")
	}
	g := spec.Nodes.Workers[1].GPU
	if g == nil || g.EFIStorage != "local-lvm" || len(g.Devices) != 2 {
		t.Fatalf("gpu pool spec mis-parsed: %+v", g)
	}
	if g.Devices[0].Mapping != "rtx4090" || !g.Devices[0].PrimaryGPU {
		t.Errorf("device[0] mis-parsed: %+v", g.Devices[0])
	}
	if g.Devices[1].Device != "0000:02:00.0" || g.Devices[1].MDev != "nvidia-35" {
		t.Errorf("device[1] mis-parsed: %+v", g.Devices[1])
	}

	// Flat ordering: index 0 = CP, 1-2 = general workers, 3 = gpu worker.
	cpCount := 1
	if GPUForNode(0, cpCount, spec) != nil {
		t.Errorf("CP node should resolve to no GPU")
	}
	if GPUForNode(1, cpCount, spec) != nil || GPUForNode(2, cpCount, spec) != nil {
		t.Errorf("general workers should resolve to no GPU")
	}
	if GPUForNode(3, cpCount, spec) != g {
		t.Errorf("gpu worker (index 3) should resolve to the gpu pool spec")
	}
}

func TestApplyGPUToVMSpec(t *testing.T) {
	// Nil is a no-op.
	vm := map[string]any{"cpu": map[string]any{"cores": 4}}
	ApplyGPUToVMSpec(vm, nil)
	if _, ok := vm["machine"]; ok {
		t.Errorf("nil GPU should be a no-op")
	}

	// Full stamp preserves cpu.cores, defaults cpuType, drops empty devices.
	ApplyGPUToVMSpec(vm, &GPUSpec{
		EFIStorage: "zfs",
		Devices: []PCIDevice{
			{Device: "0000:01:00", PrimaryGPU: true},
			{}, // empty → skipped
			{Mapping: "nic"},
		},
	})
	if vm["machine"] != "q35" || vm["bios"] != "ovmf" {
		t.Errorf("machine/bios not set: %v %v", vm["machine"], vm["bios"])
	}
	cpu := vm["cpu"].(map[string]any)
	if cpu["cores"] != 4 || cpu["type"] != "host" {
		t.Errorf("cpu wrong (want cores=4,type=host): %v", cpu)
	}
	if efi := vm["efiDisk"].(map[string]any); efi["storage"] != "zfs" || efi["type"] != "4m" {
		t.Errorf("efiDisk wrong: %v", vm["efiDisk"])
	}
	devs := vm["hostPCI"].([]map[string]any)
	if len(devs) != 2 {
		t.Fatalf("want 2 devices (empty skipped), got %d: %v", len(devs), devs)
	}
	if devs[0]["device"] != "0000:01:00" || devs[0]["pcie"] != true || devs[0]["primaryGPU"] != true {
		t.Errorf("device[0] wrong: %v", devs[0])
	}
	if devs[1]["mapping"] != "nic" || devs[1]["pcie"] != true {
		t.Errorf("device[1] wrong: %v", devs[1])
	}
}
