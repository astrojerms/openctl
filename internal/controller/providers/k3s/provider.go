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
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

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
func (p *Provider) Kinds() []string { return []string{kindCluster} }

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

// Apply: no-op if a state file for the cluster already exists; otherwise
// creates child VMs via the VM provider, installs k3s + agent, persists
// state. Static IPs are required in Phase 4 (no QGA polling in the
// in-process path yet).
func (p *Provider) Apply(ctx context.Context, manifest *protocol.Resource) (*protocol.Resource, error) {
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
	if spec.Network.StaticIPs == nil || spec.Network.StaticIPs.StartIP == "" {
		return nil, fmt.Errorf("static IPs required (set spec.network.staticIPs.{startIP,gateway,netmask}); QGA-based IP discovery in the controller path is a Phase 4.5 followup")
	}

	// No-op-on-existing per the architecture decision.
	if existing, err := p.loadState(name); err == nil && existing != nil {
		return existing, nil
	}

	// Pre-allocate static IPs deterministically per node name.
	staticIPs, err := k3sresources.AllocateIPs(name, spec)
	if err != nil {
		return nil, fmt.Errorf("allocate static IPs: %w", err)
	}

	creator := k3scluster.NewCreator(name, spec, p.config)
	dispatches := creator.GenerateDispatchRequests()

	// Apply each VM via the in-process VM provider. Phase 4 runs them
	// sequentially in this single op; Phase 4.5 may parallelize via the
	// child-ops mechanism.
	for _, d := range dispatches {
		if _, err := p.vms.Apply(ctx, d.Manifest); err != nil {
			return nil, fmt.Errorf("apply VM %s: %w", d.Manifest.Metadata.Name, err)
		}
	}

	// Install k3s + agent. cluster.InstallK3s does the SSH heavy lifting
	// + cert generation + verification.
	result, err := creator.InstallK3s(staticIPs)
	if err != nil {
		return nil, fmt.Errorf("install k3s: %w", err)
	}

	// Persist state file (legacy YAML location for now; the controller may
	// migrate this to its DB in a later phase).
	state, err := p.saveState(name, manifest, spec, result, staticIPs)
	if err != nil {
		return nil, fmt.Errorf("save state: %w", err)
	}
	return state, nil
}

func (p *Provider) Get(_ context.Context, kind, name string) (*protocol.Resource, error) {
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
	return r, nil
}

func (p *Provider) List(_ context.Context, kind string) ([]*protocol.Resource, error) {
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
	}
	// Remove state files.
	dir, _ := stateDir()
	_ = os.Remove(filepath.Join(dir, name+".yaml"))
	_ = os.RemoveAll(filepath.Join(dir, name))
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
