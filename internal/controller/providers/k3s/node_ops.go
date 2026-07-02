package k3s

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/openctl/openctl/pkg/k3s/ssh"
	"github.com/openctl/openctl/pkg/protocol"
)

const (
	kindK3sNode = "K3sNode"

	// sshWaitTimeout is how long applyK3sNode waits for SSH to come
	// up before giving up. Generous because a fresh VM may take a
	// while to finish first-boot cloud-init before sshd starts.
	sshWaitTimeout = 5 * time.Minute
	sshPort        = 22
)

// nodeState is what we persist per K3sNode. YAML-shaped for
// consistency with the Cluster state files under ~/.openctl/state/k3s/.
type nodeState struct {
	Name        string    `yaml:"name"`
	VMName      string    `yaml:"vmName"`
	VMIP        string    `yaml:"vmIP"`
	Role        string    `yaml:"role"`
	Installed   bool      `yaml:"installed"`
	NodeToken   string    `yaml:"nodeToken,omitempty"`  // servers only
	Kubeconfig  string    `yaml:"kubeconfig,omitempty"` // first-server only
	InstalledAt time.Time `yaml:"installedAt,omitempty"`
}

// applyK3sNode installs k3s on the referenced VM. Called by
// Provider.Apply when the manifest's Kind is K3sNode.
//
// Ref resolution has already happened by the time we get here — the
// dispatcher runs the refs.Resolver before calling us — so vmRef
// contains the resolved VM (as a map with metadata + spec + status),
// joinFrom (if any) contains the resolved node-token string, and
// joinURLFrom contains the resolved server IP.
func (p *Provider) applyK3sNode(ctx context.Context, manifest *protocol.Resource) (*protocol.Resource, error) {
	name := manifest.Metadata.Name
	if name == "" {
		return nil, fmt.Errorf("metadata.name is required")
	}
	spec, err := parseK3sNodeSpec(manifest)
	if err != nil {
		return nil, err
	}

	// Fast path: already installed. Return the stored state as
	// status so the caller sees a Healthy resource without a fresh
	// SSH. Phase 7's verifying cache handles the manifest-hash check
	// at the dispatcher layer; this is a defense-in-depth belt.
	if existing, err := loadNodeState(name); err == nil && existing != nil && existing.Installed {
		return nodeStateToResource(name, manifest, existing), nil
	}

	// If vmRef was resolved before the VM's QGA reported its IP
	// (Plan-emitted K3sNode dispatched immediately after VM create),
	// poll until the IP appears.
	if spec.vmIP == "" {
		ip, err := waitForVMIP(ctx, p.vms, spec.vmName, vmIPWaitTimeout)
		if err != nil {
			return nil, fmt.Errorf("wait for VM %s IP: %w", spec.vmName, err)
		}
		spec.vmIP = ip
	}

	// Bring up SSH. WaitForSSH retries until sshd responds — a fresh
	// VM's cloud-init can take a minute past IP acquisition before
	// the ssh port is answering.
	client, err := ssh.WaitForSSH(spec.vmIP, sshPort, spec.sshUser, spec.sshKeyPath, sshWaitTimeout)
	if err != nil {
		return nil, fmt.Errorf("ssh to %s@%s: %w", spec.sshUser, spec.vmIP, err)
	}
	defer func() { _ = client.Close() }()

	// Build the appropriate install command for this role.
	cmd := buildNodeInstallCommand(spec)
	if _, err := client.RunSudo(cmd); err != nil {
		return nil, fmt.Errorf("k3s install failed on %s: %w", spec.vmIP, err)
	}

	// Populate state.
	state := &nodeState{
		Name:        name,
		VMName:      spec.vmName,
		VMIP:        spec.vmIP,
		Role:        spec.role,
		Installed:   true,
		InstalledAt: time.Now(),
	}

	// Server nodes: grab the node token (any subsequent K3sNode with
	// joinFrom pointing at this one will resolve to it).
	if spec.role == "server" {
		token, err := client.RunSudo("cat /var/lib/rancher/k3s/server/node-token")
		if err != nil {
			return nil, fmt.Errorf("read node-token: %w", err)
		}
		state.NodeToken = strings.TrimSpace(token)

		// First server (no joinFrom): grab the kubeconfig too.
		// Subsequent servers already have a working cluster to talk
		// to; the operator gets the kubeconfig from the first server.
		if spec.joinFromToken == "" {
			kc, err := client.RunSudo("cat /etc/rancher/k3s/k3s.yaml")
			if err != nil {
				return nil, fmt.Errorf("read kubeconfig: %w", err)
			}
			kc = strings.ReplaceAll(kc, "127.0.0.1", spec.vmIP)
			kc = strings.ReplaceAll(kc, "localhost", spec.vmIP)
			state.Kubeconfig = kc
			// Save to the standard kubeconfig path so the existing
			// get-kubeconfig action can find it. Same directory shape
			// as the Cluster provider uses.
			if err := saveK3sNodeKubeconfig(name, kc); err != nil {
				return nil, fmt.Errorf("save kubeconfig: %w", err)
			}
		}
	}

	if err := saveNodeState(state); err != nil {
		return nil, fmt.Errorf("save state: %w", err)
	}
	return nodeStateToResource(name, manifest, state), nil
}

