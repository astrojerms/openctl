package templates

import (
	"testing"
)

func TestDefaultRegistryHasStarters(t *testing.T) {
	r := Default()
	want := map[string]bool{
		"ubuntu-server-vm":  true,
		"small-k3s-cluster": true,
	}
	for _, tpl := range r.All() {
		delete(want, tpl.Name)
	}
	if len(want) != 0 {
		t.Errorf("missing built-in templates: %v", want)
	}
}

func TestRegistryOrderIsStable(t *testing.T) {
	r := Default()
	first := r.All()
	second := r.All()
	if len(first) != len(second) {
		t.Fatal("registry returned different lengths")
	}
	for i := range first {
		if first[i].Name != second[i].Name {
			t.Errorf("order changed: [%d] %s vs %s", i, first[i].Name, second[i].Name)
		}
	}
}

func TestRenderUbuntuVM(t *testing.T) {
	r := Default()
	res, err := r.Render("ubuntu-server-vm", map[string]any{
		"name":   "vm-test",
		"node":   "pve1",
		"sshKey": "ssh-ed25519 AAAA... test@example",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if res.APIVersion != "proxmox.openctl.io/v1" || res.Kind != "VirtualMachine" {
		t.Errorf("unexpected apiVersion/kind: %s/%s", res.APIVersion, res.Kind)
	}
	if res.Metadata.Name != "vm-test" {
		t.Errorf("Metadata.Name = %q, want vm-test", res.Metadata.Name)
	}
	if res.Metadata.Annotations["openctl.io/template"] != "ubuntu-server-vm" {
		t.Errorf("template annotation missing: %v", res.Metadata.Annotations)
	}
	// Defaults applied (size=small → 2 cores; diskGB default 32).
	cpu, _ := res.Spec["cpu"].(map[string]any)
	if cpu["cores"] != 2 {
		t.Errorf("default cpu.cores = %v, want 2", cpu["cores"])
	}
	disks, _ := res.Spec["disks"].([]any)
	if len(disks) != 1 {
		t.Fatalf("expected 1 disk, got %d", len(disks))
	}
	disk := disks[0].(map[string]any)
	if disk["size"] != "32G" {
		t.Errorf("default disk size = %v, want 32G", disk["size"])
	}
}

func TestRenderMissingRequiredParam(t *testing.T) {
	r := Default()
	_, err := r.Render("ubuntu-server-vm", map[string]any{
		"node":   "pve1",
		"sshKey": "ssh-ed25519 AAAA...",
		// name missing — required, no default
	})
	if err == nil {
		t.Error("expected error for missing required param, got nil")
	}
}

func TestRenderBadEnum(t *testing.T) {
	r := Default()
	_, err := r.Render("ubuntu-server-vm", map[string]any{
		"name":   "vm-test",
		"node":   "pve1",
		"sshKey": "ssh-ed25519 AAAA...",
		"size":   "colossal", // not in enum
	})
	if err == nil {
		t.Error("expected error for bad enum, got nil")
	}
}

func TestRenderUnknownTemplate(t *testing.T) {
	r := Default()
	_, err := r.Render("does-not-exist", map[string]any{})
	if err == nil {
		t.Error("expected error for unknown template, got nil")
	}
}

func TestRenderSmallK3sCluster(t *testing.T) {
	r := Default()
	res, err := r.Render("small-k3s-cluster", map[string]any{
		"name":              "dev",
		"node":              "pve1",
		"startIP":           "192.168.1.100",
		"gateway":           "192.168.1.1",
		"sshPrivateKeyPath": "/root/.ssh/id_ed25519",
		"sshPublicKey":      "ssh-ed25519 AAAA...",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	spec := res.Spec
	// Defaults: controlPlaneCount=1, workerCount=2, size=small.
	nodes, _ := spec["nodes"].(map[string]any)
	cp, _ := nodes["controlPlane"].(map[string]any)
	if cp["count"] != 1 {
		t.Errorf("default controlPlane.count = %v, want 1", cp["count"])
	}
	workers, _ := nodes["workers"].([]any)
	if len(workers) != 1 {
		t.Fatalf("expected 1 worker pool, got %d", len(workers))
	}
	w0 := workers[0].(map[string]any)
	if w0["count"] != 2 {
		t.Errorf("default worker pool count = %v, want 2", w0["count"])
	}
	// Static IPs wired through from params.
	network, _ := spec["network"].(map[string]any)
	staticIPs, _ := network["staticIPs"].(map[string]any)
	if staticIPs["startIP"] != "192.168.1.100" {
		t.Errorf("startIP not wired through: %v", staticIPs["startIP"])
	}
}
