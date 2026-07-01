package k3s

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/openctl/openctl/pkg/k3s/agent/bootstrap"
	"github.com/openctl/openctl/pkg/k3s/agent/certs"
	"github.com/openctl/openctl/pkg/k3s/ssh"
	"github.com/openctl/openctl/pkg/protocol"
)

const (
	kindAgentInstall = "AgentInstall"

	// agentDeleteSSHTimeout: short so a persistently-down node
	// doesn't block Delete indefinitely (matches the K3sNode
	// convention).
	agentDeleteSSHTimeout = 15 * time.Second
)

// agentInstallState is what we persist per AgentInstall. Kept
// small — the CA bundle lives in the Cluster's state dir and
// gets rehydrated on demand, so this file just records that the
// install happened.
type agentInstallState struct {
	Name        string    `yaml:"name"`
	VMName      string    `yaml:"vmName"`
	VMIP        string    `yaml:"vmIP"`
	ClusterName string    `yaml:"clusterName"`
	Arch        string    `yaml:"arch,omitempty"`
	Init        string    `yaml:"init,omitempty"`
	Installed   bool      `yaml:"installed"`
	InstalledAt time.Time `yaml:"installedAt,omitempty"`
}

// applyAgentInstall installs (or reinstalls) the openctl-k3s-agent
// on the referenced VM. Called by Provider.Apply when the manifest's
// Kind is AgentInstall.
//
// Preconditions (checked here, surfaced as errors):
//   - the referenced VM has status.ip populated
//   - a Cluster with clusterName exists on disk (its bundle dir has
//     ca.pem / ca.key so we can mint a per-node server cert)
func (p *Provider) applyAgentInstall(ctx context.Context, manifest *protocol.Resource) (*protocol.Resource, error) {
	name := manifest.Metadata.Name
	if name == "" {
		return nil, fmt.Errorf("metadata.name is required")
	}
	spec, err := parseAgentInstallSpec(manifest)
	if err != nil {
		return nil, err
	}

	if spec.vmIP == "" {
		ip, err := waitForVMIP(ctx, p.vms, spec.vmName, vmIPWaitTimeout)
		if err != nil {
			return nil, fmt.Errorf("wait for VM %s IP: %w", spec.vmName, err)
		}
		spec.vmIP = ip
	}

	// Fast path: same install already recorded.
	if existing, err := loadAgentInstallState(name); err == nil && existing != nil && existing.Installed &&
		existing.VMIP == spec.vmIP && existing.ClusterName == spec.clusterName {
		return agentInstallStateToResource(name, manifest, existing), nil
	}

	bundleDir, err := clusterBundleDir(spec.clusterName)
	if err != nil {
		return nil, err
	}
	bundle, err := certs.LoadBundle(bundleDir)
	if err != nil {
		return nil, fmt.Errorf("load cert bundle for cluster %q from %s: %w — an AgentInstall requires an existing Cluster with a CA on disk",
			spec.clusterName, bundleDir, err)
	}

	// Mint a server cert for this node if the bundle doesn't
	// already have one. MintServerCerts is idempotent per node
	// (it overwrites the entry) — a re-mint on top of the same
	// vmIP produces an equivalent cert.
	if _, ok := bundle.ServerCerts[spec.vmName]; !ok {
		if err := bundle.MintServerCerts([]certs.NodeIdentity{{Name: spec.vmName, IP: spec.vmIP}}); err != nil {
			return nil, fmt.Errorf("mint server cert for %s: %w", spec.vmName, err)
		}
		if err := bundle.WriteTo(bundleDir); err != nil {
			return nil, fmt.Errorf("persist minted cert into bundle: %w", err)
		}
	}

	client, err := ssh.WaitForSSH(spec.vmIP, sshPort, spec.sshUser, spec.sshKeyPath, sshWaitTimeout)
	if err != nil {
		return nil, fmt.Errorf("ssh to %s@%s: %w", spec.sshUser, spec.vmIP, err)
	}
	defer func() { _ = client.Close() }()

	installer := &bootstrap.Installer{}
	host, err := installer.Install(client, bundle.ServerCerts[spec.vmName], bundle.CACertPEM)
	if err != nil {
		return nil, fmt.Errorf("install openctl-k3s-agent on %s: %w", spec.vmIP, err)
	}

	state := &agentInstallState{
		Name:        name,
		VMName:      spec.vmName,
		VMIP:        spec.vmIP,
		ClusterName: spec.clusterName,
		Arch:        host.Arch,
		Init:        string(host.Init),
		Installed:   true,
		InstalledAt: time.Now(),
	}
	if err := saveAgentInstallState(state); err != nil {
		return nil, fmt.Errorf("save state: %w", err)
	}
	return agentInstallStateToResource(name, manifest, state), nil
}

func (p *Provider) getAgentInstall(_ context.Context, name string) (*protocol.Resource, error) {
	state, err := loadAgentInstallState(name)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, fmt.Errorf("AgentInstall %q not found", name)
	}
	return agentInstallStateToResource(name, nil, state), nil
}

func (p *Provider) listAgentInstalls(_ context.Context) ([]*protocol.Resource, error) {
	dir, err := agentInstallStateDir()
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
		name := strings.TrimSuffix(e.Name(), ".yaml")
		state, err := loadAgentInstallState(name)
		if err != nil || state == nil {
			continue
		}
		out = append(out, agentInstallStateToResource(name, nil, state))
	}
	return out, nil
}

