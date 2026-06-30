package form

import (
	"testing"
)

func TestBuildForKindReturnsClusterForm(t *testing.T) {
	f, ok, err := BuildForKind("k3s.openctl.io/v1", "Cluster")
	if err != nil {
		t.Fatalf("BuildForKind: %v", err)
	}
	if !ok {
		t.Fatal("BuildForKind ok=false; expected k3s Cluster schema to be present")
	}
	if f.Type != FieldObject {
		t.Fatalf("root type = %s, want object", f.Type)
	}
	top := byName(f.Fields)
	// #Resource top-level structure: apiVersion (const), kind (const),
	// metadata, spec.
	if top["apiVersion"].Const != "k3s.openctl.io/v1" {
		t.Errorf("apiVersion const = %v, want \"k3s.openctl.io/v1\"", top["apiVersion"].Const)
	}
	if top["kind"].Const != "Cluster" {
		t.Errorf("kind const = %v, want \"Cluster\"", top["kind"].Const)
	}

	spec := top["spec"]
	if spec.Type != FieldObject {
		t.Fatalf("spec type = %s, want object", spec.Type)
	}
	specFields := byName(spec.Fields)

	// Spec.nodes.controlPlane.count: bounded int with default.
	nodes := specFields["nodes"]
	if nodes.Type != FieldObject {
		t.Fatalf("nodes type = %s, want object", nodes.Type)
	}
	cp := byName(nodes.Fields)["controlPlane"]
	cpFields := byName(cp.Fields)
	count := cpFields["count"]
	if count.Type != FieldInt {
		t.Errorf("controlPlane.count type = %s, want int", count.Type)
	}
	if count.Min == nil || *count.Min != 1 {
		t.Errorf("controlPlane.count min = %v, want 1", count.Min)
	}

	// Spec.nodes.workers: optional array of struct.
	workers := byName(nodes.Fields)["workers"]
	if !workers.Optional {
		t.Error("nodes.workers should be optional")
	}
	if workers.Type != FieldArray {
		t.Fatalf("workers type = %s, want array", workers.Type)
	}
	if workers.Items == nil || workers.Items.Type != FieldObject {
		t.Errorf("workers.items = %+v, want object", workers.Items)
	}

	// Spec.ssh.user: string with default "ubuntu".
	ssh := byName(spec.Fields)["ssh"]
	user := byName(ssh.Fields)["user"]
	if user.Default != "ubuntu" {
		t.Errorf("ssh.user default = %v, want \"ubuntu\"", user.Default)
	}

	// controlPlane.size is now a structured object (was FieldAny before
	// #NodeSize). Its memoryMB should carry the >=512 bound.
	cpSize := cpFields["size"]
	if cpSize.Type != FieldObject {
		t.Errorf("controlPlane.size type = %s, want object (#NodeSize)", cpSize.Type)
	}
	cpSizeFields := byName(cpSize.Fields)
	mem := cpSizeFields["memoryMB"]
	if mem.Min == nil || *mem.Min != 512 {
		t.Errorf("controlPlane.size.memoryMB min = %v, want 512", mem.Min)
	}

	// workers[].count has Min 1.
	workerItem := byName(workers.Items.Fields)
	workerCount := workerItem["count"]
	if workerCount.Min == nil || *workerCount.Min != 1 {
		t.Errorf("workers[].count min = %v, want 1", workerCount.Min)
	}

	// network.dhcp defaults to true.
	network := specFields["network"]
	if network.Type != FieldObject {
		t.Fatalf("network type = %s, want object", network.Type)
	}
	dhcp := byName(network.Fields)["dhcp"]
	if dhcp.Default != true {
		t.Errorf("network.dhcp default = %v, want true", dhcp.Default)
	}

	// Docs propagate from CUE comments.
	if specFields["compute"].Description == "" {
		t.Error("compute should have a description from CUE comments")
	}
}

func TestBuildForKindReturnsVMForm(t *testing.T) {
	f, ok, err := BuildForKind("proxmox.openctl.io/v1", "VirtualMachine")
	if err != nil {
		t.Fatalf("BuildForKind: %v", err)
	}
	if !ok {
		t.Fatal("BuildForKind ok=false; expected VirtualMachine schema to be present")
	}
	top := byName(f.Fields)
	spec := top["spec"]
	specFields := byName(spec.Fields)

	// osType is a documented enum.
	osType := specFields["osType"]
	if osType.Type != FieldString {
		t.Errorf("osType type = %s, want string", osType.Type)
	}
	if len(osType.Enum) == 0 {
		t.Error("osType should have enum entries")
	}
	// Description comes from CUE docs (// ...).
	if osType.Description == "" {
		t.Error("osType should have a description from CUE comments")
	}

	// bios enum.
	bios := specFields["bios"]
	if len(bios.Enum) != 2 || bios.Enum[0] != "seabios" || bios.Enum[1] != "ovmf" {
		t.Errorf("bios enum = %v, want [seabios ovmf]", bios.Enum)
	}

	// cpu.sockets has default 1.
	cpu := specFields["cpu"]
	cpuFields := byName(cpu.Fields)
	sockets := cpuFields["sockets"]
	if sockets.Default != int64(1) && sockets.Default != 1 && sockets.Default != float64(1) {
		t.Errorf("sockets default = %v (%T), want 1", sockets.Default, sockets.Default)
	}

	// networks[].vlan has bounds 1..4094.
	networks := specFields["networks"]
	if networks.Items == nil {
		t.Fatal("networks.items is nil")
	}
	netFields := byName(networks.Items.Fields)
	vlan := netFields["vlan"]
	if vlan.Min == nil || *vlan.Min != 1 {
		t.Errorf("vlan min = %v, want 1", vlan.Min)
	}
	if vlan.Max == nil || *vlan.Max != 4094 {
		t.Errorf("vlan max = %v, want 4094", vlan.Max)
	}

	// cloudInit.ipConfig is a map of string→object.
	ci := specFields["cloudInit"]
	ciFields := byName(ci.Fields)
	ipConfig := ciFields["ipConfig"]
	if ipConfig.Type != FieldMap {
		t.Errorf("ipConfig type = %s, want map", ipConfig.Type)
	}
}

func TestBuildForKindUnknownReturnsNotFound(t *testing.T) {
	_, ok, err := BuildForKind("unknown.openctl.io/v1", "Bogus")
	if err != nil {
		t.Fatalf("BuildForKind: %v", err)
	}
	if ok {
		t.Error("BuildForKind ok=true for unknown kind; want false")
	}
}
