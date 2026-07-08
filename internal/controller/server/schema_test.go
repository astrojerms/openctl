package server

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/openctl/openctl/internal/controller/providers"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
)

func TestSchemaServiceListSchemasReturnsKnownKinds(t *testing.T) {
	h := newSchemaHandler(nil)
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
	h := newSchemaHandler(nil)
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
	h := newSchemaHandler(nil)
	_, err := h.GetSchema(context.Background(), &apiv1.GetSchemaRequest{
		ApiVersion: "made-up.openctl.io/v1",
		Kind:       "Nope",
	})
	if err == nil {
		t.Error("expected NotFound error for unknown schema")
	}
}

func TestSchemaServiceValidateAcceptsValidResource(t *testing.T) {
	h := newSchemaHandler(nil)
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
	h := newSchemaHandler(nil)
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

// advFakeProvider is a minimal Provider that declares one of its kinds as a
// composite-child via AdvancedKindDescriber. It uses the "k3s" name + built-in
// kinds so its declaration joins against real entries in schema.Registry(),
// exercising the ListSchemas stamping end-to-end without the real k3s provider.
type advFakeProvider struct{}

func (advFakeProvider) Name() string    { return "k3s" }
func (advFakeProvider) Kinds() []string { return []string{"Cluster", "K3sNode"} }
func (advFakeProvider) Apply(_ context.Context, m *protocol.Resource) (*protocol.Resource, error) {
	return m, nil
}
func (advFakeProvider) Get(context.Context, string, string) (*protocol.Resource, error) {
	return nil, nil
}
func (advFakeProvider) List(context.Context, string) ([]*protocol.Resource, error) { return nil, nil }
func (advFakeProvider) Delete(context.Context, string, string) error               { return nil }
func (advFakeProvider) AdvancedKinds() []providers.AdvancedKind {
	return []providers.AdvancedKind{
		{Kind: "K3sNode", OwnerKind: "Cluster", Note: "produced by a Cluster"},
	}
}

func TestSchemaServiceListSchemasStampsAdvancedFromRegistry(t *testing.T) {
	reg := providers.NewRegistry()
	reg.Register(advFakeProvider{})
	h := newSchemaHandler(reg)

	resp, err := h.ListSchemas(context.Background(), &apiv1.ListSchemasRequest{})
	if err != nil {
		t.Fatalf("ListSchemas: %v", err)
	}

	byKey := map[string]*apiv1.SchemaInfo{}
	for _, s := range resp.GetSchemas() {
		byKey[s.GetApiVersion()+"/"+s.GetKind()] = s
	}

	// The declared composite-child is flagged with owner + note.
	node := byKey["k3s.openctl.io/v1/K3sNode"]
	if node == nil {
		t.Fatal("missing k3s.openctl.io/v1/K3sNode in ListSchemas")
	}
	if !node.GetAdvanced() {
		t.Error("K3sNode should be flagged advanced")
	}
	if node.GetOwnerKind() != "Cluster" {
		t.Errorf("K3sNode ownerKind = %q, want Cluster", node.GetOwnerKind())
	}
	if node.GetAdvancedNote() == "" {
		t.Error("K3sNode advancedNote should be populated")
	}

	// The owning composite itself is NOT advanced.
	if cl := byKey["k3s.openctl.io/v1/Cluster"]; cl == nil || cl.GetAdvanced() {
		t.Errorf("Cluster should not be advanced (got %+v)", cl)
	}

	// A kind from a provider that declares nothing stays un-flagged — this is
	// how a k3s Cluster's proxmox VirtualMachine children avoid being flagged.
	if vm := byKey["proxmox.openctl.io/v1/VirtualMachine"]; vm == nil || vm.GetAdvanced() {
		t.Errorf("VirtualMachine should not be advanced (got %+v)", vm)
	}
}

func TestSchemaServiceListSchemasNilRegistryFlagsNothing(t *testing.T) {
	h := newSchemaHandler(nil)
	resp, err := h.ListSchemas(context.Background(), &apiv1.ListSchemasRequest{})
	if err != nil {
		t.Fatalf("ListSchemas: %v", err)
	}
	for _, s := range resp.GetSchemas() {
		if s.GetAdvanced() {
			t.Errorf("kind %s/%s flagged advanced with a nil registry", s.GetApiVersion(), s.GetKind())
		}
	}
}

func firstN(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n]
}
