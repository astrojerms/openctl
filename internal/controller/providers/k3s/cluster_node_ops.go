package k3s

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/pkg/k3s/agent/certs"
)

// Wiring for the per-node `logs` and `restart` Cluster actions. Both drive a
// node's openctl-k3s-agent over mTLS, reusing the same node enumeration + cert
// bundle + agent-client construction the rolling upgrade uses
// (enumerateUpgradeNodes / agentNodeUpgrader). Surfaced as parameterized
// actions so the UI renders a `node` input (and `lines` for logs) via the
// U10.1 action-parameter form.

// nodeAgent is the per-node capability these actions need. agentNodeUpgrader
// implements it; the factory seam keeps the orchestration testable without a
// live agent.
type nodeAgent interface {
	Logs(ctx context.Context, node upgradeNode, lines int) (string, error)
	Restart(ctx context.Context, node upgradeNode) error
}

type nodeAgentFactory func(bundle agentCertBundle, endpoints map[string]string) nodeAgent

// productionNodeAgentFactory builds the real agent-backed node agent.
func productionNodeAgentFactory(bundle agentCertBundle, endpoints map[string]string) nodeAgent {
	return newAgentNodeUpgrader(bundle, endpoints)
}

// defaultLogLines is used when the `lines` parameter is absent or unparseable.
const defaultLogLines = 200

// loadClusterNodeAgent enumerates the cluster's installed nodes and builds a
// nodeAgent bound to its mTLS bundle. Shared by the logs and restart actions.
func (p *Provider) loadClusterNodeAgent(clusterName string, factory nodeAgentFactory) ([]upgradeNode, nodeAgent, error) {
	nodes, endpoints, err := enumerateUpgradeNodes(clusterName)
	if err != nil {
		return nil, nil, err
	}
	if len(nodes) == 0 {
		return nil, nil, fmt.Errorf("cluster %q has no installed nodes", clusterName)
	}
	bundleDir, err := clusterBundleDir(clusterName)
	if err != nil {
		return nil, nil, err
	}
	bundle, err := certs.LoadBundle(bundleDir)
	if err != nil {
		return nil, nil, fmt.Errorf("load cluster %q cert bundle: %w", clusterName, err)
	}
	agent := factory(agentCertBundle{
		CACertPEM:     bundle.CACertPEM,
		ClientCertPEM: bundle.ClientCertPEM,
		ClientKeyPEM:  bundle.ClientKeyPEM,
	}, endpoints)
	return nodes, agent, nil
}

// selectNode resolves the target node for a per-node action. An explicit name
// must match; an empty name auto-selects when the cluster has exactly one node,
// otherwise it errors asking for a `node` parameter.
func selectNode(nodes []upgradeNode, name string) (upgradeNode, error) {
	name = strings.TrimSpace(name)
	if name != "" {
		for _, n := range nodes {
			if n.Name == name {
				return n, nil
			}
		}
		return upgradeNode{}, fmt.Errorf("node %q not found in cluster (nodes: %s)", name, nodeNames(nodes))
	}
	if len(nodes) == 1 {
		return nodes[0], nil
	}
	return upgradeNode{}, fmt.Errorf("cluster has %d nodes; specify a 'node' parameter (%s)", len(nodes), nodeNames(nodes))
}

func nodeNames(nodes []upgradeNode) string {
	names := make([]string, len(nodes))
	for i, n := range nodes {
		names[i] = n.Name
	}
	return strings.Join(names, ", ")
}

// runClusterLogs fetches a node's k3s journal and returns it as a downloadable
// file (the journal is long — a download is more useful than an inline toast).
func (p *Provider) runClusterLogs(ctx context.Context, clusterName string, params map[string]string, factory nodeAgentFactory) (*providers.ActionResult, error) {
	nodes, agent, err := p.loadClusterNodeAgent(clusterName, factory)
	if err != nil {
		return nil, err
	}
	node, err := selectNode(nodes, params["node"])
	if err != nil {
		return nil, err
	}
	lines := defaultLogLines
	if v := strings.TrimSpace(params["lines"]); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			lines = n
		}
	}
	out, err := agent.Logs(ctx, node, lines)
	if err != nil {
		return nil, fmt.Errorf("fetch logs from node %q: %w", node.Name, err)
	}
	return &providers.ActionResult{
		DownloadContent:  out,
		DownloadFilename: fmt.Sprintf("%s-%s-k3s.log", clusterName, node.Name),
		Message:          fmt.Sprintf("Fetched %d lines from %s", lines, node.Name),
	}, nil
}

// runClusterRestart restarts k3s on a node via its agent.
func (p *Provider) runClusterRestart(ctx context.Context, clusterName string, params map[string]string, factory nodeAgentFactory) (*providers.ActionResult, error) {
	nodes, agent, err := p.loadClusterNodeAgent(clusterName, factory)
	if err != nil {
		return nil, err
	}
	node, err := selectNode(nodes, params["node"])
	if err != nil {
		return nil, err
	}
	if err := agent.Restart(ctx, node); err != nil {
		return nil, fmt.Errorf("restart k3s on node %q: %w", node.Name, err)
	}
	return &providers.ActionResult{Message: fmt.Sprintf("Restarted k3s on %s", node.Name)}, nil
}
