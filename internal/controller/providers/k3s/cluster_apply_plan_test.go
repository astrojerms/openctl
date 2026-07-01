package k3s

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openctl/openctl/pkg/protocol"
)

// osReadFile shadows os.ReadFile for the test file so readFile can
// remain a one-liner without polluting the top of the file with
// os-only imports.
var osReadFile = os.ReadFile

// recordingChildDispatcher captures each ApplyChild call and can
// stub K3sNode dispatches by writing the state file applyK3sNode
// would have written — so applyClusterViaPlan's post-phase-2 steps
// (materialize CA bundle, save cluster state) can proceed as if the
// child work had run.
type recordingChildDispatcher struct {
	mu       sync.Mutex
	calls    []*protocol.Resource
	writeK3s bool // if true, K3sNode child calls persist a state file
}

func (r *recordingChildDispatcher) ApplyChild(_ context.Context, m *protocol.Resource) (*protocol.Resource, error) {
	r.mu.Lock()
	r.calls = append(r.calls, m)
	r.mu.Unlock()
	if r.writeK3s && m.Kind == kindK3sNode {
		// Extract vmName from vmRef $ref (Plan output shape) and
		// synthesize an IP. Same shape applyK3sNode would write.
		vmName := ""
		if vmRef, ok := m.Spec["vmRef"].(map[string]any); ok {
			if ref, ok := vmRef["$ref"].(map[string]any); ok {
				vmName, _ = ref["name"].(string)
			}
		}
		state := &nodeState{
			Name:        m.Metadata.Name,
			VMName:      vmName,
			VMIP:        fmt.Sprintf("10.0.0.%d", len(r.calls)),
			Role:        m.Spec["role"].(string),
			Installed:   true,
			InstalledAt: time.Now(),
		}
		// First server: also stash a nodeToken so subsequent K3sNodes'
		// joinFrom refs would resolve to something (we don't actually
		// re-resolve here since the ChildDispatcher doesn't run refs).
		if _, hasJoin := m.Spec["joinFrom"]; !hasJoin && state.Role == "server" {
			state.NodeToken = "K10::first-server-token"
			state.Kubeconfig = "apiVersion: v1\nkind: Config\n"
		}
		if err := saveNodeState(state); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// kindsInOrder returns the ordered list of kinds seen by the
// dispatcher — used to assert Plan-phase ordering.
func (r *recordingChildDispatcher) kindsInOrder() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.calls))
	for _, c := range r.calls {
		out = append(out, c.Kind)
	}
	return out
}

func TestApplyClusterViaPlan_DispatchOrder(t *testing.T) {
	// applyClusterViaPlan writes state under ~/.openctl/state — pin
	// HOME to a temp dir so we don't touch the real filesystem.
	t.Setenv("HOME", t.TempDir())

	cd := &recordingChildDispatcher{writeK3s: true}
	p := &Provider{}
	m := clusterManifest("dev", func(r *protocol.Resource) {
		r.Spec["nodes"].(map[string]any)["controlPlane"].(map[string]any)["count"] = float64(3)
		r.Spec["nodes"].(map[string]any)["workers"] = []any{
			map[string]any{"name": "worker", "count": float64(2)},
		}
	})
	_, err := p.applyClusterViaPlan(context.Background(), m, cd)
	if err != nil {
		t.Fatalf("applyClusterViaPlan: %v", err)
	}

	// Expected: 5 VMs (3 CP + 2 worker), then 5 K3sNodes, then 5
	// AgentInstalls. That ordering is the whole point of the
	// three-phase design — VMs must exist before K3sNodes install,
	// K3sNodes must install before the CA bundle is materialized
	// for AgentInstalls to pick up.
	kinds := cd.kindsInOrder()
	if len(kinds) != 15 {
		t.Fatalf("expected 15 total ApplyChild calls (5 VM + 5 K3sNode + 5 AgentInstall), got %d: %v", len(kinds), kinds)
	}
	// First 5 must all be VMs.
	for i, k := range kinds[:5] {
		if k != "VirtualMachine" {
			t.Errorf("call[%d] should be VirtualMachine, got %q", i, k)
		}
	}
	// Next 5 must all be K3sNode.
	for i, k := range kinds[5:10] {
		if k != kindK3sNode {
			t.Errorf("call[%d] should be K3sNode, got %q", i+5, k)
		}
	}
	// Final 5 must all be AgentInstall.
	for i, k := range kinds[10:] {
		if k != kindAgentInstall {
			t.Errorf("call[%d] should be AgentInstall, got %q", i+10, k)
		}
	}
}

func TestApplyClusterViaPlan_MaterializesCABundle(t *testing.T) {
	// Verify the CA bundle actually shows up on disk between K3sNodes
	// and AgentInstalls. Without this file, AgentInstall.LoadBundle
	// would fail — this is a load-bearing side effect of the plan
	// path.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cd := &recordingChildDispatcher{writeK3s: true}
	p := &Provider{}
	m := clusterManifest("dev")
	_, err := p.applyClusterViaPlan(context.Background(), m, cd)
	if err != nil {
		t.Fatalf("applyClusterViaPlan: %v", err)
	}
	bundleDir, err := clusterBundleDir("dev")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(bundleDir, tmp) {
		t.Fatalf("bundleDir not under temp HOME (%q not in %q)", bundleDir, tmp)
	}
	// Sanity: at least ca.pem should exist.
	if _, statErr := readFile(bundleDir + "/ca.pem"); statErr != nil {
		t.Errorf("expected ca.pem in bundle dir, got: %v", statErr)
	}
}

