package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/providers"
	pmprovider "github.com/openctl/openctl/internal/controller/providers/proxmox"
	"github.com/openctl/openctl/internal/controller/refs"
	"github.com/openctl/openctl/internal/controller/storage"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func previewReference(t *testing.T, reg *providers.Registry, store *manifests.Store, field string) *apiv1.DryRunApplyResponse {
	t.Helper()
	spec, err := structpb.NewStruct(map[string]any{"items": []any{map[string]any{"target": map[string]any{"host": map[string]any{"$ref": map[string]any{"apiVersion": "proxmox.openctl.io/v1", "kind": "VirtualMachine", "name": "declared-vm", "field": field}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := newResourceHandler(reg, nil, nil, store).DryRunApply(ctxWithRole(auth.RoleViewer), &apiv1.DryRunApplyRequest{Resource: &apiv1.Resource{ApiVersion: "fake.openctl.io/v1", Kind: "FakeKind", Metadata: &apiv1.Metadata{Name: "preview"}, Spec: spec}})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func assertSafeReferencePreview(t *testing.T, out *apiv1.DryRunApplyResponse, code string) {
	t.Helper()
	if len(out.FieldErrors) != 1 || len(out.ValidationErrors) != 1 {
		t.Fatalf("missing diagnostics: %v", out)
	}
	fe := out.FieldErrors[0]
	if fe.Path != "spec.items[0].target.host" || fe.Code != code {
		t.Fatalf("unexpected diagnostic: %v", out)
	}
	if ref := fe.GetReference(); ref.GetApiVersion() != "proxmox.openctl.io/v1" || ref.GetKind() != "VirtualMachine" || ref.GetName() != "declared-vm" || fe.GetReferenceField() != "status.ip" {
		t.Fatalf("missing structured declared reference: %v", fe)
	}
	if !strings.Contains(fe.Message, "declared-vm") || !strings.Contains(fe.Message, "status.ip") {
		t.Fatalf("missing declared reference: %v", fe)
	}
	if out.ValidationErrors[0] != fe.Path+": "+fe.Message {
		t.Fatal("legacy diagnostic differs")
	}
	wire, err := proto.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	json, err := protojson.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(json), `"reference":`) || !strings.Contains(string(json), `"referenceField":"status.ip"`) {
		t.Fatalf("JSON lost structured declared reference: %s", json)
	}
	var decoded apiv1.DryRunApplyResponse
	if err := proto.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(fe.GetReference(), decoded.GetFieldErrors()[0].GetReference()) || decoded.GetFieldErrors()[0].GetReferenceField() != "status.ip" {
		t.Fatal("wire lost structured declared reference")
	}
	for _, secret := range []string{"PRIVATE-TOKEN", "PRIVATE-BODY", "private.invalid", "OBSERVED-POISON"} {
		for _, public := range []string{out.String(), string(wire), string(json)} {
			if strings.Contains(public, secret) {
				t.Fatal("preview leaked provider credentials or private diagnostics")
			}
		}
	}
}

func TestNativeReferencePreviewRedactsProviderResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		trust  bool
		status int
		code   string
	}{
		{"TLS", false, 200, "tls_untrusted"},
		{"authentication", true, 401, "unauthorized"},
		{"permissions", true, 403, "permission_denied"},
		{"server", true, 503, "provider_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reads, writes atomic.Int32
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					writes.Add(1)
				} else {
					reads.Add(1)
				}
				if r.Header.Get("Authorization") != "PVEAPIToken=fixture-id=PRIVATE-TOKEN" {
					t.Error("missing native credentials")
				}
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprintf(w, `{"error":"PRIVATE-BODY %s https://private.invalid/"}`, r.Header.Get("Authorization"))
			}))
			defer upstream.Close()
			cfg := &pmprovider.Config{Endpoint: upstream.URL, TokenID: "fixture-id", TokenSecret: "PRIVATE-TOKEN"}
			if tc.trust {
				cfg.CAFile = proxmoxFixtureCA(t, upstream)
			}
			p, err := pmprovider.NewMulti(map[string]*pmprovider.Config{"site": cfg}, "site")
			if err != nil {
				t.Fatal(err)
			}
			reg := providers.NewRegistry()
			fake := &fakeProvider{}
			reg.Register(fake)
			reg.Register(p)
			db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "preview.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store := manifests.New(db)
			assertSafeReferencePreview(t, previewReference(t, reg, store, "status.ip"), tc.code)
			saved, err := store.Load(context.Background(), "fake.openctl.io/v1", "FakeKind", "preview")
			if err != nil || saved != nil {
				t.Fatalf("preview persisted desired state: %v %v", saved, err)
			}
			if writes.Load() != 0 || len(fake.applied) != 0 || len(fake.deletes) != 0 {
				t.Fatal("preview mutated provider")
			}
			if tc.trust && reads.Load() == 0 {
				t.Fatal("native read was not exercised")
			}
			if !tc.trust && reads.Load() != 0 {
				t.Fatal("credentials sent over untrusted TLS")
			}
		})
	}
}

func TestReferencePreviewWrappedFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		resource *protocol.Resource
		code     string
	}{
		{"private", fmt.Errorf("wrapped: %w", errors.New("PRIVATE-TOKEN PRIVATE-BODY private.invalid")), nil, "reference_read_failed"},
		{"not found", fmt.Errorf("wrapped: %w", providers.NotFound("PRIVATE-TOKEN", "PRIVATE-BODY")), nil, "reference_not_found"},
		{"missing output", nil, &protocol.Resource{APIVersion: "OBSERVED-POISON", Kind: "OBSERVED-POISON", Metadata: protocol.ResourceMetadata{Name: "OBSERVED-POISON"}, Spec: map[string]any{"secret": "OBSERVED-POISON"}, Status: map[string]any{"secret": "OBSERVED-POISON"}}, "reference_field_unavailable"},
		{"nil resource", nil, nil, "reference_not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := providers.NewRegistry()
			reg.Register(&fakeProvider{})
			reg.Register(&referenceLookupProvider{fakeProvider: &fakeProvider{getErr: tc.err, getReturn: tc.resource}})
			assertSafeReferencePreview(t, previewReference(t, reg, nil, "status.ip"), tc.code)
		})
	}
}

type referenceLookupProvider struct{ *fakeProvider }

func (*referenceLookupProvider) Name() string    { return "proxmox" }
func (*referenceLookupProvider) Kinds() []string { return []string{"VirtualMachine"} }

func TestReferenceFieldErrorFailsClosed(t *testing.T) {
	declared := refs.Ref{APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine", Name: "declared-vm", Field: "status.ip"}
	for _, tc := range []struct {
		name string
		err  error
		code string
	}{
		{"untyped", errors.New("PRIVATE-TOKEN PRIVATE-BODY private.invalid"), "reference_read_failed"},
		{"invalid", &refs.ResolutionError{Path: "target.host", Kind: refs.InvalidReference, Reference: declared, Cause: errors.New("PRIVATE-TOKEN")}, "reference_invalid"},
		{"future", &refs.ResolutionError{Path: "target.host", Kind: refs.FailureKind(255), Reference: declared, Cause: errors.New("PRIVATE-TOKEN")}, "reference_read_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantPath := "spec.target.host"
			if tc.name == "untyped" {
				wantPath = "spec"
			}
			out := referenceFieldError(tc.err)
			if out.Path != wantPath || out.Code != tc.code || strings.Contains(out.Message, "PRIVATE") || out.Reference != nil || out.ReferenceField != "" {
				t.Fatalf("unsafe fallback: %v", out)
			}
		})
	}
	for _, kind := range []refs.FailureKind{refs.UnavailableResource, refs.UnavailableField, refs.ProviderReadFailed} {
		for _, incomplete := range []refs.Ref{
			{Kind: declared.Kind, Name: declared.Name, Field: declared.Field},
			{APIVersion: declared.APIVersion, Name: declared.Name, Field: declared.Field},
			{APIVersion: declared.APIVersion, Kind: declared.Kind, Field: declared.Field},
		} {
			out := referenceFieldError(&refs.ResolutionError{Kind: kind, Reference: incomplete, Cause: errors.New("PRIVATE-TOKEN")})
			if out.Reference != nil || out.ReferenceField != "" || strings.Contains(out.Message, "PRIVATE") {
				t.Fatalf("incomplete target escaped: %v", out)
			}
		}
	}
}

