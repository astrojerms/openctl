package k3s

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/internal/controller/providers"
	k3sresources "github.com/openctl/openctl/pkg/k3s/resources"
	"github.com/openctl/openctl/pkg/protocol"
)

// recordedCall tracks one Begin/End pair from a fake child recorder. Used by
// the apply-existing tests to assert that the k3s provider routes per-VM
// work through the child-ops surface.
type recordedCall struct {
	Op      operations.Operation
	Ok      bool
	Err     string
	ChildID string
}

type fakeRecorder struct {
	calls []recordedCall
	seq   int
}

func (r *fakeRecorder) Begin(_ context.Context, op *operations.Operation) (string, error) {
	r.seq++
	cid := fmt.Sprintf("child-%d", r.seq)
	r.calls = append(r.calls, recordedCall{Op: *op, ChildID: cid})
	return cid, nil
}

func (r *fakeRecorder) End(_ context.Context, childID string, ok bool, errMsg, _ string) error {
	for i := range r.calls {
		if r.calls[i].ChildID == childID {
			r.calls[i].Ok = ok
			r.calls[i].Err = errMsg
			return nil
		}
	}
	return nil
}

// fakeVMs satisfies VMApplier for tests where we don't need real Proxmox.
type fakeVMs struct {
	deleted [][]string
}

func (f *fakeVMs) Apply(_ context.Context, _ *protocol.Resource) (*protocol.Resource, error) {
	return nil, nil
}
func (f *fakeVMs) Get(_ context.Context, _, _ string) (*protocol.Resource, error) {
	return nil, nil
}
func (f *fakeVMs) Delete(_ context.Context, kind, name string) error {
	f.deleted = append(f.deleted, []string{kind, name})
	return nil
}