func TestApplyClusterViaPlan_SavesClusterStateForApplyExisting(t *testing.T) {
	// applyExisting reads p.loadState(name) — that has to succeed
	// after applyClusterViaPlan so re-apply doesn't re-create.
	t.Setenv("HOME", t.TempDir())

	cd := &recordingChildDispatcher{writeK3s: true}
	p := &Provider{}
	m := clusterManifest("dev")
	_, err := p.applyClusterViaPlan(context.Background(), m, cd)
	if err != nil {
		t.Fatalf("applyClusterViaPlan: %v", err)
	}
	saved, err := p.loadState("dev")
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if saved == nil {
		t.Fatal("loadState returned nil — cluster state not persisted")
	}
	if saved.Kind != kindCluster {
		t.Errorf("saved.Kind = %q, want Cluster", saved.Kind)
	}
	children, err := readChildren("dev")
	if err != nil {
		t.Fatalf("readChildren: %v", err)
	}
	if len(children) != 1 {
		t.Errorf("expected 1 child VM in saved state, got %d", len(children))
	}
}

// readFile is a tiny wrapper so the test doesn't import os in
// otherwise-unused ways.
func readFile(path string) ([]byte, error) {
	return osReadFile(path)
}

// failingChildDispatcher succeeds on VirtualMachine children and
// fails on the first non-VM child. Simulates a partial-success case
// like the homelab validation: VMs came up but k3s install hung.
type failingChildDispatcher struct {
	calls []*protocol.Resource
}

func (r *failingChildDispatcher) ApplyChild(_ context.Context, m *protocol.Resource) (*protocol.Resource, error) {
	r.calls = append(r.calls, m)
	if m.Kind != "VirtualMachine" {
		return nil, fmt.Errorf("simulated K3sNode failure on %s", m.Metadata.Name)
	}
	return m, nil
}

// TestApplyClusterViaPlan_InterimStubEnablesDeleteAfterPhase2Failure:
// after phase 1 succeeds but phase 2 fails, Cluster.Delete has to be
// able to find the VM children. Without the interim stub, the state
// file is missing and Delete no-ops — leaving live VMs on the
// hypervisor. This test drives that scenario end-to-end: partial
// apply → state file present with the VM child → Delete reads it.
func TestApplyClusterViaPlan_InterimStubEnablesDeleteAfterPhase2Failure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cd := &failingChildDispatcher{}
	p := &Provider{}
	m := clusterManifest("dev")
	_, err := p.applyClusterViaPlan(context.Background(), m, cd)
	if err == nil {
		t.Fatal("expected error from failing K3sNode dispatch, got nil")
	}
	// Cluster state must exist even though the apply failed —
	// this is the whole point of the interim stub.
	saved, loadErr := p.loadState("dev")
	if loadErr != nil {
		t.Fatalf("loadState after partial apply: %v", loadErr)
	}
	if saved == nil {
		t.Fatal("no cluster state written; Delete would be a no-op and leak VMs")
	}
	children, err := readChildren("dev")
	if err != nil {
		t.Fatalf("readChildren: %v", err)
	}
	if len(children) != 1 || children[0].Kind != "VirtualMachine" || children[0].Name != "dev-cp-0" {
		t.Errorf("expected 1 VirtualMachine child (dev-cp-0), got %+v", children)
	}
	// Status phase should reflect partial state.
	phase, _ := saved.Status["phase"].(string)
	if phase != "Provisioning" {
		t.Errorf("status.phase = %q, want Provisioning", phase)
	}
}

// TestClusterDelete_CleansPerNodeState: verify Cluster.Delete's new
// per-node K3sNode + AgentInstall state cleanup. Populate state
// files as if a successful apply had run, then Delete and confirm
// they're gone. Uses a stub VMApplier since we don't need to talk to
// Proxmox for this test.
func TestClusterDelete_CleansPerNodeState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Populate cluster state with one VM child.
	m := clusterManifest("dev")
	if err := (&Provider{}).saveClusterStateStub("dev", m,
		[]*protocol.Resource{{Metadata: protocol.ResourceMetadata{Name: "dev-cp-0"}}},
		"Ready", "test",
	); err != nil {
		t.Fatalf("save state stub: %v", err)
	}
	// Populate the per-node K3sNode + AgentInstall state files.
	if err := saveNodeState(&nodeState{Name: "dev-cp-0", VMName: "dev-cp-0", VMIP: "1.2.3.4", Role: "server", Installed: true}); err != nil {
		t.Fatalf("save K3sNode state: %v", err)
	}
	if err := saveAgentInstallState(&agentInstallState{Name: "dev-cp-0-agent", VMName: "dev-cp-0", VMIP: "1.2.3.4", ClusterName: "dev", Installed: true}); err != nil {
		t.Fatalf("save AgentInstall state: %v", err)
	}

	p := &Provider{vms: nopVMApplier{}}
	if err := p.Delete(context.Background(), kindCluster, "dev"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// K3sNode state should be gone.
	if s, _ := loadNodeState("dev-cp-0"); s != nil {
		t.Errorf("K3sNode state not cleaned: %+v", s)
	}
	// AgentInstall state should be gone.
	if s, _ := loadAgentInstallState("dev-cp-0-agent"); s != nil {
		t.Errorf("AgentInstall state not cleaned: %+v", s)
	}
}

// nopVMApplier is a stand-in for the proxmox VM provider in Delete
// tests — we only care about state-file cleanup, not the VM API.
type nopVMApplier struct{}

func (nopVMApplier) Apply(context.Context, *protocol.Resource) (*protocol.Resource, error) {
	return nil, nil
}
func (nopVMApplier) Get(context.Context, string, string) (*protocol.Resource, error) {
	return nil, nil
}
func (nopVMApplier) Delete(context.Context, string, string) error { return nil }