func TestNativeReferencePreviewMissingOutput(t *testing.T) {
	var reads, writes atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
			http.Error(w, "read only", http.StatusMethodNotAllowed)
			return
		}
		reads.Add(1)
		switch r.URL.Path {
		case "/api2/json/nodes":
			_, _ = fmt.Fprint(w, `{"data":[{"node":"pve1"}]}`)
		case "/api2/json/nodes/pve1/qemu":
			_, _ = fmt.Fprint(w, `{"data":[{"vmid":101,"name":"declared-vm","status":"running"}]}`)
		case "/api2/json/nodes/pve1/qemu/101/config":
			_, _ = fmt.Fprint(w, `{"data":{"net0":"virtio=AA:BB:CC:DD:EE:FF,bridge=vmbr0","memory":4096,"description":"OBSERVED-POISON"}}`)
		case "/api2/json/nodes/pve1/qemu/101/agent/network-get-interfaces":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, `{"error":"PRIVATE-BODY PRIVATE-TOKEN https://private.invalid/"}`)
		default:
			t.Errorf("unexpected provider read: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	p, err := pmprovider.NewMulti(map[string]*pmprovider.Config{"site": {
		Endpoint: upstream.URL, CAFile: proxmoxFixtureCA(t, upstream), TokenID: "fixture-id", TokenSecret: "PRIVATE-TOKEN",
	}}, "site")
	if err != nil {
		t.Fatal(err)
	}
	reg := providers.NewRegistry()
	fake := &fakeProvider{}
	reg.Register(fake)
	reg.Register(p)
	assertSafeReferencePreview(t, previewReference(t, reg, nil, "status.ip"), "reference_field_unavailable")
	if reads.Load() != 4 || writes.Load() != 0 || len(fake.applied) != 0 || len(fake.deletes) != 0 {
		t.Fatalf("preview added reads or mutations: reads=%d writes=%d", reads.Load(), writes.Load())
	}
}

func TestReferencePreviewWholeResourceFailure(t *testing.T) {
	reg := providers.NewRegistry()
	reg.Register(&fakeProvider{})
	reg.Register(&referenceLookupProvider{fakeProvider: &fakeProvider{getErr: errors.New("PRIVATE-TOKEN PRIVATE-BODY private.invalid")}})
	out := previewReference(t, reg, nil, "")
	if len(out.FieldErrors) != 1 || len(out.ValidationErrors) != 1 || out.Operation != "" {
		t.Fatalf("whole-resource failure did not block preview: %v", out)
	}
	fe := out.FieldErrors[0]
	if ref := fe.GetReference(); ref.GetApiVersion() != "proxmox.openctl.io/v1" || ref.GetKind() != "VirtualMachine" || ref.GetName() != "declared-vm" || fe.ReferenceField != "" || fe.Code != "reference_read_failed" {
		t.Fatalf("whole-resource target lost: %v", fe)
	}
	for _, secret := range []string{"PRIVATE-TOKEN", "PRIVATE-BODY", "private.invalid"} {
		if strings.Contains(out.String(), secret) {
			t.Fatal("whole-resource preview leaked provider diagnostics")
		}
	}
}

func TestReferencePreviewMalformedTargetFailsClosed(t *testing.T) {
	for _, marker := range []any{
		"PRIVATE-TOKEN",
		map[string]any{"apiVersion": "PRIVATE-TOKEN", "kind": "VirtualMachine"},
		map[string]any{"apiVersion": "PRIVATE-TOKEN", "kind": "VirtualMachine", "name": "PRIVATE-BODY", "field": 42},
	} {
		spec, err := structpb.NewStruct(map[string]any{"target": map[string]any{"$ref": marker}})
		if err != nil {
			t.Fatal(err)
		}
		reg := providers.NewRegistry()
		reg.Register(&fakeProvider{})
		out, err := newResourceHandler(reg, nil, nil, nil).DryRunApply(ctxWithRole(auth.RoleViewer), &apiv1.DryRunApplyRequest{
			Resource: &apiv1.Resource{ApiVersion: "fake.openctl.io/v1", Kind: "FakeKind", Metadata: &apiv1.Metadata{Name: "preview"}, Spec: spec},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.FieldErrors) != 1 || out.FieldErrors[0].Code != "reference_invalid" || out.FieldErrors[0].Reference != nil || out.FieldErrors[0].ReferenceField != "" {
			t.Fatalf("malformed target became actionable: %v", out)
		}
		wire, err := proto.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		json, err := protojson.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(wire), "PRIVATE") || strings.Contains(string(json), "PRIVATE") {
			t.Fatal("malformed target leaked partial declared values")
		}
	}
}
