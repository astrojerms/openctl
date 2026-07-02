package k3s

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/pkg/protocol"
)

// addNodesViaPlan adds plan.addCPs/addWorkers to a live cluster through the
// ChildDispatcher — the Plan-driven counterpart to applyCountUp. It emits
// the cluster's Plan(), keeps only the children for the newly-added nodes,
// points each new K3sNode's join refs at a surviving control plane, and
// applies the VM → K3sNode → AgentInstall children via cd.ApplyChild.
//
// Nothing here SSHes or touches certs directly: each K3sNode's applyK3sNode
// resolves its join token/URL from the surviving CP's state via the $ref,
// and each AgentInstall extends the on-disk CA bundle with the node's
// server cert as part of its own Apply. So the imperative Joiner + explicit
// MintServerCerts step that applyCountUp runs are unnecessary here.
//
// Returns node→IP for the added nodes (read back from the state each
// K3sNode persisted) so the caller can merge them into
// status.outputs.agent.endpoints.
func (p *Provider) addNodesViaPlan(
	ctx context.Context,
	cd operations.ChildDispatcher,
	manifest *protocol.Resource,
	name string,
	plan *changePlan,
	current []childRef,
	removed map[string]bool,
) (map[string]string, error) {
	survivingCP, err := survivingControlPlane(name, current, removed)
	if err != nil {
		return nil, err
	}

	planResult, err := p.Plan(ctx, manifest)
	if err != nil {
		return nil, fmt.Errorf("plan cluster for count-up: %w", err)
	}

	addNames := append(append([]string{}, plan.addCPs...), plan.addWorkers...)
	addSet := toSet(addNames)

	// Bucket the added nodes' children by kind so they can be applied in
	// phases (all VMs, then all K3sNodes, then all AgentInstalls) — a new
	// K3sNode must find a running CP to resolve its join ref against.
	var vms, k3sNodes, agents []*protocol.Resource
	for _, c := range planResult.Children {
		switch c.Kind {
		case "VirtualMachine":
			if addSet[c.Metadata.Name] {
				vms = append(vms, c)
			}
		case kindK3sNode:
			if addSet[c.Metadata.Name] {
				// Every added node JOINS the cluster — never initializes it.
				// Plan strips the join refs off the first CP (index 0); if a
				// re-added cp-0 lands here that would make it try to bootstrap
				// a second cluster, so set the refs unconditionally.
				setJoinRef(c, survivingCP)
				k3sNodes = append(k3sNodes, c)
			}
		case kindAgentInstall:
			if addSet[strings.TrimSuffix(c.Metadata.Name, "-agent")] {
				agents = append(agents, c)
			}
		}
	}

	for _, group := range [][]*protocol.Resource{vms, k3sNodes, agents} {
		for _, child := range group {
			if _, err := cd.ApplyChild(ctx, child); err != nil {
				return nil, err
			}
		}
	}

	// Read the IP each K3sNode resolved (static, or discovered via QGA) back
	// out of its persisted state for the endpoints merge.
	endpoints := make(map[string]string, len(k3sNodes))
	for _, kn := range k3sNodes {
		if st, err := loadNodeState(kn.Metadata.Name); err == nil && st != nil && st.VMIP != "" {
			endpoints[kn.Metadata.Name] = st.VMIP
		}
	}
	return endpoints, nil
}

// survivingControlPlane returns the first (sorted) control-plane node that
// exists in state and is not being removed in this converge — the CP new
// nodes join. Mirrors the selection applyCountUp makes.
func survivingControlPlane(clusterName string, current []childRef, removed map[string]bool) (string, error) {
	cpPrefix := clusterName + "-cp-"
	var cps []string
	for _, c := range current {
		if c.Kind == "VirtualMachine" && strings.HasPrefix(c.Name, cpPrefix) && !removed[c.Name] {
			cps = append(cps, c.Name)
		}
	}
	sort.Strings(cps)
	if len(cps) == 0 {
		return "", fmt.Errorf("count-up requires at least one surviving control plane; saw none")
	}
	return cps[0], nil
}

// setJoinRef points a K3sNode's join refs at cpName's K3sNode state so the
// node joins the existing cluster (resolving status.nodeToken +
// status.vmIP) rather than initializing a new one.
func setJoinRef(k3sNode *protocol.Resource, cpName string) {
	k3sNode.Spec["joinFrom"] = map[string]any{
		"$ref": map[string]any{
			"apiVersion": "k3s.openctl.io/v1",
			"kind":       kindK3sNode,
			"name":       cpName,
			"field":      "status.nodeToken",
		},
	}
	k3sNode.Spec["joinURLFrom"] = map[string]any{
		"$ref": map[string]any{
			"apiVersion": "k3s.openctl.io/v1",
			"kind":       kindK3sNode,
			"name":       cpName,
			"field":      "status.vmIP",
		},
	}
}
