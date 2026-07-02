// Package k3s is the in-process Provider implementation for k3s Cluster
// resources. The same low-level code that powers the legacy exec'd
// `openctl-k3s` plugin (pkg/k3s/{cluster,agent,resources,ssh}) does the
// heavy lifting; this provider coordinates the steps in-process and uses
// a sibling VirtualMachine provider (typically pkg/proxmox) to create the
// child VMs instead of returning dispatch requests to a foreign caller.
//
// Phase 4 scope: synchronous Cluster apply (the whole apply runs as one
// operation in the controller's dispatcher). Per-step child operations
// surfaced as separate rows in the operations table is a Phase 4.5
// follow-up, tracked in CONTROLLER.md.
package k3s

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/internal/controller/providers"
	k3scluster "github.com/openctl/openctl/pkg/k3s/cluster"
	k3sresources "github.com/openctl/openctl/pkg/k3s/resources"
	"github.com/openctl/openctl/pkg/protocol"
)

const (
	providerName = "k3s"
	kindCluster  = "Cluster"
)

// VMApplier is the subset of providers.Provider that the k3s Provider needs
// to drive child VM creation. The proxmox provider satisfies this naturally;
// using a narrow interface keeps the dependency explicit and tests trivial.
type VMApplier interface {
	Apply(ctx context.Context, manifest *protocol.Resource) (*protocol.Resource, error)
	Get(ctx context.Context, kind, name string) (*protocol.Resource, error)
	Delete(ctx context.Context, kind, name string) error
}

// Provider implements providers.Provider for k3s Cluster.
type Provider struct {
	config *protocol.ProviderConfig
	vms    VMApplier
}

// New constructs a Provider. cfg is forwarded to the underlying cluster
// machinery (mostly for storage/network defaults from openctl config); vms
// is the provider used for child VM operations.
func New(cfg *protocol.ProviderConfig, vms VMApplier) *Provider {
	return &Provider{config: cfg, vms: vms}
}

func (p *Provider) Name() string    { return providerName }
func (p *Provider) Kinds() []string { return []string{kindCluster, kindK3sNode, kindAgentInstall} }

// Actions implements providers.Actioner. Cluster supports one
// runtime action today: get-kubeconfig, which returns the stored
// kubeconfig contents from ~/.openctl/k3s/<name>/kubeconfig.
func (p *Provider) Actions(kind string) []string {
	if kind != kindCluster {
		return nil
	}
	return []string{"get-kubeconfig"}
}

// DoAction implements providers.Actioner. Reads the stored kubeconfig
// (populated at cluster-create time by pkg/k3s/cluster.Creator) and
// returns it as a downloadable file. Fails with a clear error when
// the file is missing — a Cluster that was never successfully created
// (or was manually deleted from disk) has no kubeconfig on file.
func (p *Provider) DoAction(_ context.Context, kind, name, action string) (*providers.ActionResult, error) {
	if kind != kindCluster {
		return nil, fmt.Errorf("no actions for kind %q", kind)
	}
	if action != "get-kubeconfig" {
		return nil, fmt.Errorf("unknown action %q", action)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home: %w", err)
	}
	path := filepath.Join(home, ".openctl", "k3s", name, "kubeconfig")
	content, err := os.ReadFile(path) // #nosec G304 -- controller-owned path derived from cluster name
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("kubeconfig not found at %s — cluster may not have been created via openctl or was never successfully applied", path)
		}
		return nil, fmt.Errorf("read kubeconfig: %w", err)
	}
	return &providers.ActionResult{
		DownloadContent:  string(content),
		DownloadFilename: fmt.Sprintf("%s-kubeconfig.yaml", name),
		Message:          fmt.Sprintf("Kubeconfig read from %s", path),
	}, nil
}

// OwnerOf implements providers.OwnershipChecker: returns true if any cluster
// state file lists (kind, name) as a child. Used by the resource handler to
// block Delete on owned resources (e.g. attempting to delete a proxmox VM
// that's a member of a cluster).
func (p *Provider) OwnerOf(kind, name string) (string, string, bool) {
	dir, err := stateDir()
	if err != nil {
		return "", "", false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", false
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		clusterName := strings.TrimSuffix(e.Name(), ".yaml")
		children, err := readChildren(clusterName)
		if err != nil {
			continue
		}
		for _, c := range children {
			if c.Kind == kind && c.Name == name {
				return kindCluster, clusterName, true
			}
		}
	}
	return "", "", false
}

