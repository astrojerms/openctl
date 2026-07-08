package k3s

import (
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

// clusterStateWithEndpoint writes a cluster state file whose agent endpoints
// map the CP to an IP, so survivingCPEndpoint can resolve it.
func clusterStateWithEndpoint(t *testing.T, home, cluster, cpName, cpIP string) {
	t.Helper()
	writeClusterState(t, home, cluster, `apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: `+cluster+`
status:
  outputs:
    agent:
      endpoints:
        `+cpName+`: `+cpIP+`
children:
  - provider: proxmox
    kind: VirtualMachine
    name: `+cpName+`
`)
}

// seedServerNodes writes K3sNode state for control planes, representing a
// cluster built via the Plan/K3sNode path (so setJoin uses a $ref).
func seedServerNodes(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := saveNodeState(&nodeState{Name: n, Role: roleServer, Installed: true, NodeToken: "tok-" + n}); err != nil {
			t.Fatal(err)
		}
	}
}

// When the CP has a K3sNode resource, setJoin uses a $ref (Plan/K3sNode path).
func TestSetJoin_UsesRefWhenK3sNodeExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	clusterStateWithEndpoint(t, home, "dev", "dev-cp-0", "10.0.0.10")
	// A K3sNode resource exists for the CP.
	if err := saveNodeState(&nodeState{Name: "dev-cp-0", Role: roleServer, Installed: true, NodeToken: "tok"}); err != nil {
		t.Fatal(err)
	}

	worker := &protocol.Resource{Spec: map[string]any{}}
	p := &Provider{}
	current := []childRef{{Kind: "VirtualMachine", Name: "dev-cp-0"}}
	if err := p.setJoin(worker, "dev-cp-0", clusterManifest("dev"), "dev", current, nil); err != nil {
		t.Fatalf("setJoin: %v", err)
	}
	jf, _ := worker.Spec["joinFrom"].(map[string]any)
	if jf == nil || jf["$ref"] == nil {
		t.Errorf("joinFrom = %v, want a $ref", worker.Spec["joinFrom"])
	}
}

// Legacy cluster: the CP has no K3sNode resource, so setJoin resolves the token
// concretely (via the readCPNodeToken seam) instead of an unresolvable $ref.
func TestSetJoin_ConcreteForLegacyCP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	clusterStateWithEndpoint(t, home, "dev", "dev-cp-0", "10.0.0.10")
	// No K3sNode state for dev-cp-0 → legacy.

	// Fake the SSH token read.
	orig := readCPNodeToken
	defer func() { readCPNodeToken = orig }()
	var gotHost, gotUser string
	readCPNodeToken = func(host, user, keyPath string) (string, error) {
		gotHost, gotUser = host, user
		return "K10legacy::server:abc", nil
	}

	worker := &protocol.Resource{Spec: map[string]any{}}
	p := &Provider{}
	current := []childRef{{Kind: "VirtualMachine", Name: "dev-cp-0"}}
	if err := p.setJoin(worker, "dev-cp-0", clusterManifest("dev"), "dev", current, nil); err != nil {
		t.Fatalf("setJoin: %v", err)
	}
	if gotHost != "10.0.0.10" || gotUser != "ubuntu" {
		t.Errorf("token read from %s@%s, want ubuntu@10.0.0.10", gotUser, gotHost)
	}
	if worker.Spec["joinFrom"] != "K10legacy::server:abc" {
		t.Errorf("joinFrom = %v, want the concrete token", worker.Spec["joinFrom"])
	}
	if worker.Spec["joinURLFrom"] != "10.0.0.10" {
		t.Errorf("joinURLFrom = %v, want the concrete CP IP", worker.Spec["joinURLFrom"])
	}
}
