package k3s

import (
	"context"
	"strings"
	"testing"

	"github.com/openctl/openctl/internal/controller/operations"
	k3sresources "github.com/openctl/openctl/pkg/k3s/resources"
	"github.com/openctl/openctl/pkg/protocol"
)

// TestRespecNodesViaPlan_DestroyRecreateRejoinSurvivingCP: respec'ing cp-0 in
// a 2-CP cluster tears down its full child set, then recreates it and rejoins
// the *other* CP (cp-1) — never itself, which is down during its own respec.
func TestRespecNodesViaPlan_DestroyRecreateRejoinSurvivingCP(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedServerNodes(t, "dev-cp-0", "dev-cp-1")

	m := clusterManifest("dev", func(r *protocol.Resource) {
		r.Spec["nodes"].(map[string]any)["controlPlane"] = map[string]any{"count": float64(2)}
	})
	cd := &recordingChildDispatcher{writeK3s: true}
	ctx := operations.WithChildDispatcher(context.Background(), cd)
	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	spec := &k3sresources.ClusterSpec{}
	spec.Compute.Provider = "proxmox"

	current := []childRef{
		{Provider: "proxmox", Kind: "VirtualMachine", Name: "dev-cp-0"},
		{Provider: "proxmox", Kind: "VirtualMachine", Name: "dev-cp-1"},
	}
	respecs := []respecNode{{Name: "dev-cp-0", IsCP: true, DesiredCPUs: 4, ObservedCPUs: 2}}

	eps, err := p.respecNodesViaPlan(ctx, cd, m, "dev", spec, respecs, current, map[string]bool{})
	if err != nil {
		t.Fatalf("respecNodesViaPlan: %v", err)
	}

	// Destroyed the full child set for cp-0 (agent, k3snode, vm)...
	wantDel := []string{
		"k3s.openctl.io/v1|AgentInstall|dev-cp-0-agent",
		"k3s.openctl.io/v1|K3sNode|dev-cp-0",
		"proxmox.openctl.io/v1|VirtualMachine|dev-cp-0",
	}
	if got := cd.deleteKeys(); strings.Join(got, "\n") != strings.Join(wantDel, "\n") {
		t.Errorf("deletes:\n got=%v\nwant=%v", got, wantDel)
	}

	// ...then recreated cp-0: VM, K3sNode, AgentInstall — all for cp-0.
	wantApply := strings.Join([]string{"VirtualMachine", kindK3sNode, kindAgentInstall}, ",")
	if got := strings.Join(cd.kindsInOrder(), ","); got != wantApply {
		t.Errorf("applies = %s, want %s", got, wantApply)
	}
	for _, c := range cd.calls {
		if !strings.HasPrefix(c.Metadata.Name, "dev-cp-0") {
			t.Errorf("recreated a non-cp-0 child: %s", c.Metadata.Name)
		}
	}

	// The recreated cp-0 rejoins cp-1 — not itself.
	if got := joinRefName(t, cd.calls[1], "joinFrom"); got != "dev-cp-1" {
		t.Errorf("respec'd cp-0 joinFrom = %q, want dev-cp-1", got)
	}
	if eps["dev-cp-0"] == "" {
		t.Errorf("expected endpoint IP for recreated dev-cp-0, got %v", eps)
	}
}

// TestRespecNodesViaPlan_WorkerRejoinsCP: respec'ing a worker rejoins the
// control plane (cp-0), and the worker's own respec doesn't exclude any CP.
func TestRespecNodesViaPlan_WorkerRejoinsCP(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	seedServerNodes(t, "dev-cp-0")

	m := clusterManifest("dev", func(r *protocol.Resource) {
		r.Spec["nodes"].(map[string]any)["workers"] = []any{
			map[string]any{"name": "w", "count": float64(1)},
		}
	})
	cd := &recordingChildDispatcher{writeK3s: true}
	ctx := operations.WithChildDispatcher(context.Background(), cd)
	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	spec := &k3sresources.ClusterSpec{}
	spec.Compute.Provider = "proxmox"

	current := []childRef{
		{Provider: "proxmox", Kind: "VirtualMachine", Name: "dev-cp-0"},
		{Provider: "proxmox", Kind: "VirtualMachine", Name: "dev-w-0"},
	}
	respecs := []respecNode{{Name: "dev-w-0", DesiredMemMB: 8192, ObservedMemMB: 4096}}

	if _, err := p.respecNodesViaPlan(ctx, cd, m, "dev", spec, respecs, current, map[string]bool{}); err != nil {
		t.Fatalf("respecNodesViaPlan: %v", err)
	}
	if got := joinRefName(t, cd.calls[1], "joinFrom"); got != "dev-cp-0" {
		t.Errorf("respec'd worker joinFrom = %q, want dev-cp-0", got)
	}
}

// TestRespecNodesViaPlan_SoleCPRefused: respec'ing the only control plane is
// refused — there's no peer for the recreated node to rejoin, and letting it
// re-initialize would orphan every other node. (This is a catastrophic op
// gated upstream anyway; the plan path fails closed rather than break the
// cluster.)
func TestRespecNodesViaPlan_SoleCPRefused(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	m := clusterManifest("dev") // single CP
	cd := &recordingChildDispatcher{writeK3s: true}
	ctx := operations.WithChildDispatcher(context.Background(), cd)
	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	spec := &k3sresources.ClusterSpec{}
	spec.Compute.Provider = "proxmox"

	current := []childRef{{Provider: "proxmox", Kind: "VirtualMachine", Name: "dev-cp-0"}}
	respecs := []respecNode{{Name: "dev-cp-0", IsCP: true}}

	if _, err := p.respecNodesViaPlan(ctx, cd, m, "dev", spec, respecs, current, map[string]bool{}); err == nil {
		t.Fatal("expected an error respec'ing the sole control plane")
	}
	// Nothing should have been destroyed — it fails before the teardown.
	if len(cd.deleteKeys()) != 0 {
		t.Errorf("sole-CP respec must not destroy anything, got deletes %v", cd.deleteKeys())
	}
}
