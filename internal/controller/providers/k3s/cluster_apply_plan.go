package k3s

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/pkg/k3s/agent/bootstrap"
	"github.com/openctl/openctl/pkg/k3s/agent/certs"
	"github.com/openctl/openctl/pkg/protocol"
)

// Synthetic (non-resource) barrier task IDs in the apply graph. Prefixed
// with '@' so they never collide with a child's "Kind/Name" ChildKey.
const (
	taskStateStub = "@state-stub"
	taskCABundle  = "@ca-bundle"
)

// applyClusterViaPlan is the Plan-driven Apply path for a first-time Cluster
// create. It builds a dependency DAG over the Plan()-emitted children and
// applies them in dependency order via operations.RunGraph, instead of
// hand-ordered phase loops.
//
// Edges come from two sources:
//   - The children's own $refs (operations.RefChildEdges): each K3sNode refs
//     its VM (vmRef) and — for joiners — the first control plane (joinFrom);
//     each AgentInstall refs its VM. So a node's VM applies before it, and
//     the first CP applies before the joiners that resolve its nodeToken.
//   - Two barriers that are NOT $refs and so are added explicitly: an interim
//     state stub that must follow every VM (crash recovery walks the stub's
//     children[] to clean up VMs if a later step fails), and the cluster CA
//     bundle, an aggregation over ALL K3sNode states that every AgentInstall
//     consumes.
//
// Execution is serial by default (OPENCTL_APPLY_CONCURRENCY overrides), which
// preserves SSH-install semantics; the graph is fully parallel-capable once
// concurrent provisioning is validated. After the graph completes, the legacy
// cluster state YAML is written so applyExisting (count-up/respec/delete)
// keeps working unchanged. Callers without a ChildDispatcher (unit tests, CLI
// direct-apply) fall back to the imperative path at Cluster.Apply.
func (p *Provider) applyClusterViaPlan(ctx context.Context, manifest *protocol.Resource, cd operations.ChildDispatcher) (*protocol.Resource, error) {
	name := manifest.Metadata.Name
	plan, err := p.Plan(ctx, manifest)
	if err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}

	var vms, k3sNodes, agents []*protocol.Resource
	for _, c := range plan.Children {
		switch c.Kind {
		case "VirtualMachine":
			vms = append(vms, c)
		case kindK3sNode:
			k3sNodes = append(k3sNodes, c)
		case kindAgentInstall:
			agents = append(agents, c)
		}
	}
	if len(vms) == 0 {
		return nil, fmt.Errorf("plan produced no VirtualMachine children")
	}
	if len(k3sNodes) == 0 {
		return nil, fmt.Errorf("plan produced no K3sNode children")
	}

	refEdges := operations.RefChildEdges(plan.Children)
	tasks := make([]operations.Task, 0, len(plan.Children)+2)

	// applyChildTask wraps one child's ApplyChild as a graph task, seeding
	// its dependencies with the child's $ref edges plus any extra barriers.
	applyChildTask := func(child *protocol.Resource, extraDeps ...string) operations.Task {
		key := operations.ChildKey(child)
		deps := append(append([]string{}, extraDeps...), refEdges[key]...)
		return operations.Task{
			ID:        key,
			DependsOn: deps,
			Run: func(ctx context.Context) error {
				if _, err := cd.ApplyChild(ctx, child); err != nil {
					return fmt.Errorf("apply %s %s: %w", child.Kind, child.Metadata.Name, err)
				}
				return nil
			},
		}
	}

	vmKeys := make([]string, 0, len(vms))
	for _, vm := range vms {
		vmKeys = append(vmKeys, operations.ChildKey(vm))
		tasks = append(tasks, applyChildTask(vm))
	}

	// Interim stub: after every VM, before any k3s install.
	tasks = append(tasks, operations.Task{
		ID:        taskStateStub,
		DependsOn: vmKeys,
		Run: func(context.Context) error {
			if err := p.saveClusterStateStub(name, manifest, vms, "Provisioning", "VMs created; installing k3s"); err != nil {
				return fmt.Errorf("save interim cluster state: %w", err)
			}
			return nil
		},
	})

	k3sKeys := make([]string, 0, len(k3sNodes))
	for _, k := range k3sNodes {
		k3sKeys = append(k3sKeys, operations.ChildKey(k))
		tasks = append(tasks, applyChildTask(k, taskStateStub))
	}

	// CA bundle: aggregates every K3sNode's observed state; gates all agents.
	tasks = append(tasks, operations.Task{
		ID:        taskCABundle,
		DependsOn: k3sKeys,
		Run: func(context.Context) error {
			if err := p.materializeClusterCABundle(name, k3sNodes); err != nil {
				return fmt.Errorf("materialize CA bundle: %w", err)
			}
			return nil
		},
	})

	for _, a := range agents {
		tasks = append(tasks, applyChildTask(a, taskCABundle))
	}

	if err := operations.RunGraph(ctx, applyConcurrency(), tasks); err != nil {
		return nil, err
	}

	// Finally save cluster state in the legacy YAML shape so applyExisting
	// can operate on it later.
	return p.saveClusterStateFromChildren(name, manifest, vms, k3sNodes)
}

