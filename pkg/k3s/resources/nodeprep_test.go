package resources

import (
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

// TestParseAndResolveNodePrep covers parsing a per-pool nodePrep spec and
// resolving it to the right node index across the flat control-plane-then-
// workers ordering — mirrors the GPU resolution both VM-build paths rely on.
func TestParseAndResolveNodePrep(t *testing.T) {
	r := &protocol.Resource{Spec: map[string]any{
		"compute": map[string]any{"provider": "proxmox"},
		"nodes": map[string]any{
			"controlPlane": map[string]any{"count": float64(1)},
			"workers": []any{
				map[string]any{"name": "general", "count": float64(2)},
				map[string]any{"name": "storage", "count": float64(1), "nodePrep": map[string]any{
					"packages": []any{"open-iscsi", "nfs-common"},
					"runcmd":   []any{"systemctl enable iscsid"},
				}},
			},
		},
	}}

	spec, err := ParseClusterSpec(r)
	if err != nil {
		t.Fatalf("ParseClusterSpec: %v", err)
	}
	if spec.Nodes.Workers[0].NodePrep != nil {
		t.Errorf("general pool should have no nodePrep")
	}
	np := spec.Nodes.Workers[1].NodePrep
	if np == nil || len(np.Packages) != 2 || np.Packages[0] != "open-iscsi" {
		t.Fatalf("storage pool nodePrep mis-parsed: %+v", np)
	}
	if len(np.RunCmd) != 1 || np.RunCmd[0] != "systemctl enable iscsid" {
		t.Errorf("runcmd mis-parsed: %+v", np.RunCmd)
	}

	// Flat ordering: index 0 = CP, 1-2 = general workers, 3 = storage worker.
	cpCount := 1
	if NodePrepForNode(0, cpCount, spec) != nil {
		t.Errorf("CP node should resolve to no nodePrep")
	}
	if NodePrepForNode(1, cpCount, spec) != nil || NodePrepForNode(2, cpCount, spec) != nil {
		t.Errorf("general workers should resolve to no nodePrep")
	}
	if NodePrepForNode(3, cpCount, spec) != np {
		t.Errorf("storage worker (index 3) should resolve to the storage pool nodePrep")
	}
}

func TestApplyNodePrepToVMSpec(t *testing.T) {
	// Nil is a no-op.
	vm := map[string]any{"cloudInit": map[string]any{"user": "ubuntu"}}
	ApplyNodePrepToVMSpec(vm, nil)
	if _, ok := vm["cloudInit"].(map[string]any)["packages"]; ok {
		t.Errorf("nil nodePrep should be a no-op")
	}

	// Empty spec is also a no-op (no packages, no runcmd).
	ApplyNodePrepToVMSpec(vm, &NodePrepSpec{})
	if _, ok := vm["cloudInit"].(map[string]any)["packages"]; ok {
		t.Errorf("empty nodePrep should be a no-op")
	}

	// Full stamp writes into the existing cloudInit map, preserving user.
	ApplyNodePrepToVMSpec(vm, &NodePrepSpec{
		Packages: []string{"open-iscsi"},
		RunCmd:   []string{"systemctl enable iscsid"},
	})
	ci := vm["cloudInit"].(map[string]any)
	if ci["user"] != "ubuntu" {
		t.Errorf("cloudInit.user should be preserved, got %v", ci["user"])
	}
	if pkgs, ok := ci["packages"].([]string); !ok || len(pkgs) != 1 || pkgs[0] != "open-iscsi" {
		t.Errorf("packages not stamped into cloudInit: %v", ci["packages"])
	}
	if cmds, ok := ci["runcmd"].([]string); !ok || len(cmds) != 1 || cmds[0] != "systemctl enable iscsid" {
		t.Errorf("runcmd not stamped into cloudInit: %v", ci["runcmd"])
	}
}

// TestApplyNodePrepToVMSpec_CreatesCloudInitWhenAbsent proves the stamp is
// robust to a VM spec that somehow lacks a cloudInit map (both build paths
// construct one first, but the helper shouldn't panic if it's missing).
func TestApplyNodePrepToVMSpec_CreatesCloudInitWhenAbsent(t *testing.T) {
	vm := map[string]any{}
	ApplyNodePrepToVMSpec(vm, &NodePrepSpec{Packages: []string{"curl"}})
	ci, ok := vm["cloudInit"].(map[string]any)
	if !ok {
		t.Fatalf("cloudInit should have been created, got %T", vm["cloudInit"])
	}
	if pkgs := ci["packages"].([]string); pkgs[0] != "curl" {
		t.Errorf("packages = %v", pkgs)
	}
}
