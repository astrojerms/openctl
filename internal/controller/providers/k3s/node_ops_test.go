package k3s

import (
	"fmt"
	"strings"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

// Helpers to build the post-ref-resolution manifest shape the
// provider consumes. Real specs come in with the vmRef as a
// whole-resource map (the resolver flattened the $ref into the
// referenced resource).
func vmRefResource(name, ip string) map[string]any {
	return map[string]any{
		"apiVersion": "proxmox.openctl.io/v1",
		"kind":       "VirtualMachine",
		"metadata":   map[string]any{"name": name},
		"status":     map[string]any{"ip": ip},
	}
}

func TestParseK3sNodeSpec_FirstServer(t *testing.T) {
	m := &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       "K3sNode",
		Metadata:   protocol.ResourceMetadata{Name: "cp-0"},
		Spec: map[string]any{
			"vmRef": vmRefResource("vm-a", "192.168.1.10"),
			"role":  "server",
			"ssh":   map[string]any{"user": "ubuntu", "privateKeyPath": "/root/.ssh/id_ed25519"},
		},
	}
	s, err := parseK3sNodeSpec(m)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.vmIP != "192.168.1.10" || s.role != "server" || s.joinFromToken != "" {
		t.Errorf("unexpected parse: %+v", s)
	}
	// First server: install command has no K3S_TOKEN / K3S_URL.
	cmd := buildNodeInstallCommand(s)
	if strings.Contains(cmd, "K3S_TOKEN") || strings.Contains(cmd, "K3S_URL") {
		t.Errorf("first-server command should not carry join env: %s", cmd)
	}
	if !strings.Contains(cmd, "sh -s -") {
		t.Errorf("expected server install shell one-liner: %s", cmd)
	}
}

func TestParseK3sNodeSpec_JoiningServer(t *testing.T) {
	m := &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       "K3sNode",
		Metadata:   protocol.ResourceMetadata{Name: "cp-1"},
		Spec: map[string]any{
			"vmRef":       vmRefResource("vm-b", "192.168.1.11"),
			"role":        "server",
			"joinFrom":    "K10::server:token",
			"joinURLFrom": "192.168.1.10",
			"ssh":         map[string]any{"privateKeyPath": "/root/.ssh/id_ed25519"},
		},
	}
	s, err := parseK3sNodeSpec(m)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.joinFromToken != "K10::server:token" || s.joinFromIP != "192.168.1.10" {
		t.Errorf("join fields not wired: %+v", s)
	}
	cmd := buildNodeInstallCommand(s)
	if !strings.Contains(cmd, "K3S_TOKEN=K10::server:token") {
		t.Errorf("joining-server command missing token: %s", cmd)
	}
	if !strings.Contains(cmd, "K3S_URL=https://192.168.1.10:6443") {
		t.Errorf("joining-server command missing URL: %s", cmd)
	}
	if !strings.Contains(cmd, "sh -s - server") {
		t.Errorf("joining-server command should include 'sh -s - server' subcommand: %s", cmd)
	}
}

func TestParseK3sNodeSpec_Agent(t *testing.T) {
	m := &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       "K3sNode",
		Metadata:   protocol.ResourceMetadata{Name: "worker-0"},
		Spec: map[string]any{
			"vmRef":       vmRefResource("vm-c", "192.168.1.20"),
			"role":        "agent",
			"joinFrom":    "K10::agent:token",
			"joinURLFrom": "192.168.1.10",
			"extraArgs":   []any{"--disable=traefik"},
			"ssh":         map[string]any{"privateKeyPath": "/root/.ssh/id_ed25519"},
		},
	}
	s, err := parseK3sNodeSpec(m)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cmd := buildNodeInstallCommand(s)
	if strings.Contains(cmd, "sh -s - server") {
		t.Errorf("agent command should NOT include ' sh -s - server' subcommand: %s", cmd)
	}
	if !strings.Contains(cmd, "K3S_TOKEN=K10::agent:token") {
		t.Errorf("agent command missing token: %s", cmd)
	}
	if strings.Contains(cmd, "--disable=traefik") {
		t.Errorf("agent command should not include server-only extra args: %s", cmd)
	}
}