// ChildrenOf implements providers.ChildrenLister: returns the VirtualMachine
// children of the named Cluster from its state file. Empty when kind isn't
// Cluster, the cluster doesn't exist, or the state file is unreadable.
// Each ref carries the child's owning provider's apiVersion (e.g. proxmox
// VMs become `proxmox.openctl.io/v1`) so callers can navigate without
// inferring conventions.
func (p *Provider) ChildrenOf(kind, name string) []providers.ResourceRef {
	if kind != kindCluster {
		return nil
	}
	children, err := readChildren(name)
	if err != nil || len(children) == 0 {
		return nil
	}
	out := make([]providers.ResourceRef, 0, len(children))
	for _, c := range children {
		// childRef.Provider is the short name ("proxmox"); construct the
		// apiVersion via the same convention the Registry uses.
		out = append(out, providers.ResourceRef{
			APIVersion: c.Provider + ".openctl.io/v1",
			Kind:       c.Kind,
			Name:       c.Name,
		})
	}
	return out
}

// Annotation keys for the destructive-flag plumbing. The CLI sets these on
// the manifest's metadata.annotations before submitting; the apply path
// reads them to decide whether to honor structural changes.
const (
	annotAllowDestructive = "openctl.io/allow-destructive"
	annotIKnowThisBreaks  = "openctl.io/i-know-this-breaks-the-cluster"
)

