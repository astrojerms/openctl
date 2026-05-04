package k3s

import (
	"sort"

	k3sresources "github.com/openctl/openctl/pkg/k3s/resources"
)

// changePlan describes the structural difference between an applied
// manifest and the existing children of a Cluster. The k3s provider's Apply
// uses this to enforce destructive/catastrophic guardrails and to drive the
// VM-level convergence (cascade delete on removals; add path is a Phase 5.x
// followup — see CONTROLLER.md).
type changePlan struct {
	// Node names that exist in state but not in the new manifest's expected
	// set. Removing these is destructive (requires --allow-destructive).
	removeCPs     []string
	removeWorkers []string
	// Node names in the new manifest but not yet in state. Adding these
	// requires Phase 5.x followup work (live-cluster join).
	addCPs     []string
	addWorkers []string
}

// hasChanges reports whether the plan implies any structural difference.
func (p *changePlan) hasChanges() bool {
	return len(p.removeCPs)+len(p.removeWorkers)+len(p.addCPs)+len(p.addWorkers) > 0
}

// removesAny reports whether the plan removes any node — the trigger for
// the --allow-destructive gate.
func (p *changePlan) removesAny() bool {
	return len(p.removeCPs)+len(p.removeWorkers) > 0
}

// computeChangePlan compares the manifest's expected node names against the
// children currently listed in state. Names follow the patterns set by
// resources.NodeNames: `<cluster>-cp-<i>` for control-plane nodes,
// `<cluster>-<pool>-<i>` for workers.
func computeChangePlan(clusterName string, manifestSpec *k3sresources.ClusterSpec, current []childRef) *changePlan {
	wantCPs, wantWorkers := k3sresources.NodeNames(clusterName, manifestSpec)
	wantCPSet := toSet(wantCPs)
	wantWorkerSet := toSet(wantWorkers)

	haveCPSet := map[string]bool{}
	haveWorkerSet := map[string]bool{}
	cpPrefix := clusterName + "-cp-"
	for _, c := range current {
		if c.Kind != "VirtualMachine" {
			continue
		}
		switch {
		case len(c.Name) > len(cpPrefix) && c.Name[:len(cpPrefix)] == cpPrefix:
			haveCPSet[c.Name] = true
		default:
			haveWorkerSet[c.Name] = true
		}
	}

	plan := &changePlan{}
	for name := range haveCPSet {
		if !wantCPSet[name] {
			plan.removeCPs = append(plan.removeCPs, name)
		}
	}
	for name := range haveWorkerSet {
		if !wantWorkerSet[name] {
			plan.removeWorkers = append(plan.removeWorkers, name)
		}
	}
	for _, name := range wantCPs {
		if !haveCPSet[name] {
			plan.addCPs = append(plan.addCPs, name)
		}
	}
	for _, name := range wantWorkers {
		if !haveWorkerSet[name] {
			plan.addWorkers = append(plan.addWorkers, name)
		}
	}
	sort.Strings(plan.removeCPs)
	sort.Strings(plan.removeWorkers)
	sort.Strings(plan.addCPs)
	sort.Strings(plan.addWorkers)
	return plan
}

func toSet(s []string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[v] = true
	}
	return m
}

// catastrophicReason reports why applying this plan would catastrophically
// break the cluster — or returns "" if it wouldn't. The caller blocks with
// FailedPrecondition unless the user passes --i-know-this-breaks-the-cluster.
//
// Catastrophic conditions:
//   - The plan removes the only control-plane node (cluster has no kube-apiserver).
//   - The plan would drop the control-plane count below quorum
//     (a multi-CP cluster needs at least majority for HA; losing majority
//     is catastrophic). The threshold is ceil((haveCP+1)/2) based on the
//     classic Raft majority — for n=3 you can lose 1, for n=5 you can lose 2.
//   - The plan removes the last worker pool member (no place for workloads
//     to schedule, on the assumption CPs are tainted).
func catastrophicReason(plan *changePlan, haveCPCount, haveWorkerCount int) string {
	remainCPs := haveCPCount - len(plan.removeCPs)
	if remainCPs <= 0 {
		return "would remove the only control-plane node, leaving no kube-apiserver"
	}
	if haveCPCount > 1 {
		// Quorum is majority of *original* CP count for the cluster. Standard
		// Raft majority: ceil((n+1)/2) survivors needed. If post-plan count
		// drops below that, etcd loses quorum and the cluster halts.
		quorum := (haveCPCount + 2) / 2 // == ceil((n+1)/2)
		if remainCPs < quorum {
			return "would drop control-plane below quorum (etcd cannot make progress)"
		}
	}
	if haveWorkerCount > 0 && haveWorkerCount-len(plan.removeWorkers) <= 0 {
		return "would remove the last worker, leaving no place for workloads to schedule"
	}
	return ""
}