func writeClusterState(t *testing.T, home, name, body string) {
	t.Helper()
	dir := filepath.Join(home, ".openctl", "state", "k3s")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRemoveNodes_DispatchesFullChildSet: with a ChildDispatcher present,
// scale-down tears down each removed node's full plan-native child set
// (AgentInstall + K3sNode + VM), workers before CPs, so the per-node state
// files are cleaned up rather than orphaned.
func TestRemoveNodes_DispatchesFullChildSet(t *testing.T) {
	// Default (unset) now enables the plan path.
	t.Setenv("OPENCTL_CONVERGE_VIA_PLAN", "")
	cd := &recordingChildDispatcher{}
	ctx := operations.WithChildDispatcher(context.Background(), cd)
	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	spec := &k3sresources.ClusterSpec{}
	spec.Compute.Provider = "proxmox"

	if err := p.removeNodes(ctx, spec, []string{"c-w-0"}, []string{"c-cp-2"}); err != nil {
		t.Fatalf("removeNodes: %v", err)
	}

	want := []string{
		"k3s.openctl.io/v1|AgentInstall|c-w-0-agent",
		"k3s.openctl.io/v1|K3sNode|c-w-0",
		"proxmox.openctl.io/v1|VirtualMachine|c-w-0",
		"k3s.openctl.io/v1|AgentInstall|c-cp-2-agent",
		"k3s.openctl.io/v1|K3sNode|c-cp-2",
		"proxmox.openctl.io/v1|VirtualMachine|c-cp-2",
	}
	if got := cd.deleteKeys(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("DeleteChild calls mismatch:\n got=%v\nwant=%v", got, want)
	}
	// The VM-only fallback must not fire when a dispatcher is present.
	if vms := p.vms.(*fakeVMs); len(vms.deleted) != 0 {
		t.Errorf("VM-only fallback used despite ChildDispatcher present: %v", vms.deleted)
	}
}

// TestRemoveNodes_FallsBackToVMOnlyWithoutDispatcher: with no ChildDispatcher
// (CLI direct-apply, which never wrote per-node state), scale-down keeps the
// legacy VM-only delete.
func TestRemoveNodes_FallsBackToVMOnlyWithoutDispatcher(t *testing.T) {
	vms := &fakeVMs{}
	p := New(&protocol.ProviderConfig{}, vms)
	spec := &k3sresources.ClusterSpec{}
	spec.Compute.Provider = "proxmox"

	if err := p.removeNodes(context.Background(), spec, []string{"c-w-0"}, []string{"c-cp-0"}); err != nil {
		t.Fatalf("removeNodes: %v", err)
	}
	want := [][]string{{"VirtualMachine", "c-w-0"}, {"VirtualMachine", "c-cp-0"}}
	if fmt.Sprintf("%v", vms.deleted) != fmt.Sprintf("%v", want) {
		t.Errorf("VM deletes = %v, want %v", vms.deleted, want)
	}
}

// TestRemoveNodes_GateOffUsesVMOnlyDespiteDispatcher: with the plan-based
// converge explicitly opted out (OPENCTL_CONVERGE_VIA_PLAN=0), scale-down
// keeps the legacy VM-only delete even when a ChildDispatcher is present —
// the escape hatch back to the imperative path.
func TestRemoveNodes_GateOffUsesVMOnlyDespiteDispatcher(t *testing.T) {
	t.Setenv("OPENCTL_CONVERGE_VIA_PLAN", "0")
	cd := &recordingChildDispatcher{}
	ctx := operations.WithChildDispatcher(context.Background(), cd)
	vms := &fakeVMs{}
	p := New(&protocol.ProviderConfig{}, vms)
	spec := &k3sresources.ClusterSpec{}
	spec.Compute.Provider = "proxmox"

	if err := p.removeNodes(ctx, spec, []string{"c-w-0"}, nil); err != nil {
		t.Fatalf("removeNodes: %v", err)
	}
	if len(cd.deleteKeys()) != 0 {
		t.Errorf("gate off: expected no DeleteChild calls, got %v", cd.deleteKeys())
	}
	want := [][]string{{"VirtualMachine", "c-w-0"}}
	if fmt.Sprintf("%v", vms.deleted) != fmt.Sprintf("%v", want) {
		t.Errorf("gate off: VM deletes = %v, want %v", vms.deleted, want)
	}
}

func TestProviderName(t *testing.T) {
	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	if p.Name() != "k3s" {
		t.Errorf("Name = %q, want k3s", p.Name())
	}
	kinds := p.Kinds()
	if len(kinds) != 3 || kinds[0] != "Cluster" || kinds[1] != "K3sNode" || kinds[2] != "AgentInstall" {
		t.Errorf("Kinds = %v, want [Cluster K3sNode AgentInstall]", kinds)
	}
}

func TestProviderRejectsWrongKind(t *testing.T) {
	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	if _, err := p.Apply(context.Background(), &protocol.Resource{Kind: "Other"}); err == nil {
		t.Error("Apply on wrong kind should error")
	}
	if _, err := p.Get(context.Background(), "Other", "x"); err == nil {
		t.Error("Get on wrong kind should error")
	}
}

func TestOwnerOfFindsClusterChild(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeClusterState(t, home, "dev", `apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: dev
children:
  - provider: proxmox
    kind: VirtualMachine
    name: dev-cp-0
  - provider: proxmox
    kind: VirtualMachine
    name: dev-worker-0
`)

	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	ownerKind, ownerName, owned := p.OwnerOf("VirtualMachine", "dev-cp-0")
	if !owned {
		t.Fatal("dev-cp-0 should be reported as owned")
	}
	if ownerKind != "Cluster" || ownerName != "dev" {
		t.Errorf("owner = %s/%s, want Cluster/dev", ownerKind, ownerName)
	}
}

func TestChildrenOfReturnsVMRefs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeClusterState(t, home, "dev", `apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: dev
children:
  - provider: proxmox
    kind: VirtualMachine
    name: dev-cp-0
  - provider: proxmox
    kind: VirtualMachine
    name: dev-worker-0
`)

	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	got := p.ChildrenOf(kindCluster, "dev")
	if len(got) != 2 {
		t.Fatalf("ChildrenOf len = %d, want 2: %+v", len(got), got)
	}
	want := []providers.ResourceRef{
		{APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine", Name: "dev-cp-0"},
		{APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine", Name: "dev-worker-0"},
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("ChildrenOf[%d] = %+v, want %+v", i, got[i], w)
		}
	}

	// Wrong kind: don't find anything.
	if got := p.ChildrenOf("VirtualMachine", "dev"); got != nil {
		t.Errorf("ChildrenOf on VirtualMachine should be nil, got %+v", got)
	}
	// Unknown name: nothing.
	if got := p.ChildrenOf(kindCluster, "missing"); got != nil {
		t.Errorf("ChildrenOf on unknown cluster should be nil, got %+v", got)
	}
}

func TestOwnerOfReturnsFalseForUnowned(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// No state files at all.
	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	if _, _, owned := p.OwnerOf("VirtualMachine", "freebird"); owned {
		t.Error("freebird VM should not be reported as owned (no state files)")
	}

	// A state file exists, but doesn't list this VM.
	writeClusterState(t, home, "other", `apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: other
children:
  - provider: proxmox
    kind: VirtualMachine
    name: other-cp-0
`)
	if _, _, owned := p.OwnerOf("VirtualMachine", "freebird"); owned {
		t.Error("freebird should not be owned by 'other' cluster")
	}
}

func TestGetReturnsNotFoundWhenStateMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	_, err := p.Get(context.Background(), "Cluster", "missing")
	if err == nil {
		t.Fatal("want providers.NotFoundError, got nil")
	}
	var nf *providers.NotFoundError
	if !errors.As(err, &nf) {
		t.Errorf("want NotFoundError, got %T: %v", err, err)
	}
}

