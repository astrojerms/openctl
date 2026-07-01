package k3s

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/pkg/k3s/agent/bootstrap"
	"github.com/openctl/openctl/pkg/k3s/agent/certs"
	"github.com/openctl/openctl/pkg/protocol"
)

// applyClusterViaPlan is the Plan-driven Apply path for a first-time
// Cluster create. Dispatches Plan()-emitted children through the
// dispatcher's inline pipeline in three phases:
//
//  1. VMs — dispatched via the ChildDispatcher so the proxmox provider
//     creates each one and QGA populates status.ip asynchronously.
//  2. K3sNodes — each polls its vmRef for status.ip (waitForVMIP),
//     SSH-installs k3s, saves state file. The first CP's state carries
//     status.nodeToken which subsequent K3sNodes resolve via $ref.
//  3. AgentInstalls — after all K3sNodes finish, generate the cluster
//     CA bundle from the observed node IPs, then dispatch each agent
//     install. The bundle path matches what applyAgentInstall reads.
//
// After all children succeed, writes the legacy cluster state YAML so
// applyExisting (count-up/respec/delete) keeps working unchanged.
// Callers who invoke this without a ChildDispatcher (unit tests, CLI
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

	// Phase 1: VMs. Each ApplyChild call fans through the standard
	// dispatcher pipeline (resolve/cache/save). Ordering is stable —
	// Plan emits VMs in NodeNames order.
	for _, vm := range vms {
		if _, err := cd.ApplyChild(ctx, vm); err != nil {
			return nil, fmt.Errorf("apply VM %s: %w", vm.Metadata.Name, err)
		}
	}

	// Save an interim cluster state stub as soon as all VMs exist.
	// Without this, a Phase 2/3 failure leaves live VMs on the
	// hypervisor but Cluster.Delete can't find them because it
	// walks the cluster state file's children[]. The stub carries
	// the VM refs and a "Provisioning" phase so the operator sees
	// the partial success and Delete can clean up. The final
	// saveClusterStateFromChildren call overwrites this with the
	// full Ready state once everything succeeds.
	if err := p.saveClusterStateStub(name, manifest, vms, "Provisioning", "VMs created; installing k3s"); err != nil {
		return nil, fmt.Errorf("save interim cluster state: %w", err)
	}

	// Phase 2: K3sNodes. First CP is initialized (no joinFrom in
	// spec — Plan deleted it); subsequent K3sNodes have joinFrom
	// $refs that resolve at dispatch time against the first CP's
	// just-saved status.nodeToken. Serial execution matches SSH-
	// install semantics.
	for _, k := range k3sNodes {
		if _, err := cd.ApplyChild(ctx, k); err != nil {
			return nil, fmt.Errorf("apply K3sNode %s: %w", k.Metadata.Name, err)
		}
	}

	// Phase 3a: Materialize the cluster CA bundle to the on-disk
	// path AgentInstall reads from. Uses the K3sNode states that
	// were just written by phase 2 (each has vmName + vmIP).
	if err := p.materializeClusterCABundle(name, k3sNodes); err != nil {
		return nil, fmt.Errorf("materialize CA bundle: %w", err)
	}

	// Phase 3b: AgentInstalls. Each SSH-installs the openctl-k3s-
	// agent using its per-node server cert minted from the bundle.
	for _, a := range agents {
		if _, err := cd.ApplyChild(ctx, a); err != nil {
			return nil, fmt.Errorf("apply AgentInstall %s: %w", a.Metadata.Name, err)
		}
	}

	// Finally save cluster state in the legacy YAML shape so
	// applyExisting can operate on it later.
	return p.saveClusterStateFromChildren(name, manifest, vms, k3sNodes)
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
