package server

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/providers"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
)

// Preserve the application's target.host shape while using an atomic test
// provider, so inspection itself cannot mask drift with provider projections.
type referenceDriftProvider struct{ *fakeProvider }

func (p *referenceDriftProvider) List(context.Context, string) ([]*protocol.Resource, error) {
	return []*protocol.Resource{p.getReturn}, nil
}

func referenceDriftManifest() *protocol.Resource {
	return &protocol.Resource{
		APIVersion: "fake.openctl.io/v1", Kind: "FakeKind",
		Metadata: protocol.ResourceMetadata{Name: "application"},
		Spec: map[string]any{"target": map[string]any{"host": map[string]any{"$ref": map[string]any{
			"apiVersion": "proxmox.openctl.io/v1", "kind": "VirtualMachine", "name": "guest", "field": "status.ip",
		}}}},
	}
}

func referenceDriftGuest(ip string) *protocol.Resource {
	return &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine",
		Metadata: protocol.ResourceMetadata{Name: "guest"},
		Status:   map[string]any{"ip": ip},
	}
}

func referenceDriftFixture(t *testing.T, desired *protocol.Resource, guest *protocol.Resource, guestErr error) (apiv1.ResourceServiceClient, apiv1.InventoryServiceClient, *manifests.Store, *referenceDriftProvider, *referenceLookupProvider) {
	t.Helper()
	app := &referenceDriftProvider{&fakeProvider{getReturn: &protocol.Resource{
		APIVersion: desired.APIVersion, Kind: desired.Kind, Metadata: desired.Metadata,
		Spec: map[string]any{"target": map[string]any{"host": "192.168.11.125"}},
	}}}
	lookup := &referenceLookupProvider{&fakeProvider{getReturn: guest, getErr: guestErr}}
	reg := providers.NewRegistry()
	reg.Register(app)
	reg.Register(lookup)
	addr, mat, store := startTestServerWithManifests(t, reg)
	if err := store.Save(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	conn := dialTestServer(t, addr, mat.CACertPath)
	t.Cleanup(func() { _ = conn.Close() })
	return apiv1.NewResourceServiceClient(conn), apiv1.NewInventoryServiceClient(conn), store, app, lookup
}

func assertReferenceHostDrift(t *testing.T, drift []*apiv1.DriftEntry, desiredIP string) {
	t.Helper()
	if desiredIP == "192.168.11.125" {
		if len(drift) != 0 {
			t.Fatalf("healthy resolved reference has drift: %v", drift)
		}
		return
	}
	if len(drift) != 1 || drift[0].Path != "spec.target.host" || drift[0].Desired != desiredIP || drift[0].Observed != "192.168.11.125" {
		t.Fatalf("changed reference output lost live drift: %v", drift)
	}
}

func assertReferenceInspectionReadOnly(t *testing.T, desired *protocol.Resource, store *manifests.Store, app, lookup *fakeProvider) {
	t.Helper()
	saved, err := store.Load(context.Background(), desired.APIVersion, desired.Kind, desired.Metadata.Name)
	if err != nil || saved == nil || !reflect.DeepEqual(saved.Spec, desired.Spec) {
		t.Fatalf("inspection changed saved intent: saved=%v error=%v", saved, err)
	}
	if len(app.applied) != 0 || len(app.deletes) != 0 || len(lookup.applied) != 0 || len(lookup.deletes) != 0 {
		t.Fatal("inspection mutated a provider")
	}
	if app.getReturn.Spec["target"].(map[string]any)["host"] != "192.168.11.125" {
		t.Fatal("inspection substituted desired state into live observation")
	}
}

func TestReferenceDriftInspection(t *testing.T) {
	for _, ip := range []string{"192.168.11.125", "192.168.11.126"} {
		t.Run(ip, func(t *testing.T) {
			desired := referenceDriftManifest()
			client, inventory, store, app, lookup := referenceDriftFixture(t, desired, referenceDriftGuest(ip), nil)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req := &apiv1.GetRequest{ApiVersion: desired.APIVersion, Kind: desired.Kind, Name: desired.Metadata.Name}
			got, err := client.Get(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			assertReferenceHostDrift(t, got.Resource.Drift, ip)
			if !reflect.DeepEqual(got.Applied.Spec.AsMap(), desired.Spec) {
				t.Fatalf("Applied lost raw reference markers: %v", got.Applied)
			}
			listed, err := client.List(ctx, &apiv1.ListRequest{ApiVersion: desired.APIVersion, Kind: desired.Kind})
			if err != nil || len(listed.GetResources()) != 1 {
				t.Fatalf("List: %v %v", listed, err)
			}
			assertReferenceHostDrift(t, listed.Resources[0].Drift, ip)
			submitted, err := resourceToProto(desired)
			if err != nil {
				t.Fatal(err)
			}
			preview, err := client.DryRunApply(ctx, &apiv1.DryRunApplyRequest{Resource: submitted})
			if err != nil || len(preview.GetValidationErrors()) != 0 || len(preview.GetDiff()) != 0 {
				t.Fatalf("same-reference desired edit diff: %v %v", preview, err)
			}
			assertReferenceHostDrift(t, preview.LiveDrift, ip)
			wantOperation := "no-op"
			if ip != "192.168.11.125" {
				wantOperation = "update"
			}
			if preview.Operation != wantOperation {
				t.Fatalf("operation = %q, want %q", preview.Operation, wantOperation)
			}
			saved, err := inventory.GetSavedResource(ctx, req)
			if err != nil || !reflect.DeepEqual(saved.GetResource().GetSpec().AsMap(), desired.Spec) {
				t.Fatalf("saved API lost raw intent: %v %v", saved, err)
			}
			if !reflect.DeepEqual(submitted.Spec.AsMap(), desired.Spec) {
				t.Fatal("preview mutated submitted reference markers")
			}
			assertReferenceInspectionReadOnly(t, desired, store, app.fakeProvider, lookup.fakeProvider)
		})
	}
}

func TestReferenceDriftPreviewKeepsDesiredReferenceEdits(t *testing.T) {
	desired := referenceDriftManifest()
	client, _, store, app, lookup := referenceDriftFixture(t, desired, referenceDriftGuest("192.168.11.125"), nil)
	submitted := referenceDriftManifest()
	submitted.Spec["target"].(map[string]any)["host"].(map[string]any)["$ref"].(map[string]any)["name"] = "replacement-guest"
	request, err := resourceToProto(submitted)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := client.DryRunApply(context.Background(), &apiv1.DryRunApplyRequest{Resource: request})
	if err != nil || len(preview.GetValidationErrors()) != 0 || len(preview.GetLiveDrift()) != 0 {
		t.Fatalf("preview: %v %v", preview, err)
	}
	if len(preview.Diff) != 1 || preview.Diff[0].Path != "spec.target.host.$ref.name" || preview.Diff[0].Desired != "guest" || preview.Diff[0].Observed != "replacement-guest" || preview.Operation != "update" {
		t.Fatalf("reference edit was compared as resolved intent: %v", preview)
	}
	if len(lookup.gets) != 1 || lookup.gets[0][1] != "replacement-guest" {
		t.Fatalf("preview did not resolve submitted reference: %v", lookup.gets)
	}
	assertReferenceInspectionReadOnly(t, desired, store, app.fakeProvider, lookup.fakeProvider)
}

func TestReferenceDriftResolutionFailuresAreVisible(t *testing.T) {
	for _, tc := range []struct {
		name  string
		guest *protocol.Resource
		err   error
		code  string
	}{
		{"missing resource", nil, nil, "reference_not_found"},
		{"missing output", &protocol.Resource{Status: map[string]any{}}, nil, "reference_field_unavailable"},
		{"provider failure", nil, errors.New("PRIVATE-TOKEN PRIVATE-BODY"), "reference_read_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			desired := referenceDriftManifest()
			client, inventory, store, app, lookup := referenceDriftFixture(t, desired, tc.guest, tc.err)
			req := &apiv1.GetRequest{ApiVersion: desired.APIVersion, Kind: desired.Kind, Name: desired.Metadata.Name}
			got, err := client.Get(context.Background(), req)
			if got != nil || status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "spec.target.host") || strings.Contains(err.Error(), "PRIVATE-") {
				t.Fatalf("Get hid or leaked reference failure: %v %v", got, err)
			}
			listed, err := client.List(context.Background(), &apiv1.ListRequest{ApiVersion: desired.APIVersion, Kind: desired.Kind})
			if listed != nil || err == nil || !strings.Contains(err.Error(), "spec.target.host") || strings.Contains(err.Error(), "PRIVATE-") {
				t.Fatalf("List hid or leaked reference failure: %v %v", listed, err)
			}
			submitted, err := resourceToProto(desired)
			if err != nil {
				t.Fatal(err)
			}
			preview, err := client.DryRunApply(context.Background(), &apiv1.DryRunApplyRequest{Resource: submitted})
			if err != nil || len(preview.GetValidationErrors()) != 1 || len(preview.GetFieldErrors()) != 1 || preview.FieldErrors[0].Code != tc.code || preview.FieldErrors[0].Path != "spec.target.host" || strings.Contains(preview.String(), "PRIVATE-") {
				t.Fatalf("preview hid or leaked reference failure: %v %v", preview, err)
			}
			saved, err := inventory.GetSavedResource(context.Background(), req)
			if err != nil || !reflect.DeepEqual(saved.GetResource().GetSpec().AsMap(), desired.Spec) {
				t.Fatalf("failed live inspection prevented raw saved read: %v %v", saved, err)
			}
			assertReferenceInspectionReadOnly(t, desired, store, app.fakeProvider, lookup.fakeProvider)
		})
	}
}