func TestGetReturnsExistingClusterFromState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeClusterState(t, home, "dev", `apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: dev
spec:
  network:
    bridge: vmbr0
status:
  phase: Ready
`)

	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	r, err := p.Get(context.Background(), "Cluster", "dev")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if r.Status["phase"] != "Ready" {
		t.Errorf("phase = %v, want Ready", r.Status["phase"])
	}
}

func TestGetSynthesizesObservedCountsFromChildren(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Manifest claims 2 CPs and 3 workers, but the children list has only
	// 1 CP and 1 worker — simulating an out-of-band deletion. Get should
	// report the *actual* counts so the resource handler can surface drift.
	writeClusterState(t, home, "dev", `apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: dev
spec:
  nodes:
    controlPlane:
      count: 2
    workers:
      - name: worker
        count: 3
children:
  - provider: proxmox
    kind: VirtualMachine
    name: dev-cp-0
  - provider: proxmox
    kind: VirtualMachine
    name: dev-worker-0
`)

	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	r, err := p.Get(context.Background(), "Cluster", "dev")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	nodes, ok := r.Spec["nodes"].(map[string]any)
	if !ok {
		t.Fatalf("spec.nodes missing or wrong type: %T", r.Spec["nodes"])
	}
	cp, _ := nodes["controlPlane"].(map[string]any)
	if cp == nil || cp["count"] != 1 {
		t.Errorf("controlPlane.count = %v, want 1", cp["count"])
	}
	workers, _ := nodes["workers"].([]any)
	if len(workers) != 1 {
		t.Fatalf("workers = %v, want one pool", workers)
	}
	pool, _ := workers[0].(map[string]any)
	if pool["count"] != 1 {
		t.Errorf("workers[0].count = %v, want 1 (one VM, not the manifest's 3)", pool["count"])
	}
}

func TestDeleteCascadesToChildVMs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeClusterState(t, home, "dev", `apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: dev
children:
  - provider: proxmox
    kind: VirtualMachine
    name: dev-cp-0
  - provider: proxmox
    kind: VirtualMachine
    name: dev-worker-0
`)

	vms := &fakeVMs{}
	p := New(&protocol.ProviderConfig{}, vms)
	if err := p.Delete(context.Background(), "Cluster", "dev"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(vms.deleted) != 2 {
		t.Errorf("expected 2 child VM deletes, got %d: %v", len(vms.deleted), vms.deleted)
	}
	// State file should be gone.
	if _, err := os.Stat(filepath.Join(home, ".openctl", "state", "k3s", "dev.yaml")); !os.IsNotExist(err) {
		t.Error("state file should be removed after delete")
	}
}

func TestDeleteOnMissingClusterIsIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	if err := p.Delete(context.Background(), "Cluster", "missing"); err != nil {
		t.Errorf("delete on missing should be idempotent, got %v", err)
	}
}

// scaleDownManifest is a Cluster manifest with the given worker count, used
// across the apply-existing tests below. Numeric values are float64 to
// match how the gRPC path delivers them (structpb encodes all JSON numbers
// as float64).
func scaleDownManifest(workerCount int) *protocol.Resource {
	return &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       "Cluster",
		Metadata:   protocol.ResourceMetadata{Name: "dev"},
		Spec: map[string]any{
			"compute": map[string]any{
				"provider": "proxmox",
				"image":    map[string]any{"template": "tpl", "storage": "s"},
				"default":  map[string]any{"cpus": float64(2), "memoryMB": float64(4096), "diskGB": float64(30)},
			},
			"nodes": map[string]any{
				"controlPlane": map[string]any{"count": float64(1)},
				"workers":      []any{map[string]any{"name": "worker", "count": float64(workerCount)}},
			},
			"network": map[string]any{
				"bridge": "vmbr0",
				"staticIPs": map[string]any{
					"startIP": "192.168.1.100",
					"gateway": "192.168.1.1",
					"netmask": "24",
				},
			},
			"ssh": map[string]any{"user": "ubuntu", "privateKeyPath": "~/.ssh/id_ed25519"},
		},
	}
}

