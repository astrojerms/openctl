package protocol

import (
	"encoding/json"
	"testing"
)

func TestRequest_JSONRoundTrip(t *testing.T) {
	req := Request{
		Version:      ProtocolVersion,
		Action:       ActionCreate,
		ResourceType: "VirtualMachine",
		ResourceName: "test-vm",
		Manifest: &Resource{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   ResourceMetadata{Name: "test-vm"},
			Spec:       map[string]any{"node": "pve1"},
		},
		Config: ProviderConfig{
			Endpoint:    "https://pve.example.com:8006",
			Node:        "pve1",
			TokenID:     "root@pam!test",
			TokenSecret: "secret",
			Defaults:    map[string]string{"storage": "local-lvm"},
		},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var parsed Request
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if parsed.Version != ProtocolVersion {
		t.Errorf("expected version=%s, got %s", ProtocolVersion, parsed.Version)
	}
	if parsed.Action != ActionCreate {
		t.Errorf("expected action=%s, got %s", ActionCreate, parsed.Action)
	}
	if parsed.ResourceType != "VirtualMachine" {
		t.Errorf("expected resourceType=VirtualMachine, got %s", parsed.ResourceType)
	}
	if parsed.Manifest.Metadata.Name != "test-vm" {
		t.Errorf("expected manifest.metadata.name=test-vm, got %s", parsed.Manifest.Metadata.Name)
	}
	if parsed.Config.Endpoint != "https://pve.example.com:8006" {
		t.Errorf("expected config.endpoint, got %s", parsed.Config.Endpoint)
	}
}

func TestResponse_JSONRoundTrip(t *testing.T) {
	resp := Response{
		Status: StatusSuccess,
		Resource: &Resource{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   ResourceMetadata{Name: "created-vm"},
			Status:     map[string]any{"state": "running", "vmid": 100},
		},
		Message: "VM created successfully",
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var parsed Response
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if parsed.Status != StatusSuccess {
		t.Errorf("expected status=%s, got %s", StatusSuccess, parsed.Status)
	}
	if parsed.Resource.Metadata.Name != "created-vm" {
		t.Errorf("expected resource.metadata.name=created-vm, got %s", parsed.Resource.Metadata.Name)
	}
	if parsed.Message != "VM created successfully" {
		t.Errorf("expected message, got %s", parsed.Message)
	}
}

func TestResponse_Error(t *testing.T) {
	resp := Response{
		Status: StatusError,
		Error: &Error{
			Code:    ErrorCodeNotFound,
			Message: "VM not found",
			Details: "VM 'test' does not exist",
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var parsed Response
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if parsed.Status != StatusError {
		t.Errorf("expected status=%s, got %s", StatusError, parsed.Status)
	}
	if parsed.Error.Code != ErrorCodeNotFound {
		t.Errorf("expected error.code=%s, got %s", ErrorCodeNotFound, parsed.Error.Code)
	}
	if parsed.Error.Message != "VM not found" {
		t.Errorf("expected error.message, got %s", parsed.Error.Message)
	}
}

func TestResponse_ListResources(t *testing.T) {
	resp := Response{
		Status: StatusSuccess,
		Resources: []*Resource{
			{
				APIVersion: "proxmox.openctl.io/v1",
				Kind:       "VirtualMachine",
				Metadata:   ResourceMetadata{Name: "vm-1"},
			},
			{
				APIVersion: "proxmox.openctl.io/v1",
				Kind:       "VirtualMachine",
				Metadata:   ResourceMetadata{Name: "vm-2"},
			},
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var parsed Response
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(parsed.Resources) != 2 {
		t.Errorf("expected 2 resources, got %d", len(parsed.Resources))
	}
}

func TestCapabilities_JSONRoundTrip(t *testing.T) {
	caps := Capabilities{
		ProviderName:    "proxmox",
		ProtocolVersion: ProtocolVersion,
		Resources: []ResourceDefinition{
			{
				Kind:    "VirtualMachine",
				Plural:  "vms",
				Actions: []string{"get", "list", "create", "delete", "apply"},
			},
			{
				Kind:    "Template",
				Plural:  "templates",
				Actions: []string{"get", "list"},
			},
		},
	}

	data, err := json.Marshal(caps)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var parsed Capabilities
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if parsed.ProviderName != "proxmox" {
		t.Errorf("expected providerName=proxmox, got %s", parsed.ProviderName)
	}
	if parsed.ProtocolVersion != ProtocolVersion {
		t.Errorf("expected protocolVersion=%s, got %s", ProtocolVersion, parsed.ProtocolVersion)
	}
	if len(parsed.Resources) != 2 {
		t.Errorf("expected 2 resources, got %d", len(parsed.Resources))
	}

	vmResource := parsed.Resources[0]
	if vmResource.Kind != "VirtualMachine" {
		t.Errorf("expected kind=VirtualMachine, got %s", vmResource.Kind)
	}
	if vmResource.Plural != "vms" {
		t.Errorf("expected plural=vms, got %s", vmResource.Plural)
	}
	if len(vmResource.Actions) != 5 {
		t.Errorf("expected 5 actions, got %d", len(vmResource.Actions))
	}
}

func TestResource_WithLabelsAndAnnotations(t *testing.T) {
	resource := Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata: ResourceMetadata{
			Name:      "labeled-vm",
			Namespace: "production",
			Labels: map[string]string{
				"app":  "web",
				"tier": "frontend",
			},
			Annotations: map[string]string{
				"description": "Production web server",
			},
			UID:       "abc-123",
			CreatedAt: "2024-01-01T00:00:00Z",
		},
		Spec:   map[string]any{"node": "pve1"},
		Status: map[string]any{"state": "running"},
	}

	data, err := json.Marshal(resource)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var parsed Resource
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if parsed.Metadata.Labels["app"] != "web" {
		t.Errorf("expected label app=web, got %s", parsed.Metadata.Labels["app"])
	}
	if parsed.Metadata.Annotations["description"] != "Production web server" {
		t.Errorf("expected annotation description, got %s", parsed.Metadata.Annotations["description"])
	}
	if parsed.Metadata.UID != "abc-123" {
		t.Errorf("expected uid=abc-123, got %s", parsed.Metadata.UID)
	}
}

func TestConstants(t *testing.T) {
	// Verify action constants
	if ActionGet != "get" {
		t.Errorf("ActionGet should be 'get'")
	}
	if ActionList != "list" {
		t.Errorf("ActionList should be 'list'")
	}
	if ActionCreate != "create" {
		t.Errorf("ActionCreate should be 'create'")
	}
	if ActionDelete != "delete" {
		t.Errorf("ActionDelete should be 'delete'")
	}
	if ActionApply != "apply" {
		t.Errorf("ActionApply should be 'apply'")
	}

	// Verify status constants
	if StatusSuccess != "success" {
		t.Errorf("StatusSuccess should be 'success'")
	}
	if StatusError != "error" {
		t.Errorf("StatusError should be 'error'")
	}

	// Verify error code constants
	if ErrorCodeNotFound != "NOT_FOUND" {
		t.Errorf("ErrorCodeNotFound should be 'NOT_FOUND'")
	}
	if ErrorCodeAlreadyExists != "ALREADY_EXISTS" {
		t.Errorf("ErrorCodeAlreadyExists should be 'ALREADY_EXISTS'")
	}
	if ErrorCodeInvalidRequest != "INVALID_REQUEST" {
		t.Errorf("ErrorCodeInvalidRequest should be 'INVALID_REQUEST'")
	}
	if ErrorCodeUnauthorized != "UNAUTHORIZED" {
		t.Errorf("ErrorCodeUnauthorized should be 'UNAUTHORIZED'")
	}
	if ErrorCodeInternal != "INTERNAL" {
		t.Errorf("ErrorCodeInternal should be 'INTERNAL'")
	}

	// Verify protocol version
	if ProtocolVersion != "1.0" {
		t.Errorf("ProtocolVersion should be '1.0'")
	}
}
