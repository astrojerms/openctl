package k3s

import (
	"reflect"
	"testing"

	k3sresources "github.com/openctl/openctl/pkg/k3s/resources"
)

func newSpec(cps int, workerPools ...workerPool) *k3sresources.ClusterSpec {
	s := &k3sresources.ClusterSpec{}
	s.Nodes.ControlPlane.Count = cps
	for _, wp := range workerPools {
		s.Nodes.Workers = append(s.Nodes.Workers, k3sresources.WorkerSpec{
			Name:  wp.name,
			Count: wp.count,
		})
	}
	return s
}

type workerPool struct {
	name  string
	count int
}

func vmChild(name string) childRef {
	return childRef{Provider: "proxmox", Kind: "VirtualMachine", Name: name}
}

func TestComputeChangePlanNoChange(t *testing.T) {
	spec := newSpec(1, workerPool{"worker", 1})
	current := []childRef{vmChild("dev-cp-0"), vmChild("dev-worker-0")}
	plan := computeChangePlan("dev", spec, current)
	if plan.hasChanges() {
		t.Errorf("expected no changes, got %+v", plan)
	}
}

func TestComputeChangePlanScaleDownWorkers(t *testing.T) {
	spec := newSpec(1, workerPool{"worker", 1})
	current := []childRef{vmChild("dev-cp-0"), vmChild("dev-worker-0"), vmChild("dev-worker-1")}
	plan := computeChangePlan("dev", spec, current)
	if !reflect.DeepEqual(plan.removeWorkers, []string{"dev-worker-1"}) {
		t.Errorf("removeWorkers = %v, want [dev-worker-1]", plan.removeWorkers)
	}
	if len(plan.removeCPs) != 0 || len(plan.addCPs) != 0 || len(plan.addWorkers) != 0 {
		t.Errorf("unexpected non-worker-removal change: %+v", plan)
	}
}

func TestComputeChangePlanScaleUpWorkers(t *testing.T) {
	spec := newSpec(1, workerPool{"worker", 2})
	current := []childRef{vmChild("dev-cp-0"), vmChild("dev-worker-0")}
	plan := computeChangePlan("dev", spec, current)
	if !reflect.DeepEqual(plan.addWorkers, []string{"dev-worker-1"}) {
		t.Errorf("addWorkers = %v, want [dev-worker-1]", plan.addWorkers)
	}
	if plan.removesAny() {
		t.Errorf("unexpected removals: %+v", plan)
	}
}

func TestCatastrophicLastCP(t *testing.T) {
	plan := &changePlan{removeCPs: []string{"dev-cp-0"}}
	got := catastrophicReason(plan, 1, 2)
	if got == "" {
		t.Error("removing only CP should be catastrophic")
	}
}

func TestCatastrophicQuorumLoss(t *testing.T) {
	// 3 CPs → quorum is 2; removing 2 leaves 1 < 2 → catastrophic.
	plan := &changePlan{removeCPs: []string{"dev-cp-0", "dev-cp-1"}}
	if got := catastrophicReason(plan, 3, 2); got == "" {
		t.Error("dropping 3-CP cluster to 1 CP should be catastrophic")
	}
	// Removing 1 of 3 leaves 2 >= 2 → fine.
	plan = &changePlan{removeCPs: []string{"dev-cp-0"}}
	if got := catastrophicReason(plan, 3, 2); got != "" {
		t.Errorf("dropping 3-CP cluster to 2 CPs should be safe, got: %s", got)
	}
}

func TestCatastrophicLastWorker(t *testing.T) {
	plan := &changePlan{removeWorkers: []string{"dev-worker-0"}}
	if got := catastrophicReason(plan, 1, 1); got == "" {
		t.Error("removing the only worker should be catastrophic")
	}
}

func TestCatastrophicSafePlan(t *testing.T) {
	// Removing a worker when others remain: not catastrophic.
	plan := &changePlan{removeWorkers: []string{"dev-worker-1"}}
	if got := catastrophicReason(plan, 1, 2); got != "" {
		t.Errorf("safe plan should not be catastrophic, got: %s", got)
	}
}
