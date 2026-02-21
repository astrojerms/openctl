package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/openctl/openctl/pkg/protocol"
)

func TestFormatter_FormatJSON(t *testing.T) {
	var buf bytes.Buffer
	formatter := NewFormatter(FormatJSON, &buf)

	resources := []*protocol.Resource{
		{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "vm-1"},
			Spec:       map[string]any{"node": "pve1"},
			Status:     map[string]any{"state": "running"},
		},
		{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "vm-2"},
			Spec:       map[string]any{"node": "pve2"},
			Status:     map[string]any{"state": "stopped"},
		},
	}

	if err := formatter.FormatResources(resources); err != nil {
		t.Fatalf("FormatResources failed: %v", err)
	}

	var parsed []*protocol.Resource
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}

	if len(parsed) != 2 {
		t.Errorf("expected 2 resources, got %d", len(parsed))
	}

	if parsed[0].Metadata.Name != "vm-1" {
		t.Errorf("expected name=vm-1, got %s", parsed[0].Metadata.Name)
	}
}

func TestFormatter_FormatJSONSingle(t *testing.T) {
	var buf bytes.Buffer
	formatter := NewFormatter(FormatJSON, &buf)

	resource := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata:   protocol.ResourceMetadata{Name: "single-vm"},
		Spec:       map[string]any{"node": "pve1"},
	}

	if err := formatter.FormatResource(resource); err != nil {
		t.Fatalf("FormatResource failed: %v", err)
	}

	var parsed protocol.Resource
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}

	if parsed.Metadata.Name != "single-vm" {
		t.Errorf("expected name=single-vm, got %s", parsed.Metadata.Name)
	}
}

func TestFormatter_FormatYAML(t *testing.T) {
	var buf bytes.Buffer
	formatter := NewFormatter(FormatYAML, &buf)

	resources := []*protocol.Resource{
		{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "yaml-vm"},
			Spec:       map[string]any{"node": "pve1"},
		},
	}

	if err := formatter.FormatResources(resources); err != nil {
		t.Fatalf("FormatResources failed: %v", err)
	}

	var parsed protocol.Resource
	if err := yaml.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("failed to parse YAML output: %v", err)
	}

	if parsed.Metadata.Name != "yaml-vm" {
		t.Errorf("expected name=yaml-vm, got %s", parsed.Metadata.Name)
	}
}

func TestFormatter_FormatYAMLMultiple(t *testing.T) {
	var buf bytes.Buffer
	formatter := NewFormatter(FormatYAML, &buf)

	resources := []*protocol.Resource{
		{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "vm-1"},
		},
		{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "vm-2"},
		},
	}

	if err := formatter.FormatResources(resources); err != nil {
		t.Fatalf("FormatResources failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "---") {
		t.Error("expected YAML document separator")
	}
	if !strings.Contains(output, "name: vm-1") {
		t.Error("expected vm-1 in output")
	}
	if !strings.Contains(output, "name: vm-2") {
		t.Error("expected vm-2 in output")
	}
}

func TestFormatter_FormatTable(t *testing.T) {
	var buf bytes.Buffer
	formatter := NewFormatter(FormatTable, &buf)

	resources := []*protocol.Resource{
		{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "web-01"},
			Spec: map[string]any{
				"cpu":    map[string]any{"cores": 4},
				"memory": map[string]any{"size": 8192},
			},
			Status: map[string]any{"state": "running"},
		},
		{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "db-01"},
			Spec: map[string]any{
				"cpu":    map[string]any{"cores": 8},
				"memory": map[string]any{"size": 16384},
			},
			Status: map[string]any{"state": "stopped"},
		},
	}

	if err := formatter.FormatResources(resources); err != nil {
		t.Fatalf("FormatResources failed: %v", err)
	}

	output := buf.String()

	// Check headers
	if !strings.Contains(output, "NAME") {
		t.Error("expected NAME header")
	}
	if !strings.Contains(output, "STATUS") {
		t.Error("expected STATUS header")
	}
	if !strings.Contains(output, "CPU") {
		t.Error("expected CPU header")
	}
	if !strings.Contains(output, "MEMORY") {
		t.Error("expected MEMORY header")
	}

	// Check data
	if !strings.Contains(output, "web-01") {
		t.Error("expected web-01 in output")
	}
	if !strings.Contains(output, "db-01") {
		t.Error("expected db-01 in output")
	}
	if !strings.Contains(output, "running") {
		t.Error("expected running status")
	}
	if !strings.Contains(output, "stopped") {
		t.Error("expected stopped status")
	}
}

func TestFormatter_FormatTableWide(t *testing.T) {
	var buf bytes.Buffer
	formatter := NewFormatter(FormatWide, &buf)

	resources := []*protocol.Resource{
		{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "test-vm"},
			Spec: map[string]any{
				"node": "pve1",
			},
			Status: map[string]any{
				"state": "running",
				"vmid":  100,
			},
		},
	}

	if err := formatter.FormatResources(resources); err != nil {
		t.Fatalf("FormatResources failed: %v", err)
	}

	output := buf.String()

	// Wide format should include NODE and VMID
	if !strings.Contains(output, "NODE") {
		t.Error("expected NODE header in wide format")
	}
	if !strings.Contains(output, "VMID") {
		t.Error("expected VMID header in wide format")
	}
	if !strings.Contains(output, "pve1") {
		t.Error("expected pve1 node value")
	}
}

func TestFormatter_FormatTableEmpty(t *testing.T) {
	var buf bytes.Buffer
	formatter := NewFormatter(FormatTable, &buf)

	if err := formatter.FormatResources([]*protocol.Resource{}); err != nil {
		t.Fatalf("FormatResources failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "No resources found") {
		t.Error("expected 'No resources found' message")
	}
}

func TestFormatter_DefaultFormat(t *testing.T) {
	var buf bytes.Buffer
	formatter := NewFormatter("", &buf)

	resources := []*protocol.Resource{
		{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "default-vm"},
			Status:     map[string]any{"state": "running"},
		},
	}

	if err := formatter.FormatResources(resources); err != nil {
		t.Fatalf("FormatResources failed: %v", err)
	}

	output := buf.String()
	// Default should be table format
	if !strings.Contains(output, "NAME") {
		t.Error("expected table format as default")
	}
}

func TestPrintMessage(t *testing.T) {
	var buf bytes.Buffer
	PrintMessage(&buf, "Test message")

	if buf.String() != "Test message\n" {
		t.Errorf("expected 'Test message\\n', got %q", buf.String())
	}
}

func TestPrintError(t *testing.T) {
	var buf bytes.Buffer
	PrintError(&buf, &testError{"something went wrong"})

	expected := "Error: something went wrong\n"
	if buf.String() != expected {
		t.Errorf("expected %q, got %q", expected, buf.String())
	}
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}
