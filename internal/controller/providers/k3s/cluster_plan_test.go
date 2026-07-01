package k3s

import (
	"context"
	"testing"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/pkg/protocol"
)

// clusterManifest builds a minimal Cluster manifest for planner tests.
// Every knob has a sensible default; overrides go through the opts
// callback so tests express only what they need to test.
func clusterManifest(name string, opts ...func(*protocol.Resource)) *protocol.Resource {
	r := &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       kindCluster,
		Metadata:   protocol.ResourceMetadata{Name: name},
		Spec: map[string]any{
			"compute": map[string]any{
				"provider": "proxmox",
				"image":    map[string]any{"template": "ubuntu-2204"},
				"default":  map[string]any{"cpus": float64(2), "memoryMB": float64(4096), "diskGB": float64(40)},
			},
			"nodes": map[string]any{
				"controlPlane": map[string]any{"count": float64(1)},
				"workers":      []any{},
			},
			"network": map[string]any{"bridge": "vmbr0", "dhcp": true},
			"k3s":     map[string]any{},
			"ssh": map[string]any{
				"user":           "ubuntu",
				"privateKeyPath": "/root/.ssh/id_ed25519",
				"publicKeys":     []any{},
			},
		},
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// planFor runs Provider.Plan against a manifest and returns the child
// list, failing the test on any error.
func planFor(t *testing.T, m *protocol.Resource) []*protocol.Resource {
	t.Helper()
	p := &Provider{}
	result, err := p.Plan(context.Background(), m)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if result == nil {
		t.Fatal("Plan returned nil result")
	}
	return result.Children
}

func countByKind(children []*protocol.Resource) map[string]int {
	out := map[string]int{}
	for _, c := range children {
		out[c.Kind]++
	}
	return out
}

func findByKindName(children []*protocol.Resource, kind, name string) *protocol.Resource {
	for _, c := range children {
		if c.Kind == kind && c.Metadata.Name == name {
			return c
		}
	}
	return nil
}

func TestPlan_SingleCP_NoWorkers(t *testing.T) {
	children := planFor(t, clusterManifest("dev"))
	counts := countByKind(children)
	if counts["VirtualMachine"] != 1 || counts[kindK3sNode] != 1 || counts[kindAgentInstall] != 1 {
		t.Errorf("expected 1 of each kind for single-node cluster, got %+v", counts)
	}
	// First CP K3sNode: no joinFrom, no joinURLFrom.
	k3s := findByKindName(children, kindK3sNode, "dev-cp-0")
	if k3s == nil {
		t.Fatal("dev-cp-0 K3sNode missing from plan")
	}
	if _, has := k3s.Spec["joinFrom"]; has {
		t.Errorf("first server K3sNode must not have joinFrom, got: %+v", k3s.Spec["joinFrom"])
	}
	if _, has := k3s.Spec["joinURLFrom"]; has {
		t.Errorf("first server K3sNode must not have joinURLFrom, got: %+v", k3s.Spec["joinURLFrom"])
	}
	if k3s.Spec["role"] != "server" {
		t.Errorf("first CP role should be server, got %q", k3s.Spec["role"])
	}
}

func TestPlan_HAThreeControlPlane(t *testing.T) {
	m := clusterManifest("prod", func(r *protocol.Resource) {
		r.Spec["nodes"].(map[string]any)["controlPlane"].(map[string]any)["count"] = float64(3)
	})
	children := planFor(t, m)
	counts := countByKind(children)
	if counts["VirtualMachine"] != 3 || counts[kindK3sNode] != 3 || counts[kindAgentInstall] != 3 {
		t.Errorf("HA cluster: expected 3 each, got %+v", counts)
	}
	// Second + third CP: join the first CP.
	for _, name := range []string{"prod-cp-1", "prod-cp-2"} {
		k3s := findByKindName(children, kindK3sNode, name)
		if k3s == nil {
			t.Fatalf("%s K3sNode missing", name)
		}
		if k3s.Spec["role"] != "server" {
			t.Errorf("%s role should be server, got %q", name, k3s.Spec["role"])
		}
		join, ok := k3s.Spec["joinFrom"].(map[string]any)
		if !ok {
			t.Fatalf("%s joinFrom missing or wrong shape: %+v", name, k3s.Spec["joinFrom"])
		}
		ref, ok := join["$ref"].(map[string]any)
		if !ok {
			t.Fatalf("%s joinFrom.$ref missing: %+v", name, join)
		}
		if ref["name"] != "prod-cp-0" {
			t.Errorf("%s joinFrom should point at prod-cp-0, got %v", name, ref["name"])
		}
		if ref["field"] != "status.nodeToken" {
			t.Errorf("%s joinFrom should target status.nodeToken, got %v", name, ref["field"])
		}
	}
}

func TestPlan_WorkersJoinAsAgents(t *testing.T) {
	m := clusterManifest("prod", func(r *protocol.Resource) {
		r.Spec["nodes"].(map[string]any)["workers"] = []any{
			map[string]any{"name": "worker", "count": float64(2)},
		}
	})
	children := planFor(t, m)
	counts := countByKind(children)
	// 1 CP + 2 workers = 3 VMs, 3 K3sNodes, 3 AgentInstalls
	if counts["VirtualMachine"] != 3 || counts[kindK3sNode] != 3 || counts[kindAgentInstall] != 3 {
		t.Errorf("cluster with 2 workers: expected 3 each, got %+v", counts)
	}
	for _, name := range []string{"prod-worker-0", "prod-worker-1"} {
		k3s := findByKindName(children, kindK3sNode, name)
		if k3s == nil {
			t.Fatalf("%s K3sNode missing", name)
		}
		if k3s.Spec["role"] != "agent" {
			t.Errorf("%s role should be agent, got %q", name, k3s.Spec["role"])
		}
	}
}

func TestPlan_AgentInstallsPointAtOwnVM(t *testing.T) {
	m := clusterManifest("dev", func(r *protocol.Resource) {
		r.Spec["nodes"].(map[string]any)["controlPlane"].(map[string]any)["count"] = float64(2)
	})
	children := planFor(t, m)
	// Every AgentInstall's vmRef must point at a VM matching its
	// derived-from name (e.g. dev-cp-0 → dev-cp-0-agent).
	for _, c := range children {
		if c.Kind != kindAgentInstall {
			continue
		}
		vmRef, ok := c.Spec["vmRef"].(map[string]any)
		if !ok {
			t.Fatalf("AgentInstall %s missing vmRef", c.Metadata.Name)
		}
		ref, ok := vmRef["$ref"].(map[string]any)
		if !ok {
			t.Fatalf("AgentInstall %s vmRef missing $ref: %+v", c.Metadata.Name, vmRef)
		}
		// Name pattern: <vm-name>-agent → vm-name = strip trailing "-agent"
		wantVM := c.Metadata.Name[:len(c.Metadata.Name)-len("-agent")]
		if ref["name"] != wantVM {
			t.Errorf("AgentInstall %s vmRef should point at %s, got %v", c.Metadata.Name, wantVM, ref["name"])
		}
		if c.Spec["clusterName"] != "dev" {
			t.Errorf("AgentInstall %s clusterName should be dev, got %v", c.Metadata.Name, c.Spec["clusterName"])
		}
	}
}

func TestPlan_OwnerLabelsAttributeToParent(t *testing.T) {
	children := planFor(t, clusterManifest("dev"))
	for _, c := range children {
		if c.Metadata.Labels[providers.LabelOwnerKind] != kindCluster {
			t.Errorf("%s %s missing owner-kind label", c.Kind, c.Metadata.Name)
		}
		if c.Metadata.Labels[providers.LabelOwnerName] != "dev" {
			t.Errorf("%s %s owner-name = %q, want dev", c.Kind, c.Metadata.Name, c.Metadata.Labels[providers.LabelOwnerName])
		}
	}
}

func TestPlan_StaticIPsFlowThrough(t *testing.T) {
	m := clusterManifest("dev", func(r *protocol.Resource) {
		net := r.Spec["network"].(map[string]any)
		net["dhcp"] = false
		net["staticIPs"] = map[string]any{
			"startIP": "192.168.1.100",
			"gateway": "192.168.1.1",
			"netmask": "24",
		}
	})
	children := planFor(t, m)
	vm := findByKindName(children, "VirtualMachine", "dev-cp-0")
	if vm == nil {
		t.Fatal("dev-cp-0 VM missing")
	}
	ci, ok := vm.Spec["cloudInit"].(map[string]any)
	if !ok {
		t.Fatalf("cloudInit missing on VM: %+v", vm.Spec)
	}
	ipCfg, ok := ci["ipConfig"].(map[string]any)
	if !ok {
		t.Fatalf("ipConfig missing: %+v", ci)
	}
	net0, ok := ipCfg["net0"].(map[string]any)
	if !ok {
		t.Fatalf("net0 missing: %+v", ipCfg)
	}
	if net0["ip"] != "192.168.1.100/24" {
		t.Errorf("static IP not wired: got %v", net0["ip"])
	}
	if net0["gateway"] != "192.168.1.1" {
		t.Errorf("gateway not wired: got %v", net0["gateway"])
	}
}

func TestPlan_StaticIPsBakedIntoK3sNodeAndAgentInstall(t *testing.T) {
	// Static-IP clusters: the K3sNode and AgentInstall child
	// manifests carry `spec.vmIP` populated from AllocateIPs, so
	// applyK3sNode / applyAgentInstall can skip the QGA-based
	// waitForVMIP poll (which would hang forever if QGA isn't in
	// the guest template). The original homelab validation hit
	// this — the whole point of this fix.
	m := clusterManifest("dev", func(r *protocol.Resource) {
		r.Spec["nodes"].(map[string]any)["controlPlane"].(map[string]any)["count"] = float64(2)
		net := r.Spec["network"].(map[string]any)
		net["dhcp"] = false
		net["staticIPs"] = map[string]any{
			"startIP": "192.168.1.100",
			"gateway": "192.168.1.1",
			"netmask": "24",
		}
	})
	children := planFor(t, m)

	// Each K3sNode / AgentInstall for this static-IP cluster
	// should have `spec.vmIP` set to the deterministic AllocateIPs
	// assignment.
	wantIPs := map[string]string{
		"dev-cp-0":       "192.168.1.100",
		"dev-cp-1":       "192.168.1.101",
		"dev-cp-0-agent": "192.168.1.100",
		"dev-cp-1-agent": "192.168.1.101",
	}
	for _, c := range children {
		if c.Kind != kindK3sNode && c.Kind != kindAgentInstall {
			continue
		}
		want, expected := wantIPs[c.Metadata.Name]
		if !expected {
			continue
		}
		got, _ := c.Spec["vmIP"].(string)
		if got != want {
			t.Errorf("%s %s spec.vmIP = %q, want %q", c.Kind, c.Metadata.Name, got, want)
		}
	}
}

func TestPlan_DHCPClusterDoesNotBakeVMIP(t *testing.T) {
	// Complement of the previous test: a DHCP cluster (no
	// staticIPs) must NOT set spec.vmIP. Runtime path is required
	// because IPs are only known after QGA reports them; setting
	// vmIP to "" would just be misleading noise.
	m := clusterManifest("dev") // default has network.dhcp=true
	children := planFor(t, m)
	for _, c := range children {
		if c.Kind != kindK3sNode && c.Kind != kindAgentInstall {
			continue
		}
		if _, has := c.Spec["vmIP"]; has {
			t.Errorf("%s %s should NOT have vmIP set on a DHCP cluster; got %+v", c.Kind, c.Metadata.Name, c.Spec["vmIP"])
		}
	}
}

func TestPlan_RejectsNonClusterKind(t *testing.T) {
	m := &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       kindK3sNode,
		Metadata:   protocol.ResourceMetadata{Name: "foo"},
	}
	p := &Provider{}
	_, err := p.Plan(context.Background(), m)
	if err == nil {
		t.Fatal("expected error planning K3sNode (not composable), got nil")
	}
}

func TestPlan_K3sVersionAndExtraArgsFlowThrough(t *testing.T) {
	m := clusterManifest("dev", func(r *protocol.Resource) {
		r.Spec["k3s"] = map[string]any{
			"version":   "v1.29.0+k3s1",
			"extraArgs": []any{"--disable=traefik", "--cluster-cidr=10.42.0.0/16"},
		}
	})
	children := planFor(t, m)
	k3s := findByKindName(children, kindK3sNode, "dev-cp-0")
	if k3s == nil {
		t.Fatal("dev-cp-0 K3sNode missing")
	}
	if k3s.Spec["version"] != "v1.29.0+k3s1" {
		t.Errorf("version not wired: %v", k3s.Spec["version"])
	}
	extra, ok := k3s.Spec["extraArgs"].([]any)
	if !ok || len(extra) != 2 {
		t.Errorf("extraArgs not wired: %+v", k3s.Spec["extraArgs"])
	}
}
