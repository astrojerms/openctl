package k3s

import (
	"context"
	"errors"
	"testing"
)

// fakeUpgrader records upgrade calls and returns scripted health outcomes.
// postVersion[name] is the version Health reports after a node is upgraded
// (defaults to the target); healthErr[name] forces a health failure.
type fakeUpgrader struct {
	upgraded    []string
	postVersion map[string]string
	healthErr   map[string]error
	upgradeErr  map[string]error
	target      string
}

func (f *fakeUpgrader) Upgrade(_ context.Context, node upgradeNode, version string) error {
	if err := f.upgradeErr[node.Name]; err != nil {
		return err
	}
	f.upgraded = append(f.upgraded, node.Name)
	f.target = version
	return nil
}

func (f *fakeUpgrader) Health(_ context.Context, node upgradeNode) (string, error) {
	if err := f.healthErr[node.Name]; err != nil {
		return "", err
	}
	if v, ok := f.postVersion[node.Name]; ok {
		return v, nil
	}
	return f.target, nil // healthy at the version we upgraded to
}

func node(name, role, version string) upgradeNode {
	return upgradeNode{Name: name, Role: role, Version: version}
}

func TestUpgradeOrder_ControlPlanesFirst(t *testing.T) {
	nodes := []upgradeNode{
		node("w0", roleAgent, "v1"),
		node("cp0", roleServer, "v1"),
		node("w1", roleAgent, "v1"),
		node("cp1", roleServer, "v1"),
	}
	got := upgradeOrder(nodes)
	want := []string{"cp0", "cp1", "w0", "w1"}
	if len(got) != len(want) {
		t.Fatalf("order length = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("order[%d] = %q, want %q (CPs before workers, input order within group)", i, got[i].Name, w)
		}
	}
}

func TestRollingUpgrade_UpgradesInOrder(t *testing.T) {
	nodes := []upgradeNode{
		node("w0", roleAgent, "v1"),
		node("cp0", roleServer, "v1"),
		node("cp1", roleServer, "v1"),
	}
	f := &fakeUpgrader{}
	res, err := rollingUpgrade(context.Background(), nodes, "v2", f)
	if err != nil {
		t.Fatalf("rollingUpgrade: %v", err)
	}
	wantOrder := []string{"cp0", "cp1", "w0"}
	if len(f.upgraded) != 3 || f.upgraded[0] != "cp0" || f.upgraded[1] != "cp1" || f.upgraded[2] != "w0" {
		t.Errorf("upgrade order = %v, want %v", f.upgraded, wantOrder)
	}
	if len(res.Upgraded) != 3 {
		t.Errorf("Result.Upgraded = %v, want 3 nodes", res.Upgraded)
	}
}

func TestRollingUpgrade_SkipsNodesAtTarget(t *testing.T) {
	// cp0 already at v2 (e.g. a prior halted run got this far); it must be
	// skipped, not re-upgraded.
	nodes := []upgradeNode{
		node("cp0", roleServer, "v2"),
		node("cp1", roleServer, "v1"),
		node("w0", roleAgent, "v2"),
	}
	f := &fakeUpgrader{}
	res, err := rollingUpgrade(context.Background(), nodes, "v2", f)
	if err != nil {
		t.Fatalf("rollingUpgrade: %v", err)
	}
	if len(f.upgraded) != 1 || f.upgraded[0] != "cp1" {
		t.Errorf("upgraded = %v, want only [cp1] (others already at target)", f.upgraded)
	}
	if len(res.Skipped) != 2 {
		t.Errorf("Result.Skipped = %v, want 2 (cp0, w0)", res.Skipped)
	}
}

func TestRollingUpgrade_HaltsOnUnhealthyNode(t *testing.T) {
	nodes := []upgradeNode{
		node("cp0", roleServer, "v1"),
		node("cp1", roleServer, "v1"),
		node("w0", roleAgent, "v1"),
	}
	f := &fakeUpgrader{healthErr: map[string]error{"cp0": errors.New("apiserver down")}}
	res, err := rollingUpgrade(context.Background(), nodes, "v2", f)
	if err == nil {
		t.Fatal("expected a halt error when cp0 fails health")
	}
	// cp0 was upgraded (the swap happened) but the loop halted there — cp1 and
	// w0 must NOT have been touched (don't march past a bad control plane).
	if len(f.upgraded) != 1 || f.upgraded[0] != "cp0" {
		t.Errorf("upgraded = %v, want [cp0] then halt", f.upgraded)
	}
	if len(res.Upgraded) != 0 {
		t.Errorf("Result.Upgraded = %v, want 0 (cp0 never confirmed healthy)", res.Upgraded)
	}
}

func TestRollingUpgrade_HaltsOnWrongVersionAfterUpgrade(t *testing.T) {
	nodes := []upgradeNode{node("cp0", roleServer, "v1"), node("w0", roleAgent, "v1")}
	// cp0 comes back "healthy" but at the wrong version — treat as a halt.
	f := &fakeUpgrader{postVersion: map[string]string{"cp0": "v1"}}
	_, err := rollingUpgrade(context.Background(), nodes, "v2", f)
	if err == nil {
		t.Fatal("expected a halt when a node reports the wrong version post-upgrade")
	}
	if len(f.upgraded) != 1 {
		t.Errorf("upgraded = %v, want only cp0 before halt", f.upgraded)
	}
}

func TestRollingUpgrade_PropagatesUpgradeError(t *testing.T) {
	nodes := []upgradeNode{node("cp0", roleServer, "v1")}
	f := &fakeUpgrader{upgradeErr: map[string]error{"cp0": errors.New("download failed")}}
	_, err := rollingUpgrade(context.Background(), nodes, "v2", f)
	if err == nil || len(f.upgraded) != 0 {
		t.Fatalf("expected upgrade error to halt with nothing upgraded, err=%v upgraded=%v", err, f.upgraded)
	}
}

func TestRollingUpgrade_RejectsEmptyVersion(t *testing.T) {
	if _, err := rollingUpgrade(context.Background(), nil, "", &fakeUpgrader{}); err == nil {
		t.Fatal("expected an error for an empty target version")
	}
}

func TestRollingUpgrade_HonorsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	nodes := []upgradeNode{node("cp0", roleServer, "v1")}
	f := &fakeUpgrader{}
	if _, err := rollingUpgrade(ctx, nodes, "v2", f); err == nil {
		t.Fatal("expected a context-canceled error")
	}
	if len(f.upgraded) != 0 {
		t.Errorf("upgraded %v on a canceled context, want none", f.upgraded)
	}
}
