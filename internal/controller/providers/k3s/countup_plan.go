package k3s

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openctl/openctl/internal/config"
	"github.com/openctl/openctl/internal/controller/operations"
	k3sresources "github.com/openctl/openctl/pkg/k3s/resources"
	"github.com/openctl/openctl/pkg/k3s/ssh"
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
	survivingCP, err := survivingControlPlane(name, current, removed, nil)
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
				// a second cluster, so set the join unconditionally.
				if err := p.setJoin(c, survivingCP, manifest, name, current, removed); err != nil {
					return nil, err
				}
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
// exists in state, is not being removed in this converge, and is not in
// exclude — the CP that new or recreated nodes join. exclude lets a respec
// skip the node currently being torn down (it can't join itself). Mirrors
// the selection applyCountUp/applyRespecs make.
func survivingControlPlane(clusterName string, current []childRef, removed, exclude map[string]bool) (string, error) {
	cpPrefix := clusterName + "-cp-"
	var cps []string
	for _, c := range current {
		if c.Kind != "VirtualMachine" || !strings.HasPrefix(c.Name, cpPrefix) {
			continue
		}
		if removed[c.Name] || exclude[c.Name] {
			continue
		}
		cps = append(cps, c.Name)
	}
	sort.Strings(cps)
	if len(cps) == 0 {
		return "", fmt.Errorf("no surviving control plane to join (all removed or excluded)")
	}
	return cps[0], nil
}

// readCPNodeToken reads a control plane's k3s join token over SSH. A package
// var so the legacy-CP join path is unit-testable without a live node.
var readCPNodeToken = func(host, user, keyPath string) (string, error) {
	client, err := ssh.WaitForSSH(host, sshPort, user, keyPath, 60*time.Second)
	if err != nil {
		return "", err
	}
	defer func() { _ = client.Close() }()
	tok, err := client.RunSudo("cat /var/lib/rancher/k3s/server/node-token")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(tok), nil
}

// setJoin points a new node at the surviving control plane. When that CP has a
// K3sNode resource (clusters built via the Plan/K3sNode path), it uses a $ref
// so the ref resolver serializes install ordering. For a LEGACY cluster whose
// CP was installed inline — no K3sNode resource, so the $ref would fail with
// "K3sNode <cp> not found" — it resolves the join token + IP directly from
// cluster state + SSH and sets concrete values instead. This is what lets a
// pre-K3sNode cluster be scaled/converged through the Plan path.
func (p *Provider) setJoin(k3sNode *protocol.Resource, cpName string, manifest *protocol.Resource, clusterName string, current []childRef, removed map[string]bool) error {
	if st, err := loadNodeState(cpName); err == nil && st != nil {
		setJoinRef(k3sNode, cpName) // K3sNode resource exists → $ref
		return nil
	}
	// Legacy CP: resolve the join token + IP concretely.
	cpIP, err := p.survivingCPEndpoint(clusterName, current, removed, nil)
	if err != nil || cpIP == "" {
		return fmt.Errorf("legacy control plane %q has no K3sNode resource and no known endpoint to resolve its join token: %w", cpName, err)
	}
	spec, err := k3sresources.ParseClusterSpec(manifest)
	if err != nil {
		return err
	}
	keyPath := spec.SSH.PrivateKeyPath
	if exp, expErr := config.ExpandPath(keyPath); expErr == nil {
		keyPath = exp
	}
	token, err := readCPNodeToken(cpIP, spec.SSH.User, keyPath)
	if err != nil {
		return fmt.Errorf("read node-token from legacy CP %q (%s): %w", cpName, cpIP, err)
	}
	// applyK3sNode accepts a bare-string joinFrom (the token) and joinURLFrom
	// (the CP IP), bypassing ref resolution.
	k3sNode.Spec["joinFrom"] = token
	k3sNode.Spec["joinURLFrom"] = cpIP
	return nil
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
