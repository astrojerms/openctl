package k3s

import (
	"context"
	"fmt"
	"log"

	"github.com/openctl/openctl/internal/controller/operations"
	k3sresources "github.com/openctl/openctl/pkg/k3s/resources"
	"github.com/openctl/openctl/pkg/protocol"
)

// respecNodesViaPlan applies the given cpu/memory respecs through the
// ChildDispatcher — the Plan-driven counterpart to applyRespecs. For each
// affected node, one at a time (to preserve quorum), it:
//
//  1. Tears down the node's full child set via deleteNodeChildren
//     (DeleteChild {AgentInstall, K3sNode, VM}). Removing the K3sNode state
//     is what lets the re-apply reinstall rather than no-op on Installed.
//  2. Re-applies the node's Plan()-emitted children — VM at the desired
//     size (Plan emits the manifest's sizing, which is what triggered the
//     respec), K3sNode joined to a surviving CP other than this node, and
//     AgentInstall.
//
// Returns node→IP for the recreated nodes (read back from the state each
// K3sNode persisted) for the endpoints merge.
func (p *Provider) respecNodesViaPlan(
	ctx context.Context,
	cd operations.ChildDispatcher,
	manifest *protocol.Resource,
	name string,
	spec *k3sresources.ClusterSpec,
	respecs []respecNode,
	current []childRef,
	removed map[string]bool,
) (map[string]string, error) {
	planResult, err := p.Plan(ctx, manifest)
	if err != nil {
		return nil, fmt.Errorf("plan cluster for respec: %w", err)
	}
	byKindName := make(map[string]*protocol.Resource, len(planResult.Children))
	for _, c := range planResult.Children {
		byKindName[c.Kind+"/"+c.Metadata.Name] = c
	}

	endpoints := make(map[string]string, len(respecs))
	for _, r := range respecs {
		node := r.Name
		// The recreated node must rejoin a surviving CP that is not itself —
		// it's down during its own respec.
		survivingCP, err := survivingControlPlane(name, current, removed, map[string]bool{node: true})
		if err != nil {
			return nil, fmt.Errorf("respec %s: %w", node, err)
		}

		vm := byKindName["VirtualMachine/"+node]
		k3sNode := byKindName[kindK3sNode+"/"+node]
		agent := byKindName[kindAgentInstall+"/"+node+"-agent"]
		if vm == nil || k3sNode == nil || agent == nil {
			return nil, fmt.Errorf("respec %s: plan is missing children for this node", node)
		}

		// Destroy the current node (clears its K3sNode/AgentInstall state).
		if err := p.deleteNodeChildren(ctx, cd, spec, node); err != nil {
			return nil, fmt.Errorf("respec %s: destroy: %w", node, err)
		}

		// Evict the node's server-side state before recreating it under the
		// same hostname: the Node object and, crucially, the node-password
		// secret. The recreated agent generates a fresh node password, and
		// k3s would otherwise reject it as a duplicate hostname whose
		// password no longer matches. Best-effort via a surviving CP.
		if cpIP, err := p.survivingCPEndpoint(name, current, removed, map[string]bool{node: true}); err == nil {
			p.evictK8sNode(cpIP, spec, node)
		} else {
			log.Printf("k3s converge: respec %s: skip cluster eviction: %v", node, err)
		}

		// Recreate at the desired size, rejoining the surviving CP.
		if err := p.setJoin(k3sNode, survivingCP, manifest, name, current, removed); err != nil {
			return nil, fmt.Errorf("respec %s: resolve join: %w", node, err)
		}
		for _, child := range []*protocol.Resource{vm, k3sNode, agent} {
			if _, err := cd.ApplyChild(ctx, child); err != nil {
				return nil, fmt.Errorf("respec %s: recreate: %w", node, err)
			}
		}

		if st, err := loadNodeState(node); err == nil && st != nil && st.VMIP != "" {
			endpoints[node] = st.VMIP
		}
	}
	return endpoints, nil
}