// convergeViaPlanEnabled reports whether existing-cluster convergence should
// run through the Plan/dispatcher path (scale-down via DeleteChild, count-up
// via Plan children) rather than the legacy imperative executors
// (runChildVMDelete, applyCountUp/Joiner).
//
// Opt-in via OPENCTL_CONVERGE_VIA_PLAN=1 while the path is being
// homelab-validated; off by default means an existing-cluster apply behaves
// exactly as it did before the migration. A later change flips the default
// once the path is proven, and the legacy executors are then deleted.
func convergeViaPlanEnabled() bool {
	switch os.Getenv("OPENCTL_CONVERGE_VIA_PLAN") {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// Apply creates a fresh cluster, or — if the cluster already exists —
// converges its child set toward the new manifest. Phase 5 supports
// removals (with --allow-destructive) and detects catastrophic ops (with
// --i-know-this-breaks-the-cluster). Phase 5.x adds count-up support
// (adding nodes to a live cluster via the Joiner).
//
// IP allocation: if spec.network.staticIPs is set the IPs are deterministic
// per node name (existing nodes keep their IPs across re-applies). If
// staticIPs is omitted, the VMs come up via DHCP and the controller polls
// the QEMU guest agent (status.ip on the VM provider's Get response) until
// each node reports its IP. QGA polling requires qemu-guest-agent installed
// in the VM template; without it the poll times out with a clear message.
func (p *Provider) Apply(ctx context.Context, manifest *protocol.Resource) (*protocol.Resource, error) {
	if manifest.Kind == kindK3sNode {
		return p.applyK3sNode(ctx, manifest)
	}
	if manifest.Kind == kindAgentInstall {
		return p.applyAgentInstall(ctx, manifest)
	}
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

	// If the cluster already exists, plan the structural diff against the
	// new manifest and either no-op, converge, or refuse depending on the
	// destructive flags.
	if existing, _ := p.loadState(name); existing != nil {
		if isProvisioningCluster(existing) {
			if cd, ok := operations.ChildDispatcherFrom(ctx); ok {
				return p.applyClusterViaPlan(ctx, manifest, cd)
			}
		}
		return p.applyExisting(ctx, manifest, name, spec)
	}

	// Plan-based path (Phase 8 dispatcher refactor): when running
	// inside a dispatched op, fan out through Plan → ChildDispatcher
	// so each VM/K3sNode/AgentInstall gets its own resolve+cache+save
	// pipeline. Falls back to the imperative path when no
	// ChildDispatcher is on ctx (CLI direct-apply, unit tests).
	if cd, ok := operations.ChildDispatcherFrom(ctx); ok {
		return p.applyClusterViaPlan(ctx, manifest, cd)
	}

	creator := k3scluster.NewCreator(name, spec, p.config)
	dispatches := creator.GenerateDispatchRequests()

	rec := operations.RecorderFrom(ctx)

	// Apply each VM via the in-process VM provider. Phase 4 runs them
	// sequentially in this single op; Phase 4.5 surfaces each as a child
	// op row so the UI/CLI can show per-VM progress. Parallelism is a
	// separate followup.
	for _, d := range dispatches {
		vmManifest := d.Manifest
		if err := runChildVMApply(ctx, rec, vmManifest, p.vms); err != nil {
			return nil, err
		}
	}

	// Determine node IPs. Static path: AllocateIPs is deterministic per
	// (cluster, name). QGA path: poll vms.Get for each node's status.ip
	// until populated. Surfaced as a "discover-ips" child op so the user
	// can see the wait happening when the cluster is using DHCP+QGA.
	nodeNames := make([]string, 0, len(dispatches))
	for _, d := range dispatches {
		nodeNames = append(nodeNames, d.Manifest.Metadata.Name)
	}
	nodeIPs, err := p.resolveNodeIPs(ctx, rec, name, spec, nodeNames)
	if err != nil {
		return nil, err
	}

	// Install k3s + agent. cluster.InstallK3s does the SSH heavy lifting
	// + cert generation + verification — surfaced as one child step op
	// since splitting the per-node SSH work into separate rows would
	// require restructuring InstallK3s itself.
	result, err := runChildStep(ctx, rec, name, "install-k3s",
		"Install k3s + openctl-k3s-agent on all nodes",
		func() (any, error) { return creator.InstallK3s(nodeIPs) })
	if err != nil {
		return nil, fmt.Errorf("install k3s: %w", err)
	}
	installResult, _ := result.(*k3scluster.InstallResult)
	if installResult == nil {
		return nil, fmt.Errorf("install k3s: nil result")
	}

	// Persist state file (legacy YAML location for now; the controller may
	// migrate this to its DB in a later phase).
	state, err := p.saveState(name, manifest, spec, installResult, nodeIPs)
	if err != nil {
		return nil, fmt.Errorf("save state: %w", err)
	}
	return state, nil
}

// applyExisting handles re-apply of a Cluster that already has state. It
// computes the structural diff vs. the new manifest, enforces the
// destructive/catastrophic guardrails, and converges the child set —
// removals via VM delete (Phase 5), additions via the Joiner (Phase 5.x
// count-up), and per-node CPU/memory respecs via destroy + recreate +
// rejoin (Phase 5.x in-place spec changes).
func (p *Provider) applyExisting(ctx context.Context, manifest *protocol.Resource, name string, spec *k3sresources.ClusterSpec) (*protocol.Resource, error) {
	current, _ := readChildren(name)
	plan := computeChangePlan(name, spec, current)

	respecs := p.computeSpecRespecs(ctx, name, spec, current, toSet(append(plan.removeCPs, plan.removeWorkers...)))

	if !plan.hasChanges() && len(respecs) == 0 {
		// No structural diff and no spec drift — just return existing state.
		return p.loadState(name)
	}

	allowDestructive := manifest.Metadata.Annotations[annotAllowDestructive] == "true"
	iKnowThisBreaks := manifest.Metadata.Annotations[annotIKnowThisBreaks] == "true"

	if plan.removesAny() && !allowDestructive {
		return nil, fmt.Errorf("would remove %d node(s) (CPs %v, workers %v); pass --allow-destructive to confirm", len(plan.removeCPs)+len(plan.removeWorkers), plan.removeCPs, plan.removeWorkers)
	}
	if len(respecs) > 0 && !allowDestructive {
		names := make([]string, len(respecs))
		for i, r := range respecs {
			names[i] = r.Name
		}
		return nil, fmt.Errorf("would respec %d node(s) (%v) via destroy + recreate; pass --allow-destructive to confirm", len(respecs), names)
	}

	// Catastrophic-op detection: count current CPs/workers before applying
	// removals.
	var haveCPs, haveWorkers int
	cpPrefix := name + "-cp-"
	for _, c := range current {
		if c.Kind != "VirtualMachine" {
			continue
		}
		if strings.HasPrefix(c.Name, cpPrefix) {
			haveCPs++
		} else {
			haveWorkers++
		}
	}
	if reason := catastrophicReason(plan, haveCPs, haveWorkers); reason != "" && !iKnowThisBreaks {
		return nil, fmt.Errorf("catastrophic: %s; pass --i-know-this-breaks-the-cluster to override", reason)
	}
	if reason := catastrophicRespecReason(respecs, haveCPs, haveWorkers); reason != "" && !iKnowThisBreaks {
		return nil, fmt.Errorf("catastrophic: %s; pass --i-know-this-breaks-the-cluster to override", reason)
	}

	rec := operations.RecorderFrom(ctx)

	// Execute removals. Note: no kubectl drain (homelab assumption — workloads
	// tolerate node loss). Workers go first, then CPs, so we drop schedulable
	// capacity before touching apiserver replicas.
	if err := p.removeNodes(ctx, spec, plan.removeWorkers, plan.removeCPs); err != nil {
		return nil, err
	}

	// Compose the converged children set: keep survivors of any removes,
	// then append additions after the join succeeds.
	keep := make([]childRef, 0, len(current))
	removed := toSet(append(plan.removeCPs, plan.removeWorkers...))
	for _, c := range current {
		if !removed[c.Name] {
			keep = append(keep, c)
		}
	}

	// Phase 5.x count-up: add new nodes against the live cluster. With the
	// plan-based converge enabled and a ChildDispatcher present (controller
	// path) the new nodes are applied as Plan()-emitted VM/K3sNode/
	// AgentInstall children — each K3sNode resolves its join token from a
	// surviving CP's state via $ref, and each AgentInstall extends the CA
	// bundle itself. Otherwise it falls back to the imperative Joiner.
	addedEndpoints := map[string]string{}
	if len(plan.addCPs)+len(plan.addWorkers) > 0 {
		var joinEndpoints map[string]string
		var err error
		if cd, ok := operations.ChildDispatcherFrom(ctx); ok && convergeViaPlanEnabled() {
			joinEndpoints, err = p.addNodesViaPlan(ctx, cd, manifest, name, plan, current, removed)
		} else {
			joinEndpoints, err = p.applyCountUp(ctx, rec, name, spec, plan, current, removed)
		}
		if err != nil {
			return nil, err
		}
		addedEndpoints = joinEndpoints
		for _, n := range plan.addCPs {
			keep = append(keep, childRef{Provider: "proxmox", Kind: "VirtualMachine", Name: n})
		}
		for _, n := range plan.addWorkers {
			keep = append(keep, childRef{Provider: "proxmox", Kind: "VirtualMachine", Name: n})
		}
	}

	// Phase 5.x in-place respec: destroy → recreate → rejoin each affected
	// node, one at a time. Runs after adds so the cluster has its maximum
	// replica count before any individual node goes down for the respec.
	if len(respecs) > 0 {
		existingState, err := p.loadState(name)
		if err != nil {
			return nil, err
		}
		existingIPs := readAgentEndpoints(existingState)
		// Merge in any IPs we just learned from the count-up so respec on a
		// freshly-added node uses its new IP rather than failing the lookup.
		maps.Copy(existingIPs, addedEndpoints)
		survivingCPs := []string{}
		for _, c := range current {
			if c.Kind == "VirtualMachine" && strings.HasPrefix(c.Name, cpPrefix) && !removed[c.Name] {
				survivingCPs = append(survivingCPs, c.Name)
			}
		}
		if len(survivingCPs) == 0 && len(plan.addCPs) > 0 {
			survivingCPs = plan.addCPs
		}
		if len(survivingCPs) == 0 {
			return nil, fmt.Errorf("respec requires at least one CP to remain reachable")
		}
		firstCPName := survivingCPs[0]
		firstCPIP := existingIPs[firstCPName]
		if firstCPIP == "" {
			return nil, fmt.Errorf("no IP for surviving CP %s; can't rejoin after respec", firstCPName)
		}
		updated, err := p.applyRespecs(ctx, rec, name, spec, respecs, existingIPs, firstCPName, firstCPIP)
		if err != nil {
			return nil, err
		}
		maps.Copy(addedEndpoints, updated)
	}

	if err := p.rewriteState(name, manifest, keep, addedEndpoints, removed); err != nil {
		return nil, fmt.Errorf("rewrite state: %w", err)
	}
	return p.loadState(name)
}

// removeNodes tears down the given worker and control-plane nodes during an
// existing-cluster converge. Workers go before CPs so schedulable capacity
// drops before apiserver replicas (no kubectl drain — homelab assumption).
//
// When a ChildDispatcher is present (a dispatched op — the controller path),
// each node is removed as its full plan-native child set — AgentInstall +
// K3sNode + VM — via DeleteChild, so the per-node state files under
// state/k3s-nodes/ and state/k3s-agent-installs/ are cleaned up instead of
// orphaned. (Before this, scale-down deleted only the VM and leaked those
// two files.) Without a dispatcher (CLI direct-apply, which never wrote
// those files) it falls back to the VM-only delete.
func (p *Provider) removeNodes(ctx context.Context, spec *k3sresources.ClusterSpec, removeWorkers, removeCPs []string) error {
	cd, hasCD := operations.ChildDispatcherFrom(ctx)
	rec := operations.RecorderFrom(ctx)
	viaPlan := hasCD && convergeViaPlanEnabled()
	del := func(node string) error {
		if viaPlan {
			return p.deleteNodeChildren(ctx, cd, spec, node)
		}
		return runChildVMDelete(ctx, rec, node, p.vms)
	}
	for _, w := range removeWorkers {
		if err := del(w); err != nil {
			return fmt.Errorf("delete worker %s: %w", w, err)
		}
	}
	for _, cp := range removeCPs {
		if err := del(cp); err != nil {
			return fmt.Errorf("delete control-plane %s: %w", cp, err)
		}
	}
	return nil
}

// deleteNodeChildren removes a single node's AgentInstall, K3sNode, and VM
// through the ChildDispatcher (provider.Delete + manifest-store removal +
// per-node state cleanup). AgentInstall and K3sNode go before the VM so
// their best-effort SSH uninstall can still reach a live guest; all three
// are idempotent on an already-absent target, so a re-run after partial
// progress is safe. Child names/apiVersions mirror what Plan() emits.
func (p *Provider) deleteNodeChildren(ctx context.Context, cd operations.ChildDispatcher, spec *k3sresources.ClusterSpec, node string) error {
	vmAPIVersion := spec.Compute.Provider + ".openctl.io/v1"
	for _, c := range []*protocol.Resource{
		{APIVersion: "k3s.openctl.io/v1", Kind: kindAgentInstall, Metadata: protocol.ResourceMetadata{Name: node + "-agent"}},
		{APIVersion: "k3s.openctl.io/v1", Kind: kindK3sNode, Metadata: protocol.ResourceMetadata{Name: node}},
		{APIVersion: vmAPIVersion, Kind: "VirtualMachine", Metadata: protocol.ResourceMetadata{Name: node}},
	} {
		if err := cd.DeleteChild(ctx, c); err != nil {
			return fmt.Errorf("delete %s %q: %w", c.Kind, c.Metadata.Name, err)
		}
	}
	return nil
}

func isProvisioningCluster(r *protocol.Resource) bool {
	if r == nil || r.Status == nil {
		return false
	}
	phase, _ := r.Status["phase"].(string)
	return phase == "Provisioning"
}

func (p *Provider) Get(ctx context.Context, kind, name string) (*protocol.Resource, error) {
	if kind == kindK3sNode {
		return p.getK3sNode(ctx, name)
	}
	if kind == kindAgentInstall {
		return p.getAgentInstall(ctx, name)
	}
	if err := requireKindCluster(kind); err != nil {
		return nil, err
	}
	r, err := p.loadState(name)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, providers.NotFound(kind, name)
	}
	// Phase 5: synthesize the observed node counts from the children list so
	// structural drift surfaces against the manifest. The saved spec
	// otherwise echoes back the manifest verbatim and would always read
	// drift-free even after an out-of-band VM deletion.
	if children, err := readChildren(name); err == nil {
		applyObservedCounts(r, name, children)
	}
	return r, nil
}

// applyObservedCounts overwrites spec.nodes.controlPlane.count and each
// spec.nodes.workers[*].count with the *actual* number of children matching
// that role, derived from the names in `children`. Names follow the
// `<cluster>-cp-<i>` / `<cluster>-<pool>-<i>` pattern set by NodeNames.
func applyObservedCounts(r *protocol.Resource, clusterName string, children []childRef) {
	if r.Spec == nil {
		return
	}
	nodes, ok := r.Spec["nodes"].(map[string]any)
	if !ok {
		return
	}
	cpCount := 0
	workerCounts := map[string]int{} // pool name → count
	cpPrefix := clusterName + "-cp-"
	for _, c := range children {
		if c.Kind != "VirtualMachine" {
			continue
		}
		switch {
		case strings.HasPrefix(c.Name, cpPrefix):
			cpCount++
		case strings.HasPrefix(c.Name, clusterName+"-"):
			// Strip "<cluster>-" prefix and "-<index>" suffix to recover the
			// pool name.
			rest := strings.TrimPrefix(c.Name, clusterName+"-")
			if dash := strings.LastIndex(rest, "-"); dash > 0 {
				pool := rest[:dash]
				workerCounts[pool]++
			}
		}
	}
	if cp, ok := nodes["controlPlane"].(map[string]any); ok {
		cp["count"] = cpCount
	}
	if workers, ok := nodes["workers"].([]any); ok {
		for _, w := range workers {
			pool, ok := w.(map[string]any)
			if !ok {
				continue
			}
			poolName, _ := pool["name"].(string)
			if poolName == "" {
				poolName = "worker"
			}
			pool["count"] = workerCounts[poolName]
		}
	}
}

func (p *Provider) List(ctx context.Context, kind string) ([]*protocol.Resource, error) {
	if kind == kindK3sNode {
		return p.listK3sNodes(ctx)
	}
	if kind == kindAgentInstall {
		return p.listAgentInstalls(ctx)
	}
	if err := requireKindCluster(kind); err != nil {
		return nil, err
	}
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*protocol.Resource
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		clusterName := strings.TrimSuffix(e.Name(), ".yaml")
		r, err := p.loadState(clusterName)
		if err != nil || r == nil {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// Delete tears down a cluster: deletes child VMs via the VM provider, then
// removes the local state. Idempotent on missing cluster.
func (p *Provider) Delete(ctx context.Context, kind, name string) error {
	if kind == kindK3sNode {
		return p.deleteK3sNode(ctx, name)
	}
	if kind == kindAgentInstall {
		return p.deleteAgentInstall(ctx, name)
	}
	if err := requireKindCluster(kind); err != nil {
		return err
	}
	state, err := p.loadState(name)
	if err != nil {
		return err
	}
	if state == nil {
		return nil // idempotent
	}
	// Read children from state file.
	children, _ := readChildren(name)
	for _, child := range children {
		if child.Provider == "proxmox" && child.Kind == "VirtualMachine" {
			// Per-VM delete may legitimately fail if the VM was already
			// removed externally; treat that as success.
			if err := p.vms.Delete(ctx, child.Kind, child.Name); err != nil &&
				!strings.Contains(err.Error(), "not found") {
				return fmt.Errorf("delete VM %s: %w", child.Name, err)
			}
		}
		// Best-effort cleanup of per-node K3sNode + AgentInstall
		// state files emitted by the plan-based Apply path. Only
		// meaningful for VirtualMachine children (their names match
		// K3sNode / AgentInstall names by convention: <vm>-agent).
		if child.Kind == "VirtualMachine" {
			_ = removeNodeState(child.Name)
			_ = removeAgentInstallState(child.Name + "-agent")
		}
	}
	// Remove state files.
	dir, _ := stateDir()
	_ = os.Remove(filepath.Join(dir, name+".yaml"))
	_ = os.RemoveAll(filepath.Join(dir, name))
	// Bundle dir from the plan-path CA materialization also lives
	// under state/k3s/<name>/, so the RemoveAll above already
	// catches it — no separate cleanup needed.
	return nil
}

func requireKindCluster(got string) error {
	if got != kindCluster {
		return fmt.Errorf("k3s provider does not handle kind %q (only %q)", got, kindCluster)
	}
	return nil
}

func stateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".openctl", "state", "k3s"), nil
}

// loadState reads the legacy YAML state file. Returns nil, nil if missing
// (caller treats as "not yet applied").
func (p *Provider) loadState(name string) (*protocol.Resource, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name+".yaml")
	data, err := os.ReadFile(path) // #nosec G304 -- name comes from typed RPC
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", path, err)
	}
	r := &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       kindCluster,
		Metadata:   protocol.ResourceMetadata{Name: name},
	}
	if spec, ok := raw["spec"].(map[string]any); ok {
		r.Spec = spec
	}
	if status, ok := raw["status"].(map[string]any); ok {
		r.Status = status
	}
	return r, nil
}

