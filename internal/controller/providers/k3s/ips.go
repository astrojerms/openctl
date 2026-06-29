package k3s

import (
	"context"
	"fmt"
	"time"

	"github.com/openctl/openctl/internal/controller/operations"
	k3sresources "github.com/openctl/openctl/pkg/k3s/resources"
)

// defaultIPPollTimeout caps QGA-based discovery for a single node. Tuned to
// match the SSH WaitForSSH budget in pkg/k3s/cluster (5 minutes per node),
// since a node with no QGA-reported IP will also fail to SSH.
const defaultIPPollTimeout = 5 * time.Minute

// defaultIPPollInterval is how often resolveNodeIPs re-queries the VM
// provider when waiting for an IP. Small enough to feel responsive when
// QGA reports late; large enough not to flood the Proxmox API.
const defaultIPPollInterval = 2 * time.Second

// resolveNodeIPs returns node-name → IP for every node in nodeNames. When
// spec.network.staticIPs is set the result comes from the deterministic
// AllocateIPs map — no IO. When it isn't, the controller polls the VM
// provider's Get response for status.ip on each node until populated
// (qemu-guest-agent reports it) or the per-node timeout fires.
//
// The poll is surfaced as a single "discover-ips" child op so the user
// can see the wait in op detail; we don't want one row per node here
// because the wait is bulk (all nodes boot in parallel).
func (p *Provider) resolveNodeIPs(
	ctx context.Context,
	rec operations.ChildRecorder,
	clusterName string,
	spec *k3sresources.ClusterSpec,
	nodeNames []string,
) (map[string]string, error) {
	if spec.Network.StaticIPs != nil && spec.Network.StaticIPs.StartIP != "" {
		ips, err := k3sresources.AllocateIPs(clusterName, spec)
		if err != nil {
			return nil, fmt.Errorf("allocate static IPs: %w", err)
		}
		out := make(map[string]string, len(nodeNames))
		for _, n := range nodeNames {
			ip := ips[n]
			if ip == "" {
				return nil, fmt.Errorf("no static IP allocated for node %s", n)
			}
			out[n] = ip
		}
		return out, nil
	}

	result, err := runChildStep(ctx, rec, clusterName, "discover-ips",
		fmt.Sprintf("Discover IPs for %d node(s) via QEMU guest agent", len(nodeNames)),
		func() (any, error) {
			return pollVMIPs(ctx, p.vms, nodeNames, defaultIPPollTimeout, defaultIPPollInterval)
		})
	if err != nil {
		return nil, fmt.Errorf("discover IPs via QGA: %w (is qemu-guest-agent installed in the VM template? — set spec.network.staticIPs to bypass)", err)
	}
	ips, _ := result.(map[string]string)
	return ips, nil
}

// pollVMIPs polls vms.Get for each node sequentially and waits until
// every node has an IP in status. Returns name→IP. A single node missing
// its IP at deadline causes the whole call to fail — the caller is the
// k3s install, which needs all IPs before it can run.
func pollVMIPs(ctx context.Context, vms VMApplier, nodeNames []string, timeout, interval time.Duration) (map[string]string, error) {
	out := make(map[string]string, len(nodeNames))
	deadline := time.Now().Add(timeout)
	pending := append([]string(nil), nodeNames...)

	for len(pending) > 0 {
		next := pending[:0]
		for _, n := range pending {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			r, err := vms.Get(ctx, "VirtualMachine", n)
			if err != nil {
				// Get can fail transiently while the VM boots; keep polling.
				next = append(next, n)
				continue
			}
			ip := extractIP(r.Status)
			if ip == "" {
				next = append(next, n)
				continue
			}
			out[n] = ip
		}
		pending = next
		if len(pending) == 0 {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for IPs after %s; missing: %v", timeout, pending)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
	return out, nil
}

// extractIP pulls a non-empty IP string out of a VM's status map. The
// proxmox VMToResourceWithIP code path sets status["ip"] when the guest
// agent reports one; missing/empty means "not yet."
func extractIP(status map[string]any) string {
	if status == nil {
		return ""
	}
	ip, _ := status["ip"].(string)
	return ip
}
