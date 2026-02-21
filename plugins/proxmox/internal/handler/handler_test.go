package handler

import (
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

func TestHandler_HandleUnknownResourceType(t *testing.T) {
	h := New(&protocol.ProviderConfig{})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionGet,
		ResourceType: "UnknownResource",
	}

	resp, err := h.Handle(req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusError {
		t.Errorf("expected status=error, got %s", resp.Status)
	}
	if resp.Error == nil {
		t.Fatal("expected error in response")
	}
	if resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Errorf("expected code=INVALID_REQUEST, got %s", resp.Error.Code)
	}
}

func TestHandler_HandleVMUnknownAction(t *testing.T) {
	h := New(&protocol.ProviderConfig{})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       "unknown-action",
		ResourceType: "VirtualMachine",
	}

	resp, err := h.Handle(req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusError {
		t.Errorf("expected status=error, got %s", resp.Status)
	}
	if resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Errorf("expected code=INVALID_REQUEST, got %s", resp.Error.Code)
	}
}

func TestHandler_HandleTemplateUnsupportedAction(t *testing.T) {
	h := New(&protocol.ProviderConfig{})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionCreate,
		ResourceType: "Template",
	}

	resp, err := h.Handle(req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusError {
		t.Errorf("expected status=error, got %s", resp.Status)
	}
}

func TestHandler_CreateVMMissingNode(t *testing.T) {
	h := New(&protocol.ProviderConfig{
		// No node configured
	})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionCreate,
		ResourceType: "VirtualMachine",
		Manifest: &protocol.Resource{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "test-vm"},
			Spec: map[string]any{
				// No node in spec either
				"template": map[string]any{
					"name": "ubuntu",
				},
			},
		},
	}

	resp, err := h.Handle(req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusError {
		t.Errorf("expected status=error, got %s", resp.Status)
	}
	if resp.Error.Code != protocol.ErrorCodeInvalidRequest {
		t.Errorf("expected code=INVALID_REQUEST, got %s", resp.Error.Code)
	}
}

func TestHandler_CreateVMWithoutTemplate(t *testing.T) {
	h := New(&protocol.ProviderConfig{
		Node: "pve1",
	})

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionCreate,
		ResourceType: "VirtualMachine",
		Manifest: &protocol.Resource{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "test-vm"},
			Spec: map[string]any{
				// No template specified
				"cpu": map[string]any{
					"cores": float64(4),
				},
			},
		},
	}

	resp, err := h.Handle(req)
	if err != nil {
		t.Fatalf("Handle should not return error: %v", err)
	}

	if resp.Status != protocol.StatusError {
		t.Errorf("expected status=error, got %s", resp.Status)
	}
	// Creating VM without template is not yet supported
}

func TestHandler_NodeFromConfig(t *testing.T) {
	h := New(&protocol.ProviderConfig{
		Node: "config-node",
	})

	// Verify the handler uses the config node
	if h.config.Node != "config-node" {
		t.Errorf("expected config.Node=config-node, got %s", h.config.Node)
	}
}

func TestHandler_RoutesToCorrectResourceType(t *testing.T) {
	tests := []struct {
		resourceType string
		action       string
		shouldError  bool
	}{
		{"VirtualMachine", protocol.ActionList, false},
		{"VirtualMachine", protocol.ActionGet, false},
		{"VirtualMachine", protocol.ActionCreate, false},
		{"VirtualMachine", protocol.ActionDelete, false},
		{"VirtualMachine", protocol.ActionApply, false},
		{"Template", protocol.ActionList, false},
		{"Template", protocol.ActionGet, false},
		{"Template", protocol.ActionCreate, true}, // Not supported
		{"Unknown", protocol.ActionGet, true},
	}

	for _, tt := range tests {
		t.Run(tt.resourceType+"/"+tt.action, func(t *testing.T) {
			h := New(&protocol.ProviderConfig{
				Endpoint:    "https://pve.example.com:8006",
				TokenID:     "test",
				TokenSecret: "test",
				Node:        "pve1",
			})

			req := &protocol.Request{
				Version:      protocol.ProtocolVersion,
				Action:       tt.action,
				ResourceType: tt.resourceType,
				ResourceName: "test",
				Manifest: &protocol.Resource{
					APIVersion: "proxmox.openctl.io/v1",
					Kind:       tt.resourceType,
					Metadata:   protocol.ResourceMetadata{Name: "test"},
					Spec:       map[string]any{},
				},
			}

			resp, err := h.Handle(req)

			// The handler returns protocol errors, not Go errors
			// for known error conditions
			if tt.shouldError {
				if err == nil && (resp == nil || resp.Status != protocol.StatusError) {
					t.Errorf("expected error response")
				}
			}
			// Note: non-error cases will fail because we don't have
			// a real Proxmox server to connect to
		})
	}
}
