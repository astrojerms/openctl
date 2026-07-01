package k3s

import (
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
	if !strings.HasSuffix(strings.TrimSpace(cmd), "server") {
		t.Errorf("joining-server command should end with 'server' subcommand: %s", cmd)
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
			"ssh":         map[string]any{"privateKeyPath": "/root/.ssh/id_ed25519"},
		},
	}
	s, err := parseK3sNodeSpec(m)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cmd := buildNodeInstallCommand(s)
	if strings.HasSuffix(strings.TrimSpace(cmd), "server") {
		t.Errorf("agent command should NOT end with 'server': %s", cmd)
	}
	if !strings.Contains(cmd, "K3S_TOKEN=K10::agent:token") {
		t.Errorf("agent command missing token: %s", cmd)
	}
}

func TestParseK3sNodeSpec_MissingVMIP(t *testing.T) {
	// VMs that haven't reported their IP yet can't have K3sNode
	// installed on them — the parser should fail loud so the op
	// retries after the VM is ready.
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
	_, err := parseK3sNodeSpec(m)
	if err == nil || !strings.Contains(err.Error(), "status.ip") {
		t.Errorf("expected status.ip error, got %v", err)
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
