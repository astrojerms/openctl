package k3s

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/pkg/k3s/agent/bootstrap"
	"github.com/openctl/openctl/pkg/k3s/agent/certs"
)

// Wiring for the cluster-wide `upgrade` action: enumerate a Cluster's nodes
// from state, then run the tested rolling-upgrade core (rollingUpgrade) over
// the production agent upgrader (agentNodeUpgrader). Invoked via the
// parameterized action path (DoActionWithParams) with params["version"].

// upgraderFactory builds the nodeUpgrader from the cluster's mTLS bundle and
// the node->endpoint map. Injected so DoActionWithParams' orchestration is
// testable without live agents; production uses newAgentNodeUpgrader.
type upgraderFactory func(bundle agentCertBundle, endpoints map[string]string) nodeUpgrader

// enumerateUpgradeNodes lists a Cluster's nodes with their roles and agent
// endpoints, read from persisted state. A Cluster's state file records its
// VirtualMachine children by name; each node's role + IP live in the K3sNode
// install state under the same name (both are stamped with the same nodeName at
// plan time). Nodes with no install state (never provisioned) are skipped.
func enumerateUpgradeNodes(clusterName string) (nodes []upgradeNode, endpoints map[string]string, err error) {
	children, err := readChildren(clusterName)
	if err != nil {
		return nil, nil, fmt.Errorf("read cluster %q state: %w", clusterName, err)
	}
	endpoints = map[string]string{}
	for _, c := range children {
		if c.Kind != "VirtualMachine" {
			continue
		}
		st, err := loadNodeState(c.Name)
		if err != nil || st == nil || !st.Installed {
			continue // no k3s install recorded for this node yet
		}
		nodes = append(nodes, upgradeNode{Name: c.Name, Role: st.Role})
		if st.VMIP != "" {
			endpoints[c.Name] = net.JoinHostPort(st.VMIP, strconv.Itoa(bootstrap.Port))
		}
	}
	// Deterministic order in (before upgradeOrder re-groups by role).
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	return nodes, endpoints, nil
}

// runClusterUpgrade is the orchestration for the `upgrade` action: enumerate,
// build the upgrader via factory, and run the tested rolling-upgrade core. The
// factory seam keeps this unit-testable without live agents.
func (p *Provider) runClusterUpgrade(ctx context.Context, clusterName, version string, factory upgraderFactory) (*providers.ActionResult, error) {
	if strings.TrimSpace(version) == "" {
		return nil, fmt.Errorf("upgrade requires a 'version' parameter (e.g. v1.30.5+k3s1)")
	}
	nodes, endpoints, err := enumerateUpgradeNodes(clusterName)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("cluster %q has no installed nodes to upgrade", clusterName)
	}

	bundleDir, err := clusterBundleDir(clusterName)
	if err != nil {
		return nil, err
	}
	bundle, err := certs.LoadBundle(bundleDir)
	if err != nil {
		return nil, fmt.Errorf("load cluster %q cert bundle: %w", clusterName, err)
	}

	u := factory(agentCertBundle{
		CACertPEM:     bundle.CACertPEM,
		ClientCertPEM: bundle.ClientCertPEM,
		ClientKeyPEM:  bundle.ClientKeyPEM,
	}, endpoints)

	// Idempotency pre-pass: query each node's current k3s version so
	// rollingUpgrade can skip nodes already at the target. A node that doesn't
	// answer (down / unreachable) is left at unknown version and thus attempted,
	// which is the safe default. This makes a re-run after a partial/halted
	// upgrade resume from where it stopped instead of re-swapping every node.
	nodes = withCurrentVersions(ctx, nodes, u)

	res, err := rollingUpgrade(ctx, nodes, version, u)
	msg := fmt.Sprintf("upgraded %d node(s) to %s", len(res.Upgraded), version)
	if len(res.Skipped) > 0 {
		msg += fmt.Sprintf(" (%d already at target)", len(res.Skipped))
	}
	if err != nil {
		// A halt leaves the cluster mixed-version-but-running; report progress
		// alongside the halting error so the caller can re-run to finish.
		return nil, fmt.Errorf("%s; halted: %w", msg, err)
	}
	return &providers.ActionResult{Message: msg}, nil
}

// withCurrentVersions returns nodes with their Version populated from the
// upgrader's health/version query, so the rolling upgrade can idempotently skip
// nodes already at the target. A node whose version can't be read is left
// unknown (empty) and will be attempted.
func withCurrentVersions(ctx context.Context, nodes []upgradeNode, u nodeUpgrader) []upgradeNode {
	out := make([]upgradeNode, len(nodes))
	for i, n := range nodes {
		out[i] = n
		if v, err := u.Health(ctx, n); err == nil {
			out[i].Version = v
		}
	}
	return out
}

// productionUpgraderFactory builds the real agent-backed upgrader.
func productionUpgraderFactory(bundle agentCertBundle, endpoints map[string]string) nodeUpgrader {
	return newAgentNodeUpgrader(bundle, endpoints)
}
