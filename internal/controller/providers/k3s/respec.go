package k3s

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/pkg/k3s/agent/certs"
	k3scluster "github.com/openctl/openctl/pkg/k3s/cluster"
	k3sresources "github.com/openctl/openctl/pkg/k3s/resources"
	"github.com/openctl/openctl/pkg/protocol"
)

// respecNode describes a single existing node whose CPU/memory differs
// from what the manifest implies. Phase 5.x in-place respec destroys then
// recreates each one in turn, then re-joins it via the Joiner.
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

// applyRespecs runs destroy → recreate → rejoin for each respec node,
// one at a time. Returns the map of node-name → new IP for callers that
// want to refresh agent endpoints (same as count-up). For static-IP
// clusters this map is also same-key-same-value but the controller
// re-issues the Joiner so the agent-side cert + service redeploy.
func (p *Provider) applyRespecs(
	ctx context.Context,
	rec operations.ChildRecorder,
	clusterName string,
	spec *k3sresources.ClusterSpec,
	respecs []respecNode,
	existingIPs map[string]string,
	firstCPName, firstCPIP string,
) (map[string]string, error) {
	if len(respecs) == 0 {
		return nil, nil
	}
	bundleDir, err := clusterBundleDir(clusterName)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(bundleDir); err != nil {
		return nil, fmt.Errorf("cluster bundle dir %s missing (was this cluster created by the controller?): %w", bundleDir, err)
	}
	bundle, err := certs.LoadBundle(bundleDir)
	if err != nil {
		return nil, fmt.Errorf("load existing bundle: %w", err)
	}

	// Build a quick name → desired manifest map from the cluster's full
	// dispatch list so we don't reconstruct the per-node manifest by hand.
	creator := k3scluster.NewCreator(clusterName, spec, p.config)
	manifestsByName := map[string]*protocol.Resource{}
	for _, d := range creator.GenerateDispatchRequests() {
		manifestsByName[d.Manifest.Metadata.Name] = d.Manifest
	}

	updated := map[string]string{}
	for _, r := range respecs {
		newManifest, ok := manifestsByName[r.Name]
		if !ok {
			return nil, fmt.Errorf("respec: no manifest derivable for %s", r.Name)
		}

		// Destroy the existing VM.
		if err := runChildVMDelete(ctx, rec, r.Name, p.vms); err != nil {
			return nil, fmt.Errorf("respec delete %s: %w", r.Name, err)
		}

		// Recreate with the new spec.
		if err := runChildVMApply(ctx, rec, newManifest, p.vms); err != nil {
			return nil, fmt.Errorf("respec recreate %s: %w", r.Name, err)
		}

		// Re-resolve the IP (deterministic for static, QGA poll for DHCP).
		nodeIPs, err := p.resolveNodeIPs(ctx, rec, clusterName, spec, []string{r.Name})
		if err != nil {
			return nil, err
		}
		newIP := nodeIPs[r.Name]
		updated[r.Name] = newIP

		// The cert for this node uses CommonName + IP-as-SAN. The CommonName
		// doesn't change (same node name) but the IP might (DHCP). Re-mint
		// to be safe — same CA, new leaf.
		if err := bundle.MintServerCerts([]certs.NodeIdentity{{Name: r.Name, IP: newIP}}); err != nil {
			return nil, fmt.Errorf("respec re-mint cert for %s: %w", r.Name, err)
		}

		// Rejoin: the node is "new" to k3s after the destroy. Reuse the
		// Joiner; only the one node is in scope.
		var cps, workers []string
		if r.IsCP {
			cps = []string{r.Name}
		} else {
			workers = []string{r.Name}
		}
		joiner := k3scluster.NewJoiner(
			clusterName, spec, p.config,
			bundle, bundleDir,
			existingIPs,
			firstCPName, firstCPIP,
			cps, workers,
			map[string]string{r.Name: newIP},
		)
		if _, err := runChildStep(ctx, rec, clusterName, "respec-rejoin/"+r.Name,
			fmt.Sprintf("Rejoin %s after respec (%d→%d cpu, %d→%d MB)",
				r.Name, r.ObservedCPUs, r.DesiredCPUs, r.ObservedMemMB, r.DesiredMemMB),
			func() (any, error) { return nil, joiner.JoinNodes() }); err != nil {
			return nil, fmt.Errorf("respec rejoin %s: %w", r.Name, err)
		}
	}
	return updated, nil
}