func (p *Provider) getK3sNode(_ context.Context, name string) (*protocol.Resource, error) {
	state, err := loadNodeState(name)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, fmt.Errorf("K3sNode %q not found", name)
	}
	return nodeStateToResource(name, nil, state), nil
}

func (p *Provider) listK3sNodes(_ context.Context) ([]*protocol.Resource, error) {
	dir, err := nodeStateDir()
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
		state, err := loadNodeState(name)
		if err != nil || state == nil {
			continue
		}
		out = append(out, nodeStateToResource(name, nil, state))
	}
	return out, nil
}

func (p *Provider) deleteK3sNode(_ context.Context, name string) error {
	state, err := loadNodeState(name)
	if err != nil {
		return err
	}
	if state == nil {
		return nil // idempotent
	}
	// Best-effort uninstall — the standard k3s uninstall script name
	// depends on role. Ignore errors so a missing VM (already gone,
	// unreachable) doesn't block state cleanup.
	// TODO(phase 8 step 3): move this into the AgentInstall / K3sNode
	// provider properly once we have per-node lifecycle semantics.
	uninstallScript := "/usr/local/bin/k3s-uninstall.sh"
	if state.Role == "agent" {
		uninstallScript = "/usr/local/bin/k3s-agent-uninstall.sh"
	}
	// Use a short SSH timeout — a persistently-down node shouldn't
	// block controller Delete indefinitely.
	if client, err := ssh.WaitForSSH(state.VMIP, sshPort, "ubuntu", "", 15*time.Second); err == nil {
		_, _ = client.RunSudo(uninstallScript)
		_ = client.Close()
	}
	return removeNodeState(name)
}

// k3sNodeSpec is the post-ref-resolution shape the provider consumes.
// vmRef comes in as a whole-resource map (with metadata + spec +
// status); joinFrom / joinURLFrom come in as either whole resources
// (with field="") or the resolved field value string.
type k3sNodeSpec struct {
	vmName        string
	vmIP          string
	role          string
	joinFromToken string
	joinFromIP    string
	version       string
	extraArgs     []string
	sshUser       string
	sshKeyPath    string
}

