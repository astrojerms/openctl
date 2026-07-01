package k3s

import (
	"context"
	"fmt"
	"time"
)

// vmIPWaitTimeout caps how long K3sNode/AgentInstall waits for their
// vmRef target's status.ip to appear before giving up. Matches the SSH
// wait budget — a VM that hasn't reported its IP by then also won't
// SSH-connect, so failing earlier just skips retries that would fail
// anyway.
const vmIPWaitTimeout = 5 * time.Minute

// vmIPPollInterval is how often waitForVMIP re-queries. Small enough
// to feel responsive when QGA reports late; large enough not to flood
// the Proxmox API.
const vmIPPollInterval = 2 * time.Second

// waitForVMIP polls the referenced VM's status.ip until it becomes
// non-empty or the timeout fires. Returns the observed IP. Used by
// K3sNode + AgentInstall when they're dispatched immediately after a
// VM create (Cluster.Plan output) — QGA populates the IP
// asynchronously, so the naive "read status.ip once from the
// dispatcher-resolved manifest" would race.
//
// A nil VMApplier (test scaffolding) short-circuits: returns "" nil
// so callers with unit-test manifests carrying a pre-populated IP
// keep working.
func waitForVMIP(ctx context.Context, vms VMApplier, vmName string, timeout time.Duration) (string, error) {
	if vms == nil {
		return "", nil
	}
	if timeout <= 0 {
		timeout = vmIPWaitTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		r, err := vms.Get(ctx, "VirtualMachine", vmName)
		if err == nil && r != nil {
			if status, ok := r.Status["ip"].(string); ok && status != "" {
				return status, nil
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out after %s waiting for VM %q to report status.ip (QGA installed in template?)", timeout, vmName)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(vmIPPollInterval):
		}
	}
}
