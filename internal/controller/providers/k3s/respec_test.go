package k3s

import (
	"context"
	"strings"
	"testing"

	k3sresources "github.com/openctl/openctl/pkg/k3s/resources"
	"github.com/openctl/openctl/pkg/protocol"
)

// vmsWithObserved is a fakeVMs that returns a canned observed spec for
// each VM name. Used to exercise the spec-diff path.
type vmsWithObserved struct {
	fakeVMs
	observed map[string]*protocol.Resource
}

func (v *vmsWithObserved) Get(_ context.Context, _, name string) (*protocol.Resource, error) {
	if v.observed == nil {
		return nil, nil
	}
	return v.observed[name], nil
}

func vmObserved(cpus, memMB int) *protocol.Resource {
	return &protocol.Resource{
		Spec: map[string]any{
			"cpu":    map[string]any{"cores": cpus},
			"memory": map[string]any{"size": memMB},
		},
	}
}

func TestComputeSpecRespecsDetectsCPUDrift(t *testing.T) {
	spec, _ := k3sresources.ParseClusterSpec(scaleDownManifest(1))
	// Manifest says 2 CPUs / 4096 MB. Observed VM has 1 CPU.
	vms := &vmsWithObserved{
		observed: map[string]*protocol.Resource{
			"dev-cp-0":     vmObserved(1, 4096),
			"dev-worker-0": vmObserved(2, 4096),
		},
	}
	p := New(&protocol.ProviderConfig{}, vms)
	respecs := p.computeSpecRespecs(context.Background(), "dev", spec,
		[]childRef{
			{Kind: "VirtualMachine", Name: "dev-cp-0"},
			{Kind: "VirtualMachine", Name: "dev-worker-0"},
		}, map[string]bool{})
	if len(respecs) != 1 {
		t.Fatalf("respecs = %d, want 1 (only dev-cp-0 drifts on CPU)", len(respecs))
	}
	r := respecs[0]
	if r.Name != "dev-cp-0" || !r.IsCP || r.ObservedCPUs != 1 || r.DesiredCPUs != 2 {
		t.Errorf("unexpected respec: %+v", r)
	}
}

func TestComputeSpecRespecsDetectsMemoryDrift(t *testing.T) {
	spec, _ := k3sresources.ParseClusterSpec(scaleDownManifest(1))
	vms := &vmsWithObserved{
		observed: map[string]*protocol.Resource{
			"dev-cp-0":     vmObserved(2, 4096),
			"dev-worker-0": vmObserved(2, 2048), // drift: half the desired
		},
	}
	p := New(&protocol.ProviderConfig{}, vms)
	respecs := p.computeSpecRespecs(context.Background(), "dev", spec,
		[]childRef{
			{Kind: "VirtualMachine", Name: "dev-cp-0"},
			{Kind: "VirtualMachine", Name: "dev-worker-0"},
		}, map[string]bool{})
	if len(respecs) != 1 {
		t.Fatalf("respecs = %d, want 1", len(respecs))
	}
	if respecs[0].Name != "dev-worker-0" || respecs[0].ObservedMemMB != 2048 || respecs[0].DesiredMemMB != 4096 {
		t.Errorf("unexpected respec: %+v", respecs[0])
	}
}

func TestComputeSpecRespecsSkipsRemoved(t *testing.T) {
	spec, _ := k3sresources.ParseClusterSpec(scaleDownManifest(1))
	vms := &vmsWithObserved{
		observed: map[string]*protocol.Resource{
			"dev-cp-0":     vmObserved(2, 4096),
			"dev-worker-0": vmObserved(1, 4096), // drifted, but being removed
		},
	}
	p := New(&protocol.ProviderConfig{}, vms)
	respecs := p.computeSpecRespecs(context.Background(), "dev", spec,
		[]childRef{
			{Kind: "VirtualMachine", Name: "dev-cp-0"},
			{Kind: "VirtualMachine", Name: "dev-worker-0"},
		}, map[string]bool{"dev-worker-0": true})
	if len(respecs) != 0 {
		t.Errorf("respecs = %d, want 0 (drifted node is being removed)", len(respecs))
	}
}