func TestApplyExistingNoChangesIsNoOp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeClusterState(t, home, "dev", `apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: dev
spec:
  nodes:
    controlPlane:
      count: 1
    workers:
      - name: worker
        count: 1
children:
  - provider: proxmox
    kind: VirtualMachine
    name: dev-cp-0
  - provider: proxmox
    kind: VirtualMachine
    name: dev-worker-0
`)
	vms := &fakeVMs{}
	p := New(&protocol.ProviderConfig{}, vms)
	if _, err := p.Apply(context.Background(), scaleDownManifest(1)); err != nil {
		t.Fatalf("Apply same-shape: %v", err)
	}
	if len(vms.deleted) != 0 {
		t.Errorf("no-op apply should not delete any VMs, got %v", vms.deleted)
	}
}

func TestApplyExistingScaleDownRequiresFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeClusterState(t, home, "dev", `apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: dev
spec: {}
children:
  - provider: proxmox
    kind: VirtualMachine
    name: dev-cp-0
  - provider: proxmox
    kind: VirtualMachine
    name: dev-worker-0
  - provider: proxmox
    kind: VirtualMachine
    name: dev-worker-1
`)
	vms := &fakeVMs{}
	p := New(&protocol.ProviderConfig{}, vms)
	// Manifest asks for 1 worker; state has 2. Without --allow-destructive,
	// apply must refuse.
	_, err := p.Apply(context.Background(), scaleDownManifest(1))
	if err == nil {
		t.Fatal("scale-down without --allow-destructive should error")
	}
	if len(vms.deleted) != 0 {
		t.Errorf("refused apply must not delete any VMs, got %v", vms.deleted)
	}
}

func TestApplyExistingScaleDownWithFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeClusterState(t, home, "dev", `apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: dev
spec: {}
children:
  - provider: proxmox
    kind: VirtualMachine
    name: dev-cp-0
  - provider: proxmox
    kind: VirtualMachine
    name: dev-worker-0
  - provider: proxmox
    kind: VirtualMachine
    name: dev-worker-1
`)
	vms := &fakeVMs{}
	p := New(&protocol.ProviderConfig{}, vms)
	manifest := scaleDownManifest(1)
	manifest.Metadata.Annotations = map[string]string{
		"openctl.io/allow-destructive": "true",
	}
	if _, err := p.Apply(context.Background(), manifest); err != nil {
		t.Fatalf("scale-down with flag: %v", err)
	}
	if len(vms.deleted) != 1 || vms.deleted[0][1] != "dev-worker-1" {
		t.Errorf("expected delete of dev-worker-1, got %v", vms.deleted)
	}
}

func TestApplyExistingScaleDownRecordsChildOpsViaRecorder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeClusterState(t, home, "dev", `apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: dev
spec: {}
children:
  - provider: proxmox
    kind: VirtualMachine
    name: dev-cp-0
  - provider: proxmox
    kind: VirtualMachine
    name: dev-worker-0
  - provider: proxmox
    kind: VirtualMachine
    name: dev-worker-1
`)
	vms := &fakeVMs{}
	p := New(&protocol.ProviderConfig{}, vms)
	manifest := scaleDownManifest(1)
	manifest.Metadata.Annotations = map[string]string{
		"openctl.io/allow-destructive": "true",
	}
	rec := &fakeRecorder{}
	ctx := operations.WithRecorder(context.Background(), rec, "parent-op-id")
	if _, err := p.Apply(ctx, manifest); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// One delete child op for the removed worker, with the right shape.
	if len(rec.calls) != 1 {
		t.Fatalf("recorder calls = %d, want 1", len(rec.calls))
	}
	c := rec.calls[0]
	if c.Op.Type != operations.TypeDelete {
		t.Errorf("child type = %q, want delete", c.Op.Type)
	}
	if c.Op.Kind != "VirtualMachine" || c.Op.ResourceName != "dev-worker-1" {
		t.Errorf("child target = %s/%s, want VirtualMachine/dev-worker-1", c.Op.Kind, c.Op.ResourceName)
	}
	if !c.Ok {
		t.Errorf("child should have ended with ok=true, got err=%q", c.Err)
	}
}