// rewriteState updates the saved state file to reflect a converged child
// set. Preserves the existing status/outputs (the cluster is still up)
// and replaces the spec with the new manifest's spec so future Gets diff
// against the user's current intent. addEndpoints carries node→IP entries
// from a count-up to merge into status.outputs.agent.endpoints;
// removedNames carries names to drop from the same map. Both may be nil.
func (p *Provider) rewriteState(name string, manifest *protocol.Resource, keep []childRef, addEndpoints map[string]string, removedNames map[string]bool) error {
	dir, err := stateDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, name+".yaml")
	data, err := os.ReadFile(path) // #nosec G304 -- name comes from typed RPC
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	doc["spec"] = manifest.Spec
	// Children rewritten in the legacy YAML's tag style.
	rendered := make([]map[string]any, 0, len(keep))
	for _, c := range keep {
		rendered = append(rendered, map[string]any{
			"provider": c.Provider,
			"kind":     c.Kind,
			"name":     c.Name,
		})
	}
	doc["children"] = rendered
	if md, ok := doc["metadata"].(map[string]any); ok {
		md["updatedAt"] = time.Now().UTC().Format(time.RFC3339)
	}
	if len(addEndpoints) > 0 || len(removedNames) > 0 {
		updateAgentEndpoints(doc, addEndpoints, removedNames)
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}

