package k3s

import (
	"context"
	"fmt"
)

// Cluster-wide rolling upgrade — orchestration core.
//
// This is the pure, injectable heart of the cluster `upgrade` action designed
// in docs/k3s-rolling-upgrade.md: upgrade every node to a target k3s version
// in a safe order (control planes first for etcd quorum, then workers),
// serially, skipping nodes already at the target (idempotent), and halting on
// the first node that fails to come back healthy. The messy wiring — reading
// cluster state, loading the per-cluster mTLS bundle, building a real agent
// client per node — is layered on top of this and gated on multi-node
// validation (the one step that needs hardware). Keeping the orchestration
// pure and interface-driven means the safety-critical logic (ordering, quorum,
// halt-on-failure, idempotency) is fully unit-tested without a real cluster.
//
// The `--drain` opt-in from the design is deliberately out of this core: the
// default path is no-drain (matching the cluster's documented homelab
// assumption) and takes no k8s-client dependency.

// Node roles as recorded in K3sNode state.
const (
	roleServer = "server" // control plane
	roleAgent  = "agent"  // worker
)

// upgradeNode is one node considered for upgrade.
type upgradeNode struct {
	Name    string
	Role    string // roleServer | roleAgent
	Version string // current running k3s version; "" if unknown
}

// nodeUpgrader upgrades a single node and reports its post-upgrade health.
// Injected so rollingUpgrade is testable without a live agent; the production
// implementation calls the per-node agent client (UpgradeK3s + Info).
type nodeUpgrader interface {
	// Upgrade swaps the node's k3s binary to version and restarts it.
	Upgrade(ctx context.Context, node upgradeNode, version string) error
	// Health blocks until the node is Ready and returns its running k3s
	// version, or errors if it does not come back healthy in time.
	Health(ctx context.Context, node upgradeNode) (version string, err error)
}

// upgradeOrder returns nodes in safe upgrade order: control planes
// (roleServer) first, then workers (roleAgent), each group preserving input
// order. Control planes lead so an HA cluster upgrades its apiserver/etcd
// members before the workers that depend on them; serial application of this
// order (see rollingUpgrade) preserves etcd quorum. Deterministic.
func upgradeOrder(nodes []upgradeNode) []upgradeNode {
	ordered := make([]upgradeNode, 0, len(nodes))
	for _, n := range nodes {
		if n.Role == roleServer {
			ordered = append(ordered, n)
		}
	}
	for _, n := range nodes {
		if n.Role != roleServer {
			ordered = append(ordered, n)
		}
	}
	return ordered
}

// UpgradeResult reports the outcome of a rolling upgrade.
type UpgradeResult struct {
	Target   string   // the target version requested
	Upgraded []string // node names actually upgraded (in order)
	Skipped  []string // node names already at target (idempotent skip)
}

// rollingUpgrade upgrades every node to targetVersion in upgradeOrder,
// serially. A node already at targetVersion is skipped (idempotent — re-running
// after a halted upgrade resumes). After each upgrade the node's health is
// gated: if it does not come back healthy at targetVersion, the upgrade halts
// immediately, leaving the cluster mixed-version-but-running rather than
// marching past a failed control plane and risking quorum. The returned error
// names the offending node; Result reports progress up to the halt.
func rollingUpgrade(ctx context.Context, nodes []upgradeNode, targetVersion string, u nodeUpgrader) (UpgradeResult, error) {
	res := UpgradeResult{Target: targetVersion}
	if targetVersion == "" {
		return res, fmt.Errorf("rolling upgrade: target version is required")
	}
	if u == nil {
		return res, fmt.Errorf("rolling upgrade: nil upgrader")
	}

	for _, node := range upgradeOrder(nodes) {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		// Idempotent skip: a node already at the target needs no work. This is
		// what makes a re-run after a halt resume from where it stopped.
		if node.Version == targetVersion {
			res.Skipped = append(res.Skipped, node.Name)
			continue
		}

		if err := u.Upgrade(ctx, node, targetVersion); err != nil {
			return res, fmt.Errorf("upgrade %s %q: %w", node.Role, node.Name, err)
		}

		got, err := u.Health(ctx, node)
		if err != nil {
			return res, fmt.Errorf("node %q did not come back healthy after upgrade: %w", node.Name, err)
		}
		if got != targetVersion {
			return res, fmt.Errorf("node %q reports version %q after upgrade, want %q — halting to avoid marching past a bad node", node.Name, got, targetVersion)
		}
		res.Upgraded = append(res.Upgraded, node.Name)
	}
	return res, nil
}