func TestComputeSpecRespecsSkipsUnobservable(t *testing.T) {
	// vms.Get returns nil for all names — should not panic, should report 0.
	spec, _ := k3sresources.ParseClusterSpec(scaleDownManifest(1))
	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	respecs := p.computeSpecRespecs(context.Background(), "dev", spec,
		[]childRef{
			{Kind: "VirtualMachine", Name: "dev-cp-0"},
			{Kind: "VirtualMachine", Name: "dev-worker-0"},
		}, map[string]bool{})
	if len(respecs) != 0 {
		t.Errorf("respecs = %d, want 0 (can't observe → skip)", len(respecs))
	}
}

func TestCatastrophicRespecReasonFlags1CPCase(t *testing.T) {
	r := catastrophicRespecReason([]respecNode{{Name: "dev-cp-0", IsCP: true}}, 1, 1)
	if !strings.Contains(r, "only control-plane") {
		t.Errorf("expected 1-CP catastrophic reason, got %q", r)
	}
	// 3 CPs: respec one at a time is fine — no flag.
	r = catastrophicRespecReason([]respecNode{{Name: "dev-cp-0", IsCP: true}}, 3, 1)
	if r != "" {
		t.Errorf("3-CP respec should NOT be catastrophic, got %q", r)
	}
}

func TestCatastrophicRespecReasonFlags1WorkerCase(t *testing.T) {
	r := catastrophicRespecReason([]respecNode{{Name: "dev-worker-0"}}, 3, 1)
	if !strings.Contains(r, "only worker") {
		t.Errorf("expected 1-worker catastrophic reason, got %q", r)
	}
}

func TestApplyExistingSpecDriftRequiresFlag(t *testing.T) {
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
	vms := &vmsWithObserved{
		observed: map[string]*protocol.Resource{
			"dev-cp-0":     vmObserved(1, 4096), // drifts (desired = 2 cpus)
			"dev-worker-0": vmObserved(2, 4096),
		},
	}
	p := New(&protocol.ProviderConfig{}, vms)
	_, err := p.Apply(context.Background(), scaleDownManifest(1))
	if err == nil {
		t.Fatal("spec drift without --allow-destructive should error")
	}
	if !strings.Contains(err.Error(), "respec") || !strings.Contains(err.Error(), "allow-destructive") {
		t.Errorf("error should mention respec + flag, got: %v", err)
	}
}

func TestApplyExistingSpecDriftOnOnlyCPCatastrophic(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
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
	vms := &vmsWithObserved{
		observed: map[string]*protocol.Resource{
			"dev-cp-0":     vmObserved(1, 4096),
			"dev-worker-0": vmObserved(2, 4096),
		},
	}
	p := New(&protocol.ProviderConfig{}, vms)
	manifest := scaleDownManifest(1)
	manifest.Metadata.Annotations = map[string]string{
		"openctl.io/allow-destructive": "true",
	}
	_, err := p.Apply(context.Background(), manifest)
	if err == nil {
		t.Fatal("respec on the only CP should be catastrophic")
	}
	if !strings.Contains(err.Error(), "catastrophic") || !strings.Contains(err.Error(), "control-plane") {
		t.Errorf("error should mention catastrophic CP, got: %v", err)
	}
}

func TestDesiredSizeForUsesPerPoolOverride(t *testing.T) {
	// Manifest with a per-pool size override.
	manifest := scaleDownManifest(2)
	manifest.Spec["nodes"].(map[string]any)["workers"] = []any{
		map[string]any{
			"name":  "worker",
			"count": float64(2),
			"size":  map[string]any{"cpus": float64(8), "memoryMB": float64(16384), "diskGB": float64(60)},
		},
	}
	spec, err := k3sresources.ParseClusterSpec(manifest)
	if err != nil {
		t.Fatal(err)
	}
	got := desiredSizeFor("dev-worker-0", false, "dev", spec)
	if got == nil {
		t.Fatal("nil size for worker")
	}
	if got.CPUs != 8 || got.MemoryMB != 16384 {
		t.Errorf("override not picked up: %+v", got)
	}
	// CP still uses cluster default.
	cp := desiredSizeFor("dev-cp-0", true, "dev", spec)
	if cp.CPUs != 2 || cp.MemoryMB != 4096 {
		t.Errorf("CP should fall through to default: %+v", cp)
	}
}