// updateAgentEndpoints merges add/remove diffs into the saved state's
// status.outputs.agent.endpoints map. Used by count-up + count-down to
// keep the agent endpoints in sync with the surviving children list.
// The endpoints map keys are node names; values are IP strings.
func updateAgentEndpoints(doc map[string]any, add map[string]string, remove map[string]bool) {
	status, ok := doc["status"].(map[string]any)
	if !ok {
		return
	}
	outputs, ok := status["outputs"].(map[string]any)
	if !ok {
		return
	}
	agent, ok := outputs["agent"].(map[string]any)
	if !ok {
		return
	}
	endpoints, ok := agent["endpoints"].(map[string]any)
	if !ok {
		endpoints = map[string]any{}
	}
	for name := range remove {
		delete(endpoints, name)
	}
	for name, ip := range add {
		endpoints[name] = ip
	}
	agent["endpoints"] = endpoints
}

// childRef matches the YAML shape used by the legacy state files.
type childRef struct {
	Provider string `yaml:"provider"`
	Kind     string `yaml:"kind"`
	Name     string `yaml:"name"`
}

func readChildren(name string) ([]childRef, error) {
	dir, _ := stateDir()
	data, err := os.ReadFile(filepath.Join(dir, name+".yaml")) // #nosec G304 -- name from typed RPC
	if err != nil {
		return nil, err
	}
	var doc struct {
		Children []childRef `yaml:"children"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return doc.Children, nil
}

// saveState writes a state YAML matching the format the legacy plugin uses,
// so existing tooling (kubectl path, etc.) keeps working. Returns the
// Resource form for the caller to echo back.
func (p *Provider) saveState(name string, manifest *protocol.Resource, _ *k3sresources.ClusterSpec, result *k3scluster.InstallResult, staticIPs map[string]string) (*protocol.Resource, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	children := make([]childRef, 0, len(staticIPs))
	for nodeName := range staticIPs {
		children = append(children, childRef{
			Provider: "proxmox",
			Kind:     "VirtualMachine",
			Name:     nodeName,
		})
	}

	outputs := map[string]any{
		"kubeconfigPath": result.KubeconfigPath,
		"serverIP":       result.ServerIP,
	}
	if result.AgentBundleDir != "" {
		outputs["agent"] = map[string]any{
			"bundleDir":      result.AgentBundleDir,
			"caPath":         filepath.Join(result.AgentBundleDir, "ca.pem"),
			"clientCertPath": filepath.Join(result.AgentBundleDir, "client.pem"),
			"clientKeyPath":  filepath.Join(result.AgentBundleDir, "client.key"),
			"port":           result.AgentPort,
			"endpoints":      result.AgentEndpoints,
		}
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
