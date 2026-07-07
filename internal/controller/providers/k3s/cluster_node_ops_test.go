package k3s

import (
	"context"
	"strings"
	"testing"

	"github.com/openctl/openctl/internal/controller/providers"
)

// fakeNodeAgent records logs/restart calls and returns canned output.
type fakeNodeAgent struct {
	logsOut    string
	logsErr    error
	restartErr error
	logsFor    []string // node names Logs was called for
	restartFor []string // node names Restart was called for
	lastLines  int
}

func (f *fakeNodeAgent) Logs(_ context.Context, node upgradeNode, lines int) (string, error) {
	f.logsFor = append(f.logsFor, node.Name)
	f.lastLines = lines
	return f.logsOut, f.logsErr
}

func (f *fakeNodeAgent) Restart(_ context.Context, node upgradeNode) error {
	f.restartFor = append(f.restartFor, node.Name)
	return f.restartErr
}

func nodeOpsFixture(t *testing.T, nodes ...string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	writeClusterChildren(t, "dev", nodes)
	for i, n := range nodes {
		role := roleServer
		if i > 0 {
			role = roleAgent
		}
		writeNodeState(t, n, role, "10.0.0.10")
	}
	bundleDir, err := clusterBundleDir("dev")
	if err != nil {
		t.Fatal(err)
	}
	writeBundle(t, bundleDir)
}

func TestSelectNode(t *testing.T) {
	nodes := []upgradeNode{{Name: "a"}, {Name: "b"}}
	// explicit match
	if n, err := selectNode(nodes, "b"); err != nil || n.Name != "b" {
		t.Errorf("explicit select: %v %v", n, err)
	}
	// unknown name errors
	if _, err := selectNode(nodes, "z"); err == nil {
		t.Error("want error for unknown node")
	}
	// empty + multiple → error asking for a node param
	if _, err := selectNode(nodes, ""); err == nil || !strings.Contains(err.Error(), "specify a 'node'") {
		t.Errorf("want 'specify a node' error, got %v", err)
	}
	// empty + single → auto-select
	if n, err := selectNode([]upgradeNode{{Name: "solo"}}, ""); err != nil || n.Name != "solo" {
		t.Errorf("single auto-select: %v %v", n, err)
	}
}

// logs returns the journal as a downloadable file, defaulting lines to 200 and
// auto-selecting the only node.
func TestRunClusterLogs_SingleNodeDownload(t *testing.T) {
	nodeOpsFixture(t, "dev-cp-0")
	fake := &fakeNodeAgent{logsOut: "line1\nline2\n"}
	factory := func(_ agentCertBundle, _ map[string]string) nodeAgent { return fake }

	res, err := (&Provider{}).runClusterLogs(context.Background(), "dev", map[string]string{}, factory)
	if err != nil {
		t.Fatalf("runClusterLogs: %v", err)
	}
	if res.DownloadContent != "line1\nline2\n" {
		t.Errorf("download content = %q", res.DownloadContent)
	}
	if !strings.HasSuffix(res.DownloadFilename, "dev-cp-0-k3s.log") {
		t.Errorf("filename = %q", res.DownloadFilename)
	}
	if fake.lastLines != defaultLogLines {
		t.Errorf("lines = %d, want default %d", fake.lastLines, defaultLogLines)
	}
	if len(fake.logsFor) != 1 || fake.logsFor[0] != "dev-cp-0" {
		t.Errorf("logs fetched for %v", fake.logsFor)
	}
}

// logs honors an explicit node + lines parameter.
func TestRunClusterLogs_NodeAndLinesParams(t *testing.T) {
	nodeOpsFixture(t, "dev-cp-0", "dev-worker-0")
	fake := &fakeNodeAgent{logsOut: "x"}
	factory := func(_ agentCertBundle, _ map[string]string) nodeAgent { return fake }

	_, err := (&Provider{}).runClusterLogs(context.Background(), "dev",
		map[string]string{"node": "dev-worker-0", "lines": "50"}, factory)
	if err != nil {
		t.Fatalf("runClusterLogs: %v", err)
	}
	if fake.logsFor[0] != "dev-worker-0" {
		t.Errorf("node = %v, want dev-worker-0", fake.logsFor)
	}
	if fake.lastLines != 50 {
		t.Errorf("lines = %d, want 50", fake.lastLines)
	}
}

// logs on a multi-node cluster with no node param errors clearly.
func TestRunClusterLogs_MultiNodeNeedsNodeParam(t *testing.T) {
	nodeOpsFixture(t, "dev-cp-0", "dev-worker-0")
	factory := func(_ agentCertBundle, _ map[string]string) nodeAgent { return &fakeNodeAgent{} }
	_, err := (&Provider{}).runClusterLogs(context.Background(), "dev", map[string]string{}, factory)
	if err == nil || !strings.Contains(err.Error(), "specify a 'node'") {
		t.Errorf("want a 'specify a node' error, got %v", err)
	}
}

// restart drives the agent for the named node.
func TestRunClusterRestart(t *testing.T) {
	nodeOpsFixture(t, "dev-cp-0", "dev-worker-0")
	fake := &fakeNodeAgent{}
	factory := func(_ agentCertBundle, _ map[string]string) nodeAgent { return fake }

	res, err := (&Provider{}).runClusterRestart(context.Background(), "dev",
		map[string]string{"node": "dev-cp-0"}, factory)
	if err != nil {
		t.Fatalf("runClusterRestart: %v", err)
	}
	if len(fake.restartFor) != 1 || fake.restartFor[0] != "dev-cp-0" {
		t.Errorf("restart called for %v", fake.restartFor)
	}
	if !strings.Contains(res.Message, "Restarted k3s on dev-cp-0") {
		t.Errorf("message = %q", res.Message)
	}
}

// logs/restart on a cluster with no installed nodes errors.
func TestRunClusterNodeOps_NoNodes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeClusterChildren(t, "dev", []string{"vm-only"}) // no node state
	factory := func(_ agentCertBundle, _ map[string]string) nodeAgent { return &fakeNodeAgent{} }
	if _, err := (&Provider{}).runClusterLogs(context.Background(), "dev", map[string]string{}, factory); err == nil {
		t.Error("logs: want error for no installed nodes")
	}
	if _, err := (&Provider{}).runClusterRestart(context.Background(), "dev", map[string]string{"node": "x"}, factory); err == nil {
		t.Error("restart: want error for no installed nodes")
	}
}

// ActionSpecs advertises logs + restart with their parameter schemas.
func TestActionSpecs_IncludesNodeOps(t *testing.T) {
	specs := (&Provider{}).ActionSpecs(kindCluster)
	byName := map[string][]providers.ActionParameter{}
	for _, s := range specs {
		byName[s.Name] = s.Parameters
	}
	if _, ok := byName["logs"]; !ok {
		t.Error("no logs action spec")
	}
	rst, ok := byName["restart"]
	if !ok {
		t.Fatal("no restart action spec")
	}
	if len(rst) != 1 || rst[0].Name != "node" || !rst[0].Required {
		t.Errorf("restart params = %+v, want one required 'node'", rst)
	}
}
