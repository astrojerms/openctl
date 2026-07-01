package k3s

import (
	"strings"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

func TestParseAgentInstallSpec_Full(t *testing.T) {
	m := &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       "AgentInstall",
		Metadata:   protocol.ResourceMetadata{Name: "ai-cp-0"},
		Spec: map[string]any{
			"vmRef":       vmRefResource("vm-a", "192.168.1.10"),
			"clusterName": "dev",
			"ssh":         map[string]any{"user": "ubuntu", "privateKeyPath": "/k"},
		},
	}
	s, err := parseAgentInstallSpec(m)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.vmName != "vm-a" || s.vmIP != "192.168.1.10" || s.clusterName != "dev" || s.sshUser != "ubuntu" || s.sshKeyPath != "/k" {
		t.Errorf("unexpected parse: %+v", s)
	}
}

func TestParseAgentInstallSpec_DefaultsSSHUser(t *testing.T) {
	m := &protocol.Resource{
		Spec: map[string]any{
			"vmRef":       vmRefResource("vm-a", "1.2.3.4"),
			"clusterName": "dev",
			// no ssh.user — should default to "ubuntu"
			"ssh": map[string]any{"privateKeyPath": "/k"},
		},
	}
	s, err := parseAgentInstallSpec(m)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.sshUser != "ubuntu" {
		t.Errorf("expected default sshUser=ubuntu, got %q", s.sshUser)
	}
}

func TestParseAgentInstallSpec_MissingClusterName(t *testing.T) {
	m := &protocol.Resource{
		Spec: map[string]any{
			"vmRef": vmRefResource("vm-a", "1.2.3.4"),
			"ssh":   map[string]any{"privateKeyPath": "/k"},
		},
	}
	_, err := parseAgentInstallSpec(m)
	if err == nil || !strings.Contains(err.Error(), "clusterName") {
		t.Errorf("expected clusterName error, got %v", err)
	}
}

func TestParseAgentInstallSpec_MissingVMIP_ParsesAndDefersToWait(t *testing.T) {
	m := &protocol.Resource{
		Spec: map[string]any{
			"vmRef": map[string]any{
				"metadata": map[string]any{"name": "vm-a"},
				"status":   map[string]any{}, // no ip
			},
			"clusterName": "dev",
			"ssh":         map[string]any{"privateKeyPath": "/k"},
		},
	}
	s, err := parseAgentInstallSpec(m)
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

func TestParseAgentInstallSpec_MissingSSHKey(t *testing.T) {
	m := &protocol.Resource{
		Spec: map[string]any{
			"vmRef":       vmRefResource("vm-a", "1.2.3.4"),
			"clusterName": "dev",
			"ssh":         map[string]any{"user": "ubuntu"},
		},
	}
	_, err := parseAgentInstallSpec(m)
	if err == nil || !strings.Contains(err.Error(), "privateKeyPath") {
		t.Errorf("expected privateKeyPath error, got %v", err)
	}
}

func TestAgentInstallStateRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s := &agentInstallState{
		Name:        "ai-cp-0",
		VMName:      "vm-a",
		VMIP:        "192.168.1.10",
		ClusterName: "dev",
		Arch:        "amd64",
		Init:        "systemd",
		Installed:   true,
	}
	if err := saveAgentInstallState(s); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := loadAgentInstallState("ai-cp-0")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded nil")
	}
	if loaded.ClusterName != s.ClusterName || loaded.VMIP != s.VMIP || loaded.Arch != s.Arch {
		t.Errorf("roundtrip drift:\n saved=%+v\n loaded=%+v", s, loaded)
	}
	if err := removeAgentInstallState("ai-cp-0"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	loaded2, err := loadAgentInstallState("ai-cp-0")
	if err != nil || loaded2 != nil {
		t.Errorf("after remove: got (%+v, %v), want (nil, nil)", loaded2, err)
	}
}

func TestAgentInstallStateToResource(t *testing.T) {
	s := &agentInstallState{
		Name:        "ai-cp-0",
		VMName:      "vm-a",
		VMIP:        "192.168.1.10",
		ClusterName: "dev",
		Arch:        "amd64",
		Init:        "systemd",
		Installed:   true,
	}
	r := agentInstallStateToResource("ai-cp-0", nil, s)
	if r.Kind != kindAgentInstall {
		t.Errorf("kind: %q", r.Kind)
	}
	if r.Status["installed"] != true {
		t.Errorf("status.installed missing: %+v", r.Status)
	}
	if r.Status["clusterName"] != "dev" {
		t.Errorf("status.clusterName missing: %+v", r.Status)
	}
}