// applyConcurrency is the max number of plan children applied in parallel.
// Defaults to 1 (serial) — the safe, validated default that preserves the
// original phase-loop semantics. OPENCTL_APPLY_CONCURRENCY=N opts into
// parallel provisioning of independent nodes once a deployment has validated
// it, matching the repo's env-gated rollout convention.
func applyConcurrency() int {
	if v := os.Getenv("OPENCTL_APPLY_CONCURRENCY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 1
}

// materializeClusterCABundle mints a fresh per-cluster CA and per-
// node server certs, then persists the bundle to
// ~/.openctl/state/k3s/<cluster>/ so applyAgentInstall can load it.
// Reads node IPs from the K3sNode state files that phase-2 populated.
//
// Idempotency: if a bundle already exists on disk, extend it with
// server certs for any new nodes rather than regenerating (which
// would break already-installed agents that trust the old CA).
func (p *Provider) materializeClusterCABundle(clusterName string, k3sNodeManifests []*protocol.Resource) error {
	bundleDir, err := clusterBundleDir(clusterName)
	if err != nil {
		return err
	}

	// Pull IPs from the state files each applyK3sNode wrote.
	ids := make([]certs.NodeIdentity, 0, len(k3sNodeManifests))
	for _, m := range k3sNodeManifests {
		state, err := loadNodeState(m.Metadata.Name)
		if err != nil {
			return fmt.Errorf("load K3sNode state for %s: %w", m.Metadata.Name, err)
		}
		if state == nil || state.VMIP == "" {
			return fmt.Errorf("K3sNode %s has no observed IP in state", m.Metadata.Name)
		}
		ids = append(ids, certs.NodeIdentity{Name: state.VMName, IP: state.VMIP})
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].Name < ids[j].Name })

	if _, err := certs.LoadBundle(bundleDir); err == nil {
		// Existing CA — mint additional per-node certs into it and
		// re-persist. Preserves trust for already-installed agents.
		bundle, err := certs.LoadBundle(bundleDir)
		if err != nil {
			return fmt.Errorf("reload bundle: %w", err)
		}
		var missing []certs.NodeIdentity
		for _, id := range ids {
			if _, has := bundle.ServerCerts[id.Name]; !has {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			if err := bundle.MintServerCerts(missing); err != nil {
				return fmt.Errorf("mint missing certs: %w", err)
			}
			if err := bundle.WriteTo(bundleDir); err != nil {
				return fmt.Errorf("persist extended bundle: %w", err)
			}
		}
		return nil
	}

	// Fresh bundle.
	bundle, err := certs.GenerateBundle(clusterName, ids)
	if err != nil {
		return fmt.Errorf("generate bundle: %w", err)
	}
	if err := bundle.WriteTo(bundleDir); err != nil {
		return fmt.Errorf("persist bundle: %w", err)
	}
	return nil
}

