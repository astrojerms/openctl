package k3s

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/openctl/openctl/internal/controller/providers"
	k3sresources "github.com/openctl/openctl/pkg/k3s/resources"
	"github.com/openctl/openctl/pkg/protocol"
)

// DryRun previews what an Apply would do without performing it.
// Implements providers.DryRunner — the resource handler calls this from
// the DryRunApply RPC to drive the UI's edit-preview pane.
//
// Mirrors the structure of Apply: parse spec, load state (if any),
// compute change plan + respecs, then translate into provider-agnostic
// ChildAction entries plus the gates the user would need to flip on the
// Apply request.
func (p *Provider) DryRun(ctx context.Context, manifest *protocol.Resource) (*providers.DryRunResult, error) {
	if err := requireKindCluster(manifest.Kind); err != nil {
		return nil, err
	}
	name := manifest.Metadata.Name
	if name == "" {
		return nil, fmt.Errorf("metadata.name is required")
	}
	spec, err := k3sresources.ParseClusterSpec(manifest)
	if err != nil {
		return nil, fmt.Errorf("parse cluster spec: %w", err)
	}

	existing, _ := p.loadState(name)
	if existing == nil {
		return dryRunFreshCluster(name, spec), nil
	}
	return p.dryRunExisting(ctx, name, spec)
}

// dryRunFreshCluster builds the plan for a Cluster that doesn't exist
// yet — every node is a "create". No destructive gates needed.
func dryRunFreshCluster(name string, spec *k3sresources.ClusterSpec) *providers.DryRunResult {
	cps, workers := k3sresources.NodeNames(name, spec)
	children := make([]providers.ChildAction, 0, len(cps)+len(workers))
	for _, n := range cps {
		children = append(children, providers.ChildAction{
			Verb: "create", Kind: "VirtualMachine", Name: n,
			Detail: "new control-plane node",
		})
	}
	for _, n := range workers {
		children = append(children, providers.ChildAction{
			Verb: "create", Kind: "VirtualMachine", Name: n,
			Detail: "new worker node",
		})
	}
	return &providers.DryRunResult{
		Children: children,
		Summary: fmt.Sprintf("would create cluster %q with %d CP + %d worker node(s)",
			name, len(cps), len(workers)),
	}
}

// dryRunExisting builds the plan for a Cluster that already has state.
// Reuses the same computeChangePlan + computeSpecRespecs +
// catastrophicReason chain that Apply enforces.
func (p *Provider) dryRunExisting(ctx context.Context, name string, spec *k3sresources.ClusterSpec) (*providers.DryRunResult, error) {
	current, _ := readChildren(name)
	plan := computeChangePlan(name, spec, current)
	removed := toSet(append(plan.removeCPs, plan.removeWorkers...))
	respecs := p.computeSpecRespecs(ctx, name, spec, current, removed)

	if !plan.hasChanges() && len(respecs) == 0 {
		return &providers.DryRunResult{Summary: "no-op (cluster matches manifest)"}, nil
	}

	children := make([]providers.ChildAction, 0,
		len(plan.addCPs)+len(plan.addWorkers)+len(plan.removeCPs)+len(plan.removeWorkers)+len(respecs))
	for _, n := range plan.addCPs {
		children = append(children, providers.ChildAction{
			Verb: "create", Kind: "VirtualMachine", Name: n, Detail: "new control-plane node",
		})
	}
	for _, n := range plan.addWorkers {
		children = append(children, providers.ChildAction{
			Verb: "create", Kind: "VirtualMachine", Name: n, Detail: "new worker node",
		})
	}
	for _, n := range plan.removeCPs {
		children = append(children, providers.ChildAction{
			Verb: "destroy", Kind: "VirtualMachine", Name: n, Detail: "remove control-plane node",
		})
	}
	for _, n := range plan.removeWorkers {
		children = append(children, providers.ChildAction{
			Verb: "destroy", Kind: "VirtualMachine", Name: n, Detail: "remove worker node",
		})
	}
	for _, r := range respecs {
		role := "worker"
		if r.IsCP {
			role = "control-plane"
		}
		children = append(children, providers.ChildAction{
			Verb: "respec", Kind: "VirtualMachine", Name: r.Name,
			Detail: fmt.Sprintf(
				"destroy + recreate %s (cpu %d→%d, mem %dMi→%dMi)",
				role, r.ObservedCPUs, r.DesiredCPUs, r.ObservedMemMB, r.DesiredMemMB),
		})
	}

	var gates []string
	if plan.removesAny() || len(respecs) > 0 {
		gates = append(gates, providers.GateAllowDestructive)
	}

	haveCPs, haveWorkers := countCurrent(current, name)
	if reason := catastrophicReason(plan, haveCPs, haveWorkers); reason != "" {
		gates = appendUnique(gates, providers.GateIKnowThisBreaks)
	}
	if reason := catastrophicRespecReason(respecs, haveCPs, haveWorkers); reason != "" {
		gates = appendUnique(gates, providers.GateIKnowThisBreaks)
	}

	return &providers.DryRunResult{
		Children:      children,
		RequiredGates: gates,
		Summary:       summarisePlan(plan, len(respecs)),
	}, nil
}

func countCurrent(current []childRef, clusterName string) (cps, workers int) {
	cpPrefix := clusterName + "-cp-"
	for _, c := range current {
		if c.Kind != "VirtualMachine" {
			continue
		}
		if strings.HasPrefix(c.Name, cpPrefix) {
			cps++
		} else {
			workers++
		}
	}
	return cps, workers
}

func appendUnique(s []string, v string) []string {
	if slices.Contains(s, v) {
		return s
	}
	return append(s, v)
}

func summarisePlan(plan *changePlan, respecs int) string {
	var parts []string
	if n := len(plan.addCPs) + len(plan.addWorkers); n > 0 {
		parts = append(parts, fmt.Sprintf("add %d", n))
	}
	if n := len(plan.removeCPs) + len(plan.removeWorkers); n > 0 {
		parts = append(parts, fmt.Sprintf("remove %d", n))
	}
	if respecs > 0 {
		parts = append(parts, fmt.Sprintf("respec %d", respecs))
	}
	if len(parts) == 0 {
		return "no-op"
	}
	return "would " + strings.Join(parts, ", ") + " node(s)"
}