func (p *Provider) deleteAgentInstall(_ context.Context, name string) error {
	state, err := loadAgentInstallState(name)
	if err != nil {
		return err
	}
	if state == nil {
		return nil // idempotent
	}
	// Best-effort uninstall: stop the service and drop the binary
	// + config dir. Ignore SSH errors so a permanently-down node
	// doesn't block state cleanup.
	if client, err := ssh.WaitForSSH(state.VMIP, sshPort, "ubuntu", "", agentDeleteSSHTimeout); err == nil {
		switch state.Init {
		case "systemd":
			_, _ = client.RunSudo("systemctl disable --now openctl-k3s-agent.service || true")
			_, _ = client.RunSudo("rm -f /etc/systemd/system/openctl-k3s-agent.service")
			_, _ = client.RunSudo("systemctl daemon-reload || true")
		case "openrc":
			_, _ = client.RunSudo("rc-service openctl-k3s-agent stop || true")
			_, _ = client.RunSudo("rc-update del openctl-k3s-agent default || true")
			_, _ = client.RunSudo("rm -f /etc/init.d/openctl-k3s-agent")
		}
		_, _ = client.RunSudo("rm -f /usr/local/bin/openctl-k3s-agent")
		_, _ = client.RunSudo("rm -rf /etc/openctl-k3s-agent")
		_ = client.Close()
	}
	return removeAgentInstallState(name)
}

// agentInstallSpec is the post-ref-resolution shape the provider
// consumes. vmRef comes in as a whole-resource map (metadata +
// status); clusterName is a plain string.
type agentInstallSpec struct {
	vmName      string
	vmIP        string
	clusterName string
	sshUser     string
	sshKeyPath  string
}

func parseAgentInstallSpec(manifest *protocol.Resource) (*agentInstallSpec, error) {
	if manifest.Spec == nil {
		return nil, fmt.Errorf("spec is required")
	}
	out := &agentInstallSpec{sshUser: "ubuntu"}

	vmRef, ok := manifest.Spec["vmRef"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("spec.vmRef is required (whole-resource ref to the target VM)")
	}
	if md, ok := vmRef["metadata"].(map[string]any); ok {
		if s, ok := md["name"].(string); ok {
			out.vmName = s
		}
	}
	if status, ok := vmRef["status"].(map[string]any); ok {
		if ip, ok := status["ip"].(string); ok {
			out.vmIP = ip
		}
	}
	if out.vmName == "" {
		return nil, fmt.Errorf("vmRef target has no metadata.name")
	}
	// vmIP is allowed to be "" — applyAgentInstall polls the VM
	// provider for status.ip when the ref-resolved manifest was
	// captured mid-boot (e.g. dispatched immediately after VM
	// create in a Cluster Plan fan-out).

	if s, ok := manifest.Spec["clusterName"].(string); ok && s != "" {
		out.clusterName = s
	} else {
		return nil, fmt.Errorf("spec.clusterName is required (must name an existing k3s Cluster whose CA bundle backs this agent)")
	}

	if sshBlock, ok := manifest.Spec["ssh"].(map[string]any); ok {
		if u, ok := sshBlock["user"].(string); ok && u != "" {
			out.sshUser = u
		}
		if p, ok := sshBlock["privateKeyPath"].(string); ok {
			out.sshKeyPath = p
		}
	}
	if out.sshKeyPath == "" {
		return nil, fmt.Errorf("spec.ssh.privateKeyPath is required")
	}
	return out, nil
}

// agentInstallStateToResource projects the persisted state (and
// optionally the incoming manifest) back into a protocol.Resource
// with a populated status.
func agentInstallStateToResource(name string, manifest *protocol.Resource, state *agentInstallState) *protocol.Resource {
	spec := map[string]any{}
	if manifest != nil && manifest.Spec != nil {
		spec = manifest.Spec
	}
	status := map[string]any{
		"installed":   state.Installed,
		"vmName":      state.VMName,
		"vmIP":        state.VMIP,
		"clusterName": state.ClusterName,
	}
	if state.Arch != "" {
		status["arch"] = state.Arch
	}
	if state.Init != "" {
		status["init"] = state.Init
	}
	if !state.InstalledAt.IsZero() {
		status["installedAt"] = state.InstalledAt.Format(time.RFC3339)
	}
	return &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       kindAgentInstall,
		Metadata:   protocol.ResourceMetadata{Name: name},
		Spec:       spec,
		Status:     status,
	}
}

// Persistence: one YAML file per install under
// ~/.openctl/state/k3s-agent-installs/.

func agentInstallStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".openctl", "state", "k3s-agent-installs"), nil
}

func loadAgentInstallState(name string) (*agentInstallState, error) {
	dir, err := agentInstallStateDir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, name+".yaml")) // #nosec G304 -- controller-owned path
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var s agentInstallState
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	return &s, nil
}

func saveAgentInstallState(s *agentInstallState) error {
	dir, err := agentInstallStateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, s.Name+".yaml"), data, 0o600)
}

func removeAgentInstallState(name string) error {
	dir, err := agentInstallStateDir()
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(dir, name+".yaml"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