// saveClusterStateStub writes a minimal cluster state YAML with just
// the VM children populated. Called after Phase 1 of the plan path so
// Cluster.Delete can find + clean up the child VMs even if Phase 2/3
// fail. The final saveClusterStateFromChildren call overwrites this
// with a Ready-state document (including agent bundle + kubeconfig
// paths) once every phase succeeds.
//
// phase and message set status.phase / status.message so operators
// see progress in ResourceService.Get. Idempotent: overwrites any
// existing file at the state path.
func (p *Provider) saveClusterStateStub(name string, manifest *protocol.Resource, vms []*protocol.Resource, phase, message string) error {
	dir, err := stateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	children := make([]childRef, 0, len(vms))
	for _, vm := range vms {
		children = append(children, childRef{
			Provider: "proxmox",
			Kind:     "VirtualMachine",
			Name:     vm.Metadata.Name,
		})
	}
	now := time.Now().UTC().Format(time.RFC3339)
	doc := map[string]any{
		"apiVersion": "k3s.openctl.io/v1",
		"kind":       "Cluster",
		"metadata": map[string]any{
			"name":      name,
			"provider":  "k3s",
			"createdAt": now,
			"updatedAt": now,
		},
		"spec": manifest.Spec,
		"status": map[string]any{
			"phase":   phase,
			"message": message,
		},
		"children": children,
	}
	data, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name+".yaml"), data, 0o600)
}

// saveClusterStateFromChildren writes the legacy cluster state YAML
// from the observed child manifests + their per-resource state files.
// Same shape as saveState() so applyExisting can operate on it later.
//
// kubeconfigPath, serverIP, token come from the first CP's K3sNode
// state (populated by applyK3sNode on first-server init). Agent
// endpoints come from each K3sNode state's vmIP.
func (p *Provider) saveClusterStateFromChildren(name string, manifest *protocol.Resource, vms, k3sNodes []*protocol.Resource) (*protocol.Resource, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	firstCPState, err := loadNodeState(k3sNodes[0].Metadata.Name)
	if err != nil {
		return nil, fmt.Errorf("load first-CP state: %w", err)
	}
	if firstCPState == nil {
		return nil, fmt.Errorf("no state for first CP %s", k3sNodes[0].Metadata.Name)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	kubeconfigPath := filepath.Join(home, ".openctl", "k3s", k3sNodes[0].Metadata.Name, "kubeconfig")
	bundleDir, err := clusterBundleDir(name)
	if err != nil {
		return nil, err
	}

	agentEndpoints := map[string]string{}
	children := make([]childRef, 0, len(vms))
	for _, vm := range vms {
		children = append(children, childRef{
			Provider: "proxmox",
			Kind:     "VirtualMachine",
			Name:     vm.Metadata.Name,
		})
	}
	for _, k := range k3sNodes {
		state, err := loadNodeState(k.Metadata.Name)
		if err != nil || state == nil {
			continue
		}
		agentEndpoints[state.VMName] = state.VMIP
	}

	outputs := map[string]any{
		"kubeconfigPath": kubeconfigPath,
		"serverIP":       firstCPState.VMIP,
		"agent": map[string]any{
			"bundleDir":      bundleDir,
			"caPath":         filepath.Join(bundleDir, "ca.pem"),
			"clientCertPath": filepath.Join(bundleDir, "client.pem"),
			"clientKeyPath":  filepath.Join(bundleDir, "client.key"),
			"port":           bootstrap.Port,
			"endpoints":      agentEndpoints,
		},
	}

	now := time.Now().UTC().Format(time.RFC3339)
	doc := map[string]any{
		"apiVersion": "k3s.openctl.io/v1",
		"kind":       "Cluster",
		"metadata": map[string]any{
			"name":      name,
			"provider":  "k3s",
			"createdAt": now,
			"updatedAt": now,
		},
		"spec": manifest.Spec,
		"status": map[string]any{
			"phase":   "Ready",
			"message": "Cluster is ready",
			"outputs": outputs,
		},
		"children": children,
	}
	data, err := yaml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), data, 0o600); err != nil {
		return nil, err
	}
	return &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       kindCluster,
		Metadata:   protocol.ResourceMetadata{Name: name},
		Spec:       manifest.Spec,
		Status: map[string]any{
			"phase":   "Ready",
			"outputs": outputs,
		},
	}, nil
}
