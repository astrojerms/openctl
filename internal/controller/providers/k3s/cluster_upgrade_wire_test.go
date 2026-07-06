package k3s

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openctl/openctl/pkg/k3s/agent/certs"
)

// writeClusterChildren fabricates the Cluster state file (VM children) that
// readChildren reads, under the test's HOME-redirected state dir.
func writeClusterChildren(t *testing.T, cluster string, nodeNames []string) {
	t.Helper()
	dir, err := stateDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("children:\n")
	for _, n := range nodeNames {
		b.WriteString("  - provider: proxmox\n    kind: VirtualMachine\n    name: ")
		b.WriteString(n)
		b.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(dir, cluster+".yaml"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeNodeState(t *testing.T, name, role, ip string) {
	t.Helper()
	if err := saveNodeState(&nodeState{Name: name, VMName: name, VMIP: ip, Role: role, Installed: true}); err != nil {
		t.Fatalf("saveNodeState %s: %v", name, err)
	}
}

func TestEnumerateUpgradeNodes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeClusterChildren(t, "dev", []string{"dev-cp-0", "dev-cp-1", "dev-worker-0", "never-installed"})
	writeNodeState(t, "dev-cp-0", roleServer, "10.0.0.10")
	writeNodeState(t, "dev-cp-1", roleServer, "10.0.0.11")
	writeNodeState(t, "dev-worker-0", roleAgent, "10.0.0.20")
	// "never-installed" has a VM child but no node state → must be skipped.

	nodes, endpoints, err := enumerateUpgradeNodes("dev")
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	if len(nodes) != 3 {
		t.Fatalf("got %d nodes, want 3 (never-installed skipped): %+v", len(nodes), nodes)
	}
	roleByName := map[string]string{}
	for _, n := range nodes {
		roleByName[n.Name] = n.Role
	}
	if roleByName["dev-cp-0"] != roleServer || roleByName["dev-worker-0"] != roleAgent {
		t.Errorf("roles wrong: %v", roleByName)
	}
	if !strings.HasPrefix(endpoints["dev-cp-0"], "10.0.0.10:") {
		t.Errorf("endpoint for dev-cp-0 = %q, want 10.0.0.10:<port>", endpoints["dev-cp-0"])
	}
	if _, ok := endpoints["never-installed"]; ok {
		t.Error("never-installed should have no endpoint")
	}
}

func TestRunClusterUpgrade_RejectsEmptyVersion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := &Provider{}
	if _, err := p.runClusterUpgrade(context.Background(), "dev", "", productionUpgraderFactory); err == nil {
		t.Fatal("expected an error for an empty version")
	}
}

func TestRunClusterUpgrade_NoNodes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeClusterChildren(t, "dev", []string{"vm-only"}) // no node state → no installed nodes
	p := &Provider{}
	_, err := p.runClusterUpgrade(context.Background(), "dev", "v2", productionUpgraderFactory)
	if err == nil || !strings.Contains(err.Error(), "no installed nodes") {
		t.Fatalf("want 'no installed nodes' error, got %v", err)
	}
}

// Happy path with an injected fake upgrader: enumeration + bundle load + the
// rolling-upgrade core run end to end, with the fake standing in for live
// agents. Proves the wiring passes the right nodes/version through.
func TestRunClusterUpgrade_DrivesRollingUpgrade(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeClusterChildren(t, "dev", []string{"dev-cp-0", "dev-worker-0"})
	writeNodeState(t, "dev-cp-0", roleServer, "10.0.0.10")
	writeNodeState(t, "dev-worker-0", roleAgent, "10.0.0.20")

	// A real bundle on disk (runClusterUpgrade loads it before building the
	// upgrader). We inject a fake upgrader so no live agent is contacted.
	bundleDir, err := clusterBundleDir("dev")
	if err != nil {
		t.Fatal(err)
	}
	writeBundle(t, bundleDir)

	fake := &fakeUpgrader{}
	var gotEndpoints map[string]string
	factory := func(_ agentCertBundle, endpoints map[string]string) nodeUpgrader {
		gotEndpoints = endpoints
		return fake
	}

	p := &Provider{}
	res, err := p.runClusterUpgrade(context.Background(), "dev", "v2", factory)
	if err != nil {
		t.Fatalf("runClusterUpgrade: %v", err)
	}
	// CP before worker (upgradeOrder), both upgraded.
	if len(fake.upgraded) != 2 || fake.upgraded[0] != "dev-cp-0" || fake.upgraded[1] != "dev-worker-0" {
		t.Errorf("upgrade order = %v, want [dev-cp-0 dev-worker-0]", fake.upgraded)
	}
	if !strings.HasPrefix(gotEndpoints["dev-cp-0"], "10.0.0.10:") {
		t.Errorf("factory got endpoints %v", gotEndpoints)
	}
	if !strings.Contains(res.Message, "upgraded 2 node(s) to v2") {
		t.Errorf("result message = %q", res.Message)
	}
}

func writeBundle(t *testing.T, dir string) {
	t.Helper()
	bundle, err := certs.GenerateBundle("dev", []certs.NodeIdentity{{Name: "dev-cp-0", IP: "10.0.0.10"}})
	if err != nil {
		t.Fatalf("GenerateBundle: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"ca.pem":     bundle.CACertPEM,
		"ca.key":     bundle.CAKeyPEM,
		"client.pem": bundle.ClientCertPEM,
		"client.key": bundle.ClientKeyPEM,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