func TestApplyExistingNoChangesEmitsNoChildOps(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeClusterState(t, home, "dev", `apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: dev
spec:
  nodes:
    controlPlane:
      count: 1
    workers:
      - name: worker
        count: 1
children:
  - provider: proxmox
    kind: VirtualMachine
    name: dev-cp-0
  - provider: proxmox
    kind: VirtualMachine
    name: dev-worker-0
`)
	rec := &fakeRecorder{}
	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	ctx := operations.WithRecorder(context.Background(), rec, "parent-op-id")
	if _, err := p.Apply(ctx, scaleDownManifest(1)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(rec.calls) != 0 {
		t.Errorf("no-op apply should emit no child ops, got %d", len(rec.calls))
	}
}

func TestApplyExistingCatastrophicRequiresIKnowFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeClusterState(t, home, "dev", `apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: dev
spec: {}
children:
  - provider: proxmox
    kind: VirtualMachine
    name: dev-cp-0
  - provider: proxmox
    kind: VirtualMachine
    name: dev-worker-0
`)
	vms := &fakeVMs{}
	p := New(&protocol.ProviderConfig{}, vms)
	// Manifest with 0 workers + 1 CP. Removing the only worker is
	// catastrophic; even --allow-destructive isn't enough.
	manifest := scaleDownManifest(0)
	manifest.Metadata.Annotations = map[string]string{
		"openctl.io/allow-destructive": "true",
	}
	_, err := p.Apply(context.Background(), manifest)
	if err == nil {
		t.Fatal("removing last worker should be blocked even with --allow-destructive")
	}
	if len(vms.deleted) != 0 {
		t.Errorf("blocked catastrophic op must not delete VMs, got %v", vms.deleted)
	}

	// Now with the catastrophic-override flag, it goes through.
	manifest.Metadata.Annotations["openctl.io/i-know-this-breaks-the-cluster"] = "true"
	if _, err := p.Apply(context.Background(), manifest); err != nil {
		t.Fatalf("catastrophic op with both flags: %v", err)
	}
	if len(vms.deleted) != 1 || vms.deleted[0][1] != "dev-worker-0" {
		t.Errorf("expected delete of dev-worker-0, got %v", vms.deleted)
	}
}

func TestApplyExistingScaleUpRequiresBundleDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// State carries agent endpoints (so the surviving-CP IP lookup
	// succeeds) but no bundle dir alongside it — simulates state from a
	// cluster created by something other than the controller.
	writeClusterState(t, home, "dev", `apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: dev
spec: {}
status:
  outputs:
    agent:
      endpoints:
        dev-cp-0: 192.168.1.100
children:
  - provider: proxmox
    kind: VirtualMachine
    name: dev-cp-0
  - provider: proxmox
    kind: VirtualMachine
    name: dev-worker-0
`)
	vms := &fakeVMs{}
	p := New(&protocol.ProviderConfig{}, vms)
	_, err := p.Apply(context.Background(), scaleDownManifest(2))
	if err == nil {
		t.Fatal("count-up without an existing bundle dir should error clearly")
	}
	if !strings.Contains(err.Error(), "bundle dir") {
		t.Errorf("expected bundle-dir error, got: %v", err)
	}
}

func TestApplyExistingScaleUpRequiresSurvivingCPEndpoint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// No agent endpoints in state — can't learn the surviving CP's IP.
	writeClusterState(t, home, "dev", `apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: dev
spec: {}
children:
  - provider: proxmox
    kind: VirtualMachine
    name: dev-cp-0
  - provider: proxmox
    kind: VirtualMachine
    name: dev-worker-0
`)
	vms := &fakeVMs{}
	p := New(&protocol.ProviderConfig{}, vms)
	_, err := p.Apply(context.Background(), scaleDownManifest(2))
	if err == nil {
		t.Fatal("count-up without surviving-CP endpoint should error clearly")
	}
	if !strings.Contains(err.Error(), "no IP for surviving CP") {
		t.Errorf("expected surviving-CP error, got: %v", err)
	}
}

func TestComputeChangePlanDetectsAdds(t *testing.T) {
	// Sanity that the count-up branch fires for "1 worker → 2 workers".
	spec, err := k3sresources.ParseClusterSpec(scaleDownManifest(2))
	if err != nil {
		t.Fatal(err)
	}
	plan := computeChangePlan("dev", spec, []childRef{
		{Provider: "proxmox", Kind: "VirtualMachine", Name: "dev-cp-0"},
		{Provider: "proxmox", Kind: "VirtualMachine", Name: "dev-worker-0"},
	})
	if !plan.hasChanges() {
		t.Fatal("expected changes")
	}
	if len(plan.addWorkers) != 1 || plan.addWorkers[0] != "dev-worker-1" {
		t.Errorf("addWorkers = %v, want [dev-worker-1]", plan.addWorkers)
	}
	if len(plan.removeWorkers) != 0 || len(plan.removeCPs) != 0 {
		t.Errorf("plan unexpectedly has removes: %+v", plan)
	}
}
