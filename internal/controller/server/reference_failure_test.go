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
	"google.golang.org/protobuf/types/known/structpb"
)

func previewReference(t *testing.T, reg *providers.Registry, store *manifests.Store) *apiv1.DryRunApplyResponse {
	t.Helper()
	spec, err := structpb.NewStruct(map[string]any{"items": []any{map[string]any{"target": map[string]any{"host": map[string]any{"$ref": map[string]any{"apiVersion": "proxmox.openctl.io/v1", "kind": "VirtualMachine", "name": "declared-vm", "field": "status.ip"}}}}}})
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
	if !strings.Contains(fe.Message, "declared-vm") || !strings.Contains(fe.Message, "status.ip") {
		t.Fatalf("missing declared reference: %v", fe)
	}
	if out.ValidationErrors[0] != fe.Path+": "+fe.Message {
		t.Fatal("legacy diagnostic differs")
	}
	for _, secret := range []string{"PRIVATE-TOKEN", "PRIVATE-BODY", "private.invalid"} {
		if strings.Contains(out.String(), secret) {
			t.Fatal("preview leaked provider credentials or private diagnostics")
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
			assertSafeReferencePreview(t, previewReference(t, reg, store), tc.code)
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
		{"missing output", nil, &protocol.Resource{}, "reference_field_unavailable"},
		{"nil resource", nil, nil, "reference_not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := providers.NewRegistry()
			reg.Register(&fakeProvider{})
			reg.Register(&referenceLookupProvider{fakeProvider: &fakeProvider{getErr: tc.err, getReturn: tc.resource}})
			assertSafeReferencePreview(t, previewReference(t, reg, nil), tc.code)
		})
	}
}

type referenceLookupProvider struct{ *fakeProvider }

func (*referenceLookupProvider) Name() string    { return "proxmox" }
func (*referenceLookupProvider) Kinds() []string { return []string{"VirtualMachine"} }

func TestReferenceFieldErrorFailsClosed(t *testing.T) {
	out := referenceFieldError(errors.New("PRIVATE-TOKEN PRIVATE-BODY private.invalid"))
	if out.Path != "spec" || out.Code != "reference_read_failed" || strings.Contains(out.Message, "PRIVATE") {
		t.Fatalf("unsafe fallback: %v", out)
	}
	invalid := referenceFieldError(&refs.ResolutionError{Path: "target.host", Kind: refs.InvalidReference, Cause: errors.New("PRIVATE-TOKEN")})
	if invalid.Path != "spec.target.host" || invalid.Code != "reference_invalid" || strings.Contains(invalid.Message, "PRIVATE") {
		t.Fatalf("unsafe invalid reference: %v", invalid)
	}
}