// TestIsConnectionDropError covers the recovery predicate that
// distinguishes "SSH stream died mid-install" (the k3s installer
// restarts iptables, which kills the transport) from "remote command
// exited non-zero" (a genuine install failure that must propagate).
func TestIsConnectionDropError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"nil-ish", errStr(""), false},
		{"real command failure", errStr("Process exited with status 1: something broke"), false},
		{"remote wait no exit", errStr("wait: remote command exited without exit status or exit signal"), true},
		{"connection reset", errStr("read tcp: connection reset by peer"), true},
		{"broken pipe", errStr("write: broken pipe"), true},
		{"closed connection", errStr("use of closed network connection"), true},
		{"eof", errStr("unexpected EOF"), true},
		// Session/channel open racing k3s's post-start network churn.
		{"channel open packet", errStr("ssh: unexpected packet in response to channel open"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isConnectionDropError(tc.err); got != tc.want {
				t.Errorf("isConnectionDropError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

type stringError string

func (s stringError) Error() string { return string(s) }
func errStr(s string) error {
	if s == "" {
		return nil
	}
	return stringError(s)
}

// TestBuildNodeInstallCommand_HardeningWrapper asserts every install
// command carries the cloud-init wait + pipefail hardening. Regression
// guard for the homelab validation bug: the k3s installer was silently
// killed mid-run because cloud-init hadn't finished, and `curl | sh`
// under plain POSIX sh reported exit 0 anyway.
func TestBuildNodeInstallCommand_HardeningWrapper(t *testing.T) {
	cases := []struct {
		name string
		spec *k3sNodeSpec
	}{
		{"first-server", &k3sNodeSpec{role: "server"}},
		{"joining-server", &k3sNodeSpec{role: "server", joinFromToken: "t", joinFromIP: "1.1.1.1"}},
		{"agent", &k3sNodeSpec{role: "agent", joinFromToken: "t", joinFromIP: "1.1.1.1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := buildNodeInstallCommand(tc.spec)
			if !strings.Contains(cmd, "cloud-init status --wait") {
				t.Errorf("missing cloud-init wait: %s", cmd)
			}
			// The wait must be bounded (`timeout N`) so a template that
			// wedges cloud-init in package_update_upgrade_install can't
			// hang the whole apply. See cloudInitWaitSeconds.
			if !strings.Contains(cmd, fmt.Sprintf("timeout %d cloud-init status --wait", cloudInitWaitSeconds)) {
				t.Errorf("cloud-init wait is not bounded by timeout: %s", cmd)
			}
			if !strings.Contains(cmd, "set -o pipefail") {
				t.Errorf("missing pipefail: %s", cmd)
			}
			if !strings.HasPrefix(cmd, "bash -c '") {
				t.Errorf("expected bash -c wrapper, got: %s", cmd)
			}
			if !strings.HasSuffix(cmd, "'") {
				t.Errorf("expected trailing single-quote, got: %s", cmd)
			}
		})
	}
}

// TestBuildNodeInstallCommand_ClusterInit asserts `--cluster-init` is
// emitted only for the first server when clusterInit is set — the HA
// bootstrap that enables embedded etcd so additional CPs can join.
func TestBuildNodeInstallCommand_ClusterInit(t *testing.T) {
	cases := []struct {
		name string
		spec *k3sNodeSpec
		want bool
	}{
		{"first-server-ha", &k3sNodeSpec{role: "server", clusterInit: true}, true},
		{"first-server-single-cp", &k3sNodeSpec{role: "server"}, false},
		// Joining servers and agents must never get --cluster-init even
		// if the flag leaked in: they join an existing etcd, not init one.
		{"joining-server", &k3sNodeSpec{role: "server", joinFromToken: "t", joinFromIP: "1.1.1.1", clusterInit: true}, false},
		{"agent", &k3sNodeSpec{role: "agent", joinFromToken: "t", joinFromIP: "1.1.1.1", clusterInit: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := buildNodeInstallCommand(tc.spec)
			got := strings.Contains(cmd, "--cluster-init")
			if got != tc.want {
				t.Errorf("--cluster-init present=%v, want %v: %s", got, tc.want, cmd)
			}
		})
	}
}

func TestParseK3sNodeSpec_StaticVMIPOverridesResolvedRef(t *testing.T) {
	// Plan-emitted K3sNode for a static-IP cluster carries
	// `spec.vmIP` populated from AllocateIPs. That should take
	// precedence over anything the ref resolver put in
	// vmRef.status.ip (which is empty when QGA hasn't reported).
	m := &protocol.Resource{
		Spec: map[string]any{
			"vmRef": map[string]any{
				"metadata": map[string]any{"name": "vm-a"},
				"status":   map[string]any{}, // QGA hasn't reported
			},
			"vmIP": "10.0.0.42", // static-IP from Plan
			"role": "server",
			"ssh":  map[string]any{"privateKeyPath": "/k"},
		},
	}
	s, err := parseK3sNodeSpec(m)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.vmIP != "10.0.0.42" {
		t.Errorf("static-IP override not honored: got %q, want 10.0.0.42", s.vmIP)
	}
}

func TestParseK3sNodeSpec_StaticVMIPWinsOverStatusIP(t *testing.T) {
	// Both `spec.vmIP` and `vmRef.status.ip` present — the
	// explicit `spec.vmIP` wins, since Plan-time knowledge is
	// authoritative when it disagrees with runtime observation.
	m := &protocol.Resource{
		Spec: map[string]any{
			"vmRef": vmRefResource("vm-a", "10.0.0.1"),
			"vmIP":  "10.0.0.42",
			"role":  "server",
			"ssh":   map[string]any{"privateKeyPath": "/k"},
		},
	}
	s, err := parseK3sNodeSpec(m)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.vmIP != "10.0.0.42" {
		t.Errorf("spec.vmIP should win over vmRef.status.ip: got %q", s.vmIP)
	}
}

func TestParseK3sNodeSpec_MissingVMIP_ParsesAndDefersToWait(t *testing.T) {
	// VMs that haven't reported their IP yet are OK to parse —
	// applyK3sNode polls status.ip via the k3s Provider's VMApplier
	// before running the install. Verify parse returns vmIP="" so
	// the wait path takes over.
	m := &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       "K3sNode",
		Metadata:   protocol.ResourceMetadata{Name: "cp-0"},
		Spec: map[string]any{
			"vmRef": map[string]any{
				"metadata": map[string]any{"name": "vm-a"},
				"status":   map[string]any{}, // no ip
			},
			"role": "server",
			"ssh":  map[string]any{"privateKeyPath": "/root/.ssh/id_ed25519"},
		},
	}
	s, err := parseK3sNodeSpec(m)
	if err != nil {
		t.Fatalf("parse should succeed with empty IP, got: %v", err)
	}
	if s.vmIP != "" {
		t.Errorf("expected empty vmIP, got %q", s.vmIP)
	}
	if s.vmName != "vm-a" {
		t.Errorf("expected vmName=vm-a, got %q", s.vmName)
	}
}

func TestParseK3sNodeSpec_AgentRequiresJoinFields(t *testing.T) {
	m := &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       "K3sNode",
		Metadata:   protocol.ResourceMetadata{Name: "worker-0"},
		Spec: map[string]any{
			"vmRef": vmRefResource("vm-c", "192.168.1.20"),
			"role":  "agent",
			"ssh":   map[string]any{"privateKeyPath": "/k"},
			// no joinFrom / joinURLFrom
		},
	}
	_, err := parseK3sNodeSpec(m)
	if err == nil {
		t.Fatal("expected error for agent without join fields, got nil")
	}
}

func TestParseK3sNodeSpec_ExtraArgsFlowThrough(t *testing.T) {
	m := &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       "K3sNode",
		Metadata:   protocol.ResourceMetadata{Name: "cp-0"},
		Spec: map[string]any{
			"vmRef":     vmRefResource("vm-a", "1.2.3.4"),
			"role":      "server",
			"extraArgs": []any{"--disable=traefik", "--cluster-cidr=10.0.0.0/16"},
			"ssh":       map[string]any{"privateKeyPath": "/k"},
		},
	}
	s, err := parseK3sNodeSpec(m)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cmd := buildNodeInstallCommand(s)
	if !strings.Contains(cmd, "--disable=traefik") || !strings.Contains(cmd, "--cluster-cidr=10.0.0.0/16") {
		t.Errorf("extraArgs did not flow into command: %s", cmd)
	}
}

func TestNodeStateRoundTrip(t *testing.T) {
	// Redirect state dir to a temp dir so tests don't touch the user's
	// real ~/.openctl. The nodeStateDir helper reads from
	// os.UserHomeDir, which respects HOME.
	t.Setenv("HOME", t.TempDir())

	s := &nodeState{
		Name:      "cp-0",
		VMName:    "vm-a",
		VMIP:      "192.168.1.10",
		Role:      "server",
		Installed: true,
		NodeToken: "K10::secret",
	}
	if err := saveNodeState(s); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := loadNodeState("cp-0")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded nil")
	}
	if loaded.NodeToken != s.NodeToken || loaded.VMIP != s.VMIP {
		t.Errorf("roundtrip drift:\n saved=%+v\n loaded=%+v", s, loaded)
	}
	if err := removeNodeState("cp-0"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	loaded2, err := loadNodeState("cp-0")
	if err != nil || loaded2 != nil {
		t.Errorf("after remove: got (%+v, %v), want (nil, nil)", loaded2, err)
	}
}
