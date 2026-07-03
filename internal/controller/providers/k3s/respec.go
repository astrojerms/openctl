package k3s

import (
	"context"
	"strings"

	k3sresources "github.com/openctl/openctl/pkg/k3s/resources"
	"github.com/openctl/openctl/pkg/protocol"
)

// respecNode describes a single existing node whose CPU/memory differs
// from what the manifest implies. In-place respec destroys then recreates
// each one in turn, then re-joins it through the Plan/dispatcher path
// (respecNodesViaPlan in respec_plan.go).
//
// Disk resize is NOT covered — the proxmox VM Get path doesn't surface
// disk size, so we have nothing to diff against. Adding disk respec would
// need both a VMToResourceWithIP change + a Proxmox API resize call;
// tracked as a future enhancement.
type respecNode struct {
	Name string
	IsCP bool
	// Desired is the cpu+memory the manifest implies for this node.
	DesiredCPUs  int
	DesiredMemMB int
	// Observed is what the VM provider reports today.
	ObservedCPUs  int
	ObservedMemMB int
}

// computeSpecRespecs returns the subset of `current` nodes whose existing
// VM CPU/memory differs from what the manifest implies. Nodes for which
// vms.Get returns nil (or errors) are skipped — we can't diff what we
// can't observe, and surfacing a false-positive respec would do
// significant damage. Names in `removed` are skipped (they're going away
// anyway).
func (p *Provider) computeSpecRespecs(
	ctx context.Context,
	clusterName string,
	spec *k3sresources.ClusterSpec,
	current []childRef,
	removed map[string]bool,
) []respecNode {
	var out []respecNode
	cpPrefix := clusterName + "-cp-"
	for _, c := range current {
		if c.Kind != "VirtualMachine" || removed[c.Name] {
			continue
		}
		isCP := strings.HasPrefix(c.Name, cpPrefix)
		desired := desiredSizeFor(c.Name, isCP, clusterName, spec)
		if desired == nil {
			continue
		}
		observed, err := p.vms.Get(ctx, "VirtualMachine", c.Name)
		if err != nil || observed == nil {
			// Can't observe — skip rather than false-positive. Both error
			// (transient Proxmox failure) and nil (VM not found via the
			// fake test fixture) get treated the same: don't risk a
			// destructive recreate on incomplete information.
			continue
		}
		obsCPUs, obsMemMB := extractCPUMem(observed)
		if obsCPUs == 0 && obsMemMB == 0 {
			// Same reasoning: a fully empty observation is unsafe to diff.
			continue
		}
		if obsCPUs == desired.CPUs && obsMemMB == desired.MemoryMB {
			continue
		}
		out = append(out, respecNode{
			Name:          c.Name,
			IsCP:          isCP,
			DesiredCPUs:   desired.CPUs,
			DesiredMemMB:  desired.MemoryMB,
			ObservedCPUs:  obsCPUs,
			ObservedMemMB: obsMemMB,
		})
	}
	return out
}

// desiredSizeFor returns the cpu/memory the manifest implies for the given
// node. For CPs: spec.nodes.controlPlane.size if set, else spec.compute.default.
// For workers: the matching pool's size if set, else spec.compute.default.
// Returns nil if the node name doesn't match any known role (defensive
// against stale state files).
func desiredSizeFor(nodeName string, isCP bool, clusterName string, spec *k3sresources.ClusterSpec) *k3sresources.DefaultSizeSpec {
	def := spec.Compute.Default
	if isCP {
		if spec.Nodes.ControlPlane.Size != nil {
			return spec.Nodes.ControlPlane.Size
		}
		return &def
	}
	// Worker: parse pool name out of "<cluster>-<pool>-<index>".
	rest := strings.TrimPrefix(nodeName, clusterName+"-")
	dash := strings.LastIndex(rest, "-")
	if dash <= 0 {
		return &def
	}
	pool := rest[:dash]
	for _, w := range spec.Nodes.Workers {
		name := w.Name
		if name == "" {
			name = "worker"
		}
		if name == pool {
			if w.Size != nil {
				return w.Size
			}
			return &def
		}
	}
	return &def
}

// extractCPUMem pulls cpu.cores and memory.size out of an observed VM
// resource (proxmox's VMToResourceWithIP shape). Returns 0,0 if the
// fields aren't present.
func extractCPUMem(r *protocol.Resource) (int, int) {
	if r == nil || r.Spec == nil {
		return 0, 0
	}
	cpus := 0
	memMB := 0
	if cpu, ok := r.Spec["cpu"].(map[string]any); ok {
		switch v := cpu["cores"].(type) {
		case int:
			cpus = v
		case int64:
			cpus = int(v)
		case float64:
			cpus = int(v)
		}
	}
	if mem, ok := r.Spec["memory"].(map[string]any); ok {
		switch v := mem["size"].(type) {
		case int:
			memMB = v
		case int64:
			memMB = int(v)
		case float64:
			memMB = int(v)
		}
	}
	return cpus, memMB
}

// catastrophicRespecReason reports why this respec set would catastrophically
// break the cluster — or returns "" if it wouldn't. The respec loop processes
// nodes one at a time, so the relevant question is whether the cluster
// can survive the gap while ANY single affected node is offline.
//
// Conditions:
//   - Respeccing the only CP (haveCPs == 1) → no apiserver during the recreate.
//   - Respeccing the only worker → no place for workloads during the recreate.
//
// Multi-CP / multi-worker respecs are not flagged; the one-at-a-time
// cadence preserves majority.
func catastrophicRespecReason(respecs []respecNode, haveCPs, haveWorkers int) string {
	var cpHit, workerHit bool
	for _, r := range respecs {
		if r.IsCP {
			cpHit = true
		} else {
			workerHit = true
		}
	}
	if cpHit && haveCPs <= 1 {
		return "respec on the only control-plane node would drop the apiserver during recreate"
	}
	if workerHit && haveWorkers <= 1 {
		return "respec on the only worker would leave no place for workloads during recreate"
	}
	return ""
}