func TestReferenceDriftInspectionDoesNotResolveSecrets(t *testing.T) {
	t.Setenv("OPENCTL_REFERENCE_DRIFT_TEST_SECRET", "PRIVATE-SECRET-VALUE")
	desired := referenceDriftManifest()
	desired.Spec["credential"] = map[string]any{"$secret": map[string]any{"env": "OPENCTL_REFERENCE_DRIFT_TEST_SECRET"}}
	client, _, store, app, lookup := referenceDriftFixture(t, desired, referenceDriftGuest("192.168.11.125"), nil)
	app.getReturn.Spec["credential"] = "provider-redacted"
	got, err := client.Get(context.Background(), &apiv1.GetRequest{ApiVersion: desired.APIVersion, Kind: desired.Kind, Name: desired.Metadata.Name})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Resource.Drift) != 1 || got.Resource.Drift[0].Path != "spec.credential" || !strings.Contains(got.Resource.Drift[0].Desired, "$secret") || strings.Contains(got.String(), "PRIVATE-SECRET-VALUE") {
		t.Fatalf("inspection resolved or suppressed secret marker: %v", got)
	}
	if !reflect.DeepEqual(got.Applied.Spec.AsMap(), desired.Spec) {
		t.Fatal("Applied lost secret/reference markers")
	}
	listed, err := client.List(context.Background(), &apiv1.ListRequest{ApiVersion: desired.APIVersion, Kind: desired.Kind})
	if err != nil || len(listed.GetResources()) != 1 || len(listed.Resources[0].Drift) != 1 || !strings.Contains(listed.Resources[0].Drift[0].Desired, "$secret") || strings.Contains(listed.String(), "PRIVATE-SECRET-VALUE") {
		t.Fatalf("list resolved or suppressed secret marker: %v %v", listed, err)
	}
	submitted, err := resourceToProto(desired)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := client.DryRunApply(context.Background(), &apiv1.DryRunApplyRequest{Resource: submitted})
	if err != nil || len(preview.GetDiff()) != 0 || len(preview.GetLiveDrift()) != 1 || !strings.Contains(preview.LiveDrift[0].Desired, "$secret") || strings.Contains(preview.String(), "PRIVATE-SECRET-VALUE") {
		t.Fatalf("preview resolved or suppressed secret marker: %v %v", preview, err)
	}
	assertReferenceInspectionReadOnly(t, desired, store, app.fakeProvider, lookup.fakeProvider)
}
