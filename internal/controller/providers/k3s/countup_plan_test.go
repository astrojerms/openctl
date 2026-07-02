package k3s

import (
	"context"
	"strings"
	"testing"

	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/pkg/protocol"
)

// joinRefName digs the $ref target name out of a K3sNode join field.
func joinRefName(t *testing.T, k3sNode *protocol.Resource, field string) string {
	t.Helper()
	f, ok := k3sNode.Spec[field].(map[string]any)
	if !ok {
		t.Fatalf("%s missing on %s", field, k3sNode.Metadata.Name)
	}
	ref, ok := f["$ref"].(map[string]any)
	if !ok {
		t.Fatalf("%s.$ref missing on %s", field, k3sNode.Metadata.Name)
	}
	name, _ := ref["name"].(string)
	return name
}

// TestAddNodesViaPlan_AppliesAddedNodeChildrenWithSurvivingCPJoin: adding a
// worker to a live cluster applies only that node's VM → K3sNode →
// AgentInstall via the dispatcher, with the K3sNode's join refs pointed at
// the surviving control plane, and returns its resolved IP.
func TestAddNodesViaPlan_AppliesAddedNodeChildrenWithSurvivingCPJoin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Desired: 1 CP (dev-cp-0, already exists) + 1 worker (dev-w-0, to add).
	m := clusterManifest("dev", func(r *protocol.Resource) {
		r.Spec["nodes"].(map[string]any)["workers"] = []any{
			map[string]any{"name": "w", "count": float64(1)},
		}
	})

	cd := &recordingChildDispatcher{writeK3s: true}
	ctx := operations.WithChildDispatcher(context.Background(), cd)
	p := New(&protocol.ProviderConfig{}, &fakeVMs{})

	current := []childRef{{Provider: "proxmox", Kind: "VirtualMachine", Name: "dev-cp-0"}}
	plan := &changePlan{addWorkers: []string{"dev-w-0"}}

	eps, err := p.addNodesViaPlan(ctx, cd, m, "dev", plan, current, map[string]bool{})
	if err != nil {
		t.Fatalf("addNodesViaPlan: %v", err)
	}

	// Only the added node's children are applied — the existing cp-0 is
	// untouched — in VM → K3sNode → AgentInstall order.
	gotKinds := strings.Join(cd.kindsInOrder(), ",")
	wantKinds := strings.Join([]string{"VirtualMachine", kindK3sNode, kindAgentInstall}, ",")
	if gotKinds != wantKinds {
		t.Fatalf("ApplyChild kinds = %s, want %s", gotKinds, wantKinds)
	}
	wantNames := []string{"dev-w-0", "dev-w-0", "dev-w-0-agent"}
	for i, c := range cd.calls {
		if c.Metadata.Name != wantNames[i] {
			t.Errorf("child %d name = %s, want %s", i, c.Metadata.Name, wantNames[i])
		}
	}

	// The added worker's K3sNode joins the surviving CP, not Plan's default.
	k3sNode := cd.calls[1]
	if got := joinRefName(t, k3sNode, "joinFrom"); got != "dev-cp-0" {
		t.Errorf("joinFrom target = %q, want dev-cp-0", got)
	}
	if got := joinRefName(t, k3sNode, "joinURLFrom"); got != "dev-cp-0" {
		t.Errorf("joinURLFrom target = %q, want dev-cp-0", got)
	}

	// The endpoints map carries the new node's IP (writeK3s persisted one).
	if eps["dev-w-0"] == "" {
		t.Errorf("expected an endpoint IP for dev-w-0, got %v", eps)
	}
}

// TestAddNodesViaPlan_RepointsJoinWhenCP0Removed: if the first CP (cp-0) is
// being removed in the same converge, added nodes must join a *surviving*
// CP, not the default index-0 target.
func TestAddNodesViaPlan_RepointsJoinWhenCP0Removed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Desired: 2 CPs (cp-0, cp-1) + 1 worker to add. cp-1 already exists and
	// survives; cp-0 is being removed this converge.
	m := clusterManifest("dev", func(r *protocol.Resource) {
		r.Spec["nodes"].(map[string]any)["controlPlane"] = map[string]any{"count": float64(2)}
		r.Spec["nodes"].(map[string]any)["workers"] = []any{
			map[string]any{"name": "w", "count": float64(1)},
		}
	})

	cd := &recordingChildDispatcher{writeK3s: true}
	ctx := operations.WithChildDispatcher(context.Background(), cd)
	p := New(&protocol.ProviderConfig{}, &fakeVMs{})

	current := []childRef{
		{Provider: "proxmox", Kind: "VirtualMachine", Name: "dev-cp-0"},
		{Provider: "proxmox", Kind: "VirtualMachine", Name: "dev-cp-1"},
	}
	plan := &changePlan{addWorkers: []string{"dev-w-0"}}
	removed := map[string]bool{"dev-cp-0": true}

	if _, err := p.addNodesViaPlan(ctx, cd, m, "dev", plan, current, removed); err != nil {
		t.Fatalf("addNodesViaPlan: %v", err)
	}
	if got := joinRefName(t, cd.calls[1], "joinFrom"); got != "dev-cp-1" {
		t.Errorf("joinFrom target = %q, want dev-cp-1 (cp-0 is being removed)", got)
	}
}