func parseK3sNodeSpec(manifest *protocol.Resource) (*k3sNodeSpec, error) {
	if manifest.Spec == nil {
		return nil, fmt.Errorf("spec is required")
	}
	out := &k3sNodeSpec{sshUser: "ubuntu"}

	vmRef, ok := manifest.Spec["vmRef"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("spec.vmRef is required (whole-resource ref to the target VM)")
	}
	// vmRef is a whole-resource map: extract metadata.name + status.ip.
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
	// Static-IP shortcut: `spec.vmIP` set on the K3sNode manifest
	// (populated by Cluster.Plan when spec.network.staticIPs is
	// configured) wins over anything the resolver put in
	// vmRef.status.ip. This lets Plan-emitted K3sNodes for
	// static-IP clusters skip waitForVMIP entirely — the VM's
	// address is deterministic and doesn't need QGA.
	if ip, ok := manifest.Spec["vmIP"].(string); ok && ip != "" {
		out.vmIP = ip
	}
	// vmIP is allowed to be "" here — applyK3sNode calls waitForVMIP
	// with the k3s Provider's VMApplier to poll status.ip after a
	// fresh Plan-emitted VM finishes booting. Standalone K3sNode
	// tests that carry a pre-populated status.ip in vmRef still land
	// in this parse with vmIP set and skip the wait.

	if role, ok := manifest.Spec["role"].(string); ok {
		out.role = role
	} else {
		return nil, fmt.Errorf("spec.role is required (server | agent)")
	}
	if out.role != "server" && out.role != "agent" {
		return nil, fmt.Errorf("spec.role must be server or agent (got %q)", out.role)
	}

	// joinFrom: either a whole-resource map (extract status.nodeToken
	// + status.ip) or a bare string (already resolved to nodeToken
	// via field="status.nodeToken").
	switch v := manifest.Spec["joinFrom"].(type) {
	case string:
		out.joinFromToken = v
	case map[string]any:
		if status, ok := v["status"].(map[string]any); ok {
			if s, ok := status["nodeToken"].(string); ok {
				out.joinFromToken = s
			}
			if ip, ok := status["vmIP"].(string); ok {
				out.joinFromIP = ip
			}
		}
	}
	if url, ok := manifest.Spec["joinURLFrom"].(string); ok {
		out.joinFromIP = url
	} else if url, ok := manifest.Spec["joinURLFrom"].(map[string]any); ok {
		if status, ok := url["status"].(map[string]any); ok {
			if ip, ok := status["vmIP"].(string); ok {
				out.joinFromIP = ip
			}
		}
	}
	// For non-first nodes, both a token and an address are required.
	if out.role == "agent" || (out.role == "server" && out.joinFromToken != "") {
		if out.joinFromToken == "" {
			return nil, fmt.Errorf("spec.joinFrom is required for %q roles (must resolve to status.nodeToken of an existing K3sNode server)", out.role)
		}
		if out.joinFromIP == "" {
			return nil, fmt.Errorf("spec.joinURLFrom is required alongside joinFrom (must resolve to the target K3sNode's vmIP)")
		}
	}

	if v, ok := manifest.Spec["version"].(string); ok {
		out.version = v
	}
	if v, ok := manifest.Spec["extraArgs"].([]any); ok {
		for _, a := range v {
			if s, ok := a.(string); ok {
				out.extraArgs = append(out.extraArgs, s)
			}
		}
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

// buildNodeInstallCommand assembles the shell one-liner used to
// install k3s. Three cases: first server (no joinFrom), joining
// server, joining agent. Extracted for testability — the shape
// mirrors what pkg/k3s/cluster.Creator produces, kept side-by-side
// deliberately so a future PR (Cluster.Plan refactor, step 4) can
// unify them.
//
// Two hardening steps wrap the k3s installer:
//
//   - `cloud-init status --wait` first. WaitForSSH returns as soon as
//     sshd accepts a connection, but on a freshly-cloned VM cloud-init
//     is often still applying the network config, dpkg locks, etc.
//     Running the k3s installer against a half-configured system is
//     what caused the homelab validation to see
//     "cat: node-token: No such file or directory" — the installer
//     script was killed mid-run and no k3s.service was ever created.
//
//   - `bash -c 'set -o pipefail; ...'` around `curl … | sh -s -`.
//     Under plain POSIX sh a pipeline's exit code is the last stage's,
//     so a failing `curl` upstream of `sh -s -` still reports success.
//     That's exactly how the failure above hid: the installer never
//     ran but SSH returned 0. pipefail propagates the real error.
func buildNodeInstallCommand(s *k3sNodeSpec) string {
	inner := "curl -sfL https://get.k3s.io | "
	var env []string
	if s.version != "" {
		env = append(env, fmt.Sprintf("INSTALL_K3S_VERSION=%s", s.version))
	}
	// First server: no join token, no server URL.
	if s.role == "server" && s.joinFromToken == "" {
		if len(env) > 0 {
			inner += strings.Join(env, " ") + " "
		}
		inner += "sh -s -"
		if len(s.extraArgs) > 0 {
			inner += " " + strings.Join(s.extraArgs, " ")
		}
		return wrapK3sInstall(inner)
	}
	// Joining server or agent: token + URL required.
	env = append(env,
		fmt.Sprintf("K3S_TOKEN=%s", s.joinFromToken),
		fmt.Sprintf("K3S_URL=https://%s:6443", s.joinFromIP),
	)
	inner += strings.Join(env, " ") + " sh -s -"
	if s.role == "server" {
		inner += " server"
	}
	if len(s.extraArgs) > 0 {
		inner += " " + strings.Join(s.extraArgs, " ")
	}
	return wrapK3sInstall(inner)
}

// wrapK3sInstall wraps the raw `curl | sh` k3s install pipeline with
// (1) a `cloud-init status --wait` gate and (2) `set -o pipefail` so
// curl failures propagate through the pipe instead of being masked by
// `sh -s -` exiting 0. See buildNodeInstallCommand's doc comment for
// the failure mode this covers.
func wrapK3sInstall(inner string) string {
	return "bash -c 'cloud-init status --wait >/dev/null 2>&1 || true; set -o pipefail; " +
		inner + "'"
}

// nodeStateToResource projects the persisted state (and optionally
// the incoming manifest) back into a protocol.Resource with a
// populated status. spec passes through so drift detection can
// compare user intent against what got applied.
func nodeStateToResource(name string, manifest *protocol.Resource, state *nodeState) *protocol.Resource {
	spec := map[string]any{}
	if manifest != nil && manifest.Spec != nil {
		spec = manifest.Spec
	}
	status := map[string]any{
		"installed": state.Installed,
		"vmName":    state.VMName,
		"vmIP":      state.VMIP,
	}
	// nodeToken is what makes joinFrom refs work — expose it so
	// resolver.walkPath can find status.nodeToken on ref
	// resolution.
	if state.NodeToken != "" {
		status["nodeToken"] = state.NodeToken
	}
	if !state.InstalledAt.IsZero() {
		status["installedAt"] = state.InstalledAt.Format(time.RFC3339)
	}
	if state.Kubeconfig != "" {
		status["hasKubeconfig"] = true
	}
	return &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       kindK3sNode,
		Metadata:   protocol.ResourceMetadata{Name: name},
		Spec:       spec,
		Status:     status,
	}
}

// Persistence: one YAML file per node under ~/.openctl/state/k3s-nodes/.

func nodeStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".openctl", "state", "k3s-nodes"), nil
}

func loadNodeState(name string) (*nodeState, error) {
	dir, err := nodeStateDir()
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
	var s nodeState
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	return &s, nil
}

func saveNodeState(s *nodeState) error {
	dir, err := nodeStateDir()
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

func removeNodeState(name string) error {
	dir, err := nodeStateDir()
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(dir, name+".yaml"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// saveK3sNodeKubeconfig persists the fetched kubeconfig at the same
// path the existing Cluster get-kubeconfig action reads from —
// ~/.openctl/k3s/<name>/kubeconfig — so the UI's Kubeconfig button
// works for standalone K3sNode installs too.
func saveK3sNodeKubeconfig(name, contents string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".openctl", "k3s", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "kubeconfig"), []byte(contents), 0o600)
}
