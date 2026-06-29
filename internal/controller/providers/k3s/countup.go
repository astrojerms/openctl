package k3s

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/pkg/k3s/agent/certs"
	k3scluster "github.com/openctl/openctl/pkg/k3s/cluster"
	k3sresources "github.com/openctl/openctl/pkg/k3s/resources"
	"github.com/openctl/openctl/pkg/protocol"
)

// applyCountUp adds the nodes in plan.addCPs/addWorkers to a live cluster.
// Returns the map of node→IP for the newly added nodes so the caller can
// merge them into status.outputs.agent.endpoints when rewriting state.
//
// Flow:
//  1. Identify a surviving CP and its IP from the saved state.
//  2. Allocate IPs for new nodes (deterministic per cluster + name).
//  3. Load the existing CA bundle from disk and mint server certs for the
//     new nodes against it (CA stays the same so existing agents continue
//     trusting the bundle).
//  4. Apply the new VMs through the VMApplier (each as a child op).
//  5. Run the Joiner: SSH into the surviving CP to read the join token,
//     install k3s + openctl-k3s-agent on each new node.
func (p *Provider) applyCountUp(
	ctx context.Context,
	rec operations.ChildRecorder,
	name string,
	spec *k3sresources.ClusterSpec,
	plan *changePlan,
	current []childRef,
	removed map[string]bool,
) (map[string]string, error) {
	// Find a surviving CP — preferred order: dev-cp-0, dev-cp-1, ... — and
	// fetch its IP from the saved agent endpoints.
	state, err := p.loadState(name)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, fmt.Errorf("count-up on missing state for cluster %q", name)
	}
	existingIPs := readAgentEndpoints(state)

	survivingCPs := []string{}
	cpPrefix := name + "-cp-"
	for _, c := range current {
		if c.Kind != "VirtualMachine" || !strings.HasPrefix(c.Name, cpPrefix) {
			continue
		}
		if removed[c.Name] {
			continue
		}
		survivingCPs = append(survivingCPs, c.Name)
	}
	sort.Strings(survivingCPs)
	if len(survivingCPs) == 0 {
		return nil, fmt.Errorf("count-up requires at least one surviving control plane; saw none")
	}
	firstCPName := survivingCPs[0]
	firstCPIP := existingIPs[firstCPName]
	if firstCPIP == "" {
		return nil, fmt.Errorf("cluster state has no IP for surviving CP %s (status.outputs.agent.endpoints missing or stale)", firstCPName)
	}

	newNodeNames := append([]string(nil), plan.addCPs...)
	newNodeNames = append(newNodeNames, plan.addWorkers...)

	// Create VMs for the new nodes (filtered subset of the full dispatch list).
	creator := k3scluster.NewCreator(name, spec, p.config)
	all := creator.GenerateDispatchRequests()
	addSet := toSet(newNodeNames)
	for _, d := range all {
		if !addSet[d.Manifest.Metadata.Name] {
			continue
		}
		if err := runChildVMApply(ctx, rec, d.Manifest, p.vms); err != nil {
			return nil, err
		}
	}

	// Resolve IPs for the new nodes. Static: deterministic from AllocateIPs.
	// DHCP+QGA: poll vms.Get until status.ip shows up. Must happen AFTER
	// VM apply because QGA reports only once the guest is up.
	newNodeIPs, err := p.resolveNodeIPs(ctx, rec, name, spec, newNodeNames)
	if err != nil {
		return nil, err
	}

	// Load existing bundle and extend it with certs for the new nodes,
	// now that we know each node's IP.
	bundleDir, err := clusterBundleDir(name)
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
	newIdentities := make([]certs.NodeIdentity, 0, len(newNodeIPs))
	for _, n := range newNodeNames {
		newIdentities = append(newIdentities, certs.NodeIdentity{Name: n, IP: newNodeIPs[n]})
	}
	if err := bundle.MintServerCerts(newIdentities); err != nil {
		return nil, fmt.Errorf("mint new server certs: %w", err)
	}

	// Run the Joiner under a single "step" child op so the user sees one
	// row for the cluster-join phase (per-node SSH happens inside the step).
	joiner := k3scluster.NewJoiner(
		name, spec, p.config,
		bundle, bundleDir,
		existingIPs,
		firstCPName, firstCPIP,
		plan.addCPs, plan.addWorkers,
		newNodeIPs,
	)
	if _, err := runChildStep(ctx, rec, name, "join-nodes",
		fmt.Sprintf("Join %d node(s) to existing cluster", len(newNodeIPs)),
		func() (any, error) { return nil, joiner.JoinNodes() }); err != nil {
		return nil, fmt.Errorf("join nodes: %w", err)
	}

	return newNodeIPs, nil
}

// readAgentEndpoints pulls the node→IP map out of a Cluster's saved
// status.outputs.agent.endpoints. Returns an empty map if any layer is
// missing or the wrong type so callers can range without nil-checks.
func readAgentEndpoints(r *protocol.Resource) map[string]string {
	out := map[string]string{}
	if r == nil || r.Status == nil {
		return out
	}
	outputs, ok := r.Status["outputs"].(map[string]any)
	if !ok {
		return out
	}
	agent, ok := outputs["agent"].(map[string]any)
	if !ok {
		return out
	}
	endpoints, ok := agent["endpoints"].(map[string]any)
	if !ok {
		return out
	}
	for name, v := range endpoints {
		if ip, ok := v.(string); ok && ip != "" {
			out[name] = ip
		}
	}
	return out
}

// clusterBundleDir resolves the per-cluster CA bundle dir
// (~/.openctl/state/k3s/<cluster>/) so the count-up path can load the
// existing bundle and persist the extended one back.
func clusterBundleDir(name string) (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}
