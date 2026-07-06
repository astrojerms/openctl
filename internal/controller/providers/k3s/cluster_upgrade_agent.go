package k3s

import (
	"context"
	"fmt"
	"time"

	agentclient "github.com/openctl/openctl/pkg/k3s/agent/client"
)

// agentNodeUpgrader is the production nodeUpgrader (consumed by rollingUpgrade):
// it drives each node's openctl-k3s-agent over mTLS to swap the k3s binary and
// then confirms the node came back healthy at the target version. The agent
// client (already tested against a real agent server) does the transport; this
// adds only per-node client construction from the cluster's cert bundle and the
// post-upgrade health poll (k3s restarts during an upgrade, so Info briefly
// fails before recovering).
type agentNodeUpgrader struct {
	bundle    agentCertBundle
	endpoints map[string]string // node name -> host:port

	dialTimeout   time.Duration // per-request agent client timeout
	healthTimeout time.Duration // total time to wait for a node to recover
	healthPoll    time.Duration // interval between health polls
}

// agentCertBundle is the subset of the cluster's mTLS material an agent client
// needs (CA to verify the node, client cert/key to authenticate to it).
type agentCertBundle struct {
	CACertPEM     []byte
	ClientCertPEM []byte
	ClientKeyPEM  []byte
}

func newAgentNodeUpgrader(bundle agentCertBundle, endpoints map[string]string) *agentNodeUpgrader {
	return &agentNodeUpgrader{
		bundle:        bundle,
		endpoints:     endpoints,
		dialTimeout:   10 * time.Second,
		healthTimeout: 5 * time.Minute,
		healthPoll:    5 * time.Second,
	}
}

func (u *agentNodeUpgrader) clientFor(node upgradeNode) (*agentclient.Client, error) {
	ep, ok := u.endpoints[node.Name]
	if !ok || ep == "" {
		return nil, fmt.Errorf("no agent endpoint known for node %q", node.Name)
	}
	return agentclient.New(agentclient.Options{
		Endpoint:      ep,
		CACertPEM:     u.bundle.CACertPEM,
		ClientCertPEM: u.bundle.ClientCertPEM,
		ClientKeyPEM:  u.bundle.ClientKeyPEM,
		Timeout:       u.dialTimeout,
	})
}

// Upgrade swaps the node's k3s binary to version via its agent.
func (u *agentNodeUpgrader) Upgrade(ctx context.Context, node upgradeNode, version string) error {
	c, err := u.clientFor(node)
	if err != nil {
		return err
	}
	return c.UpgradeK3s(ctx, version)
}

// Health polls the node's agent until it responds, returning the running k3s
// version. k3s restarts during the upgrade, so Info fails transiently before
// recovering; poll until healthTimeout. Returns the last error if the node
// never comes back.
func (u *agentNodeUpgrader) Health(ctx context.Context, node upgradeNode) (string, error) {
	c, err := u.clientFor(node)
	if err != nil {
		return "", err
	}
	deadline := u.healthTimeout
	if deadline <= 0 {
		deadline = time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	var lastErr error
	for {
		info, err := c.Info(ctx)
		if err == nil && info != nil {
			return info.K3sVersion, nil
		}
		if err != nil {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return "", fmt.Errorf("node %q did not recover in %s: %w", node.Name, deadline, lastErr)
			}
			return "", fmt.Errorf("node %q did not recover in %s", node.Name, deadline)
		case <-time.After(u.healthPoll):
		}
	}
}
