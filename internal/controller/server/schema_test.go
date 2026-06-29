package server

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

func TestSchemaServiceListSchemasReturnsKnownKinds(t *testing.T) {
	h := newSchemaHandler()
	resp, err := h.ListSchemas(context.Background(), &apiv1.ListSchemasRequest{})
	if err != nil {
		t.Fatalf("ListSchemas: %v", err)
	}
	if len(resp.GetSchemas()) < 2 {
		t.Fatalf("got %d schemas, want at least 2 (VM + Cluster)", len(resp.GetSchemas()))
	}
	seen := map[string]bool{}
	for _, s := range resp.GetSchemas() {
		seen[s.GetApiVersion()+"/"+s.GetKind()] = true
	}
	for _, want := range []string{"proxmox.openctl.io/v1/VirtualMachine", "k3s.openctl.io/v1/Cluster"} {
		if !seen[want] {
			t.Errorf("missing schema %q", want)
		}
	}
}

func TestSchemaServiceGetSchemaReturnsCueSource(t *testing.T) {
	h := newSchemaHandler()
	resp, err := h.GetSchema(context.Background(), &apiv1.GetSchemaRequest{
		ApiVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
	})
	if err != nil {
		t.Fatalf("GetSchema: %v", err)
	}
	if resp.GetCueSource() == "" {
		t.Error("CueSource is empty")
	}
	if !strings.Contains(resp.GetCueSource(), "VirtualMachine") {
		t.Errorf("CueSource doesn't mention VirtualMachine — wrong file? Got first 100 chars: %q", firstN(resp.GetCueSource(), 100))
	}
}

func TestSchemaServiceGetSchemaNotFound(t *testing.T) {
	h := newSchemaHandler()
	_, err := h.GetSchema(context.Background(), &apiv1.GetSchemaRequest{
		ApiVersion: "made-up.openctl.io/v1",
		Kind:       "Nope",
	})
	if err == nil {
		t.Error("expected NotFound error for unknown schema")
	}
}

func TestSchemaServiceValidateAcceptsValidResource(t *testing.T) {
	h := newSchemaHandler()
	// Minimum-valid VirtualMachine from the CUE schema's required fields.
	// If this drifts due to schema changes, the test catches it.
	spec, _ := structpb.NewStruct(map[string]any{
		"node":     "pve1",
		"template": map[string]any{"name": "tpl-jammy"},
	})
	resp, err := h.Validate(context.Background(), &apiv1.ValidateRequest{
		Resource: &apiv1.Resource{
			ApiVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   &apiv1.Metadata{Name: "vm-x"},
			Spec:       spec,
		},
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(resp.GetErrors()) > 0 {
		t.Errorf("expected no errors, got: %v", resp.GetErrors())
	}
}

func TestSchemaServiceValidateRejectsMissingName(t *testing.T) {
	h := newSchemaHandler()
	resp, err := h.Validate(context.Background(), &apiv1.ValidateRequest{
		Resource: &apiv1.Resource{
			ApiVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   &apiv1.Metadata{}, // no name
		},
	})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(resp.GetErrors()) == 0 {
		t.Error("expected validation errors for missing metadata.name")
	}
}

func firstN(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n]
}
