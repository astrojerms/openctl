package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/internal/controller/providers"
	pmprovider "github.com/openctl/openctl/internal/controller/providers/proxmox"
	"github.com/openctl/openctl/internal/controller/storage"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
)

func migrationVM() *protocol.Resource {
	return &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine",
		Metadata: protocol.ResourceMetadata{Name: "existing", Annotations: map[string]string{"example.io/note": "desired intent"}},
		Spec: map[string]any{
			"context": "source", "node": "pve1", "template": map[string]any{"name": "ubuntu"},
			"cpu": map[string]any{"cores": 4}, "memory": map[string]any{"size": 2048},
			"cloudInit": map[string]any{"user": "ubuntu", "password": "literal-sensitive-password"},
		},
	}
}

func migrationFixture(t *testing.T, reg *providers.Registry) (*resourceHandler, *sql.DB, *operations.Store) {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := manifests.New(db)
	h := newResourceHandler(reg, nil, nil, store)
	h.mirror = manifests.NewDiskMirror(store, t.TempDir())
	return h, db, operations.New(db, 50)
}

// Real Proxmox decoding and real SQLite are used here: forwarding-only mocks
// would not catch accidental Apply/resize or exporting observation as intent.
func TestMigrationExportAdoptForgetPreservesLiveVM(t *testing.T) {
	ctx := context.Background()
	uuid := "8e0da4e0-01ef-441e-a64a-75db8614a535"
	mutations := 0
	pve := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			mutations++
			t.Errorf("migration mutated Proxmox: %s %s", req.Method, req.URL.Path)
			http.Error(w, "read-only VM", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/api2/json/nodes":
			_, _ = w.Write([]byte(`{"data":[{"node":"pve1"}]}`))
		case "/api2/json/nodes/pve1/qemu":
			_, _ = w.Write([]byte(`{"data":[{"name":"existing","vmid":101,"status":"stopped"}]}`))
		case "/api2/json/nodes/pve1/qemu/101/config":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"smbios1": "uuid=" + uuid, "cores": 2, "sockets": 1, "memory": "2048", "name": "existing",
			}})
		default:
			t.Errorf("unexpected provider read: %s", req.URL.Path)
			http.NotFound(w, req)
		}
	}))
	defer pve.Close()
	registry := func(contextName string) *providers.Registry {
		reg := providers.NewRegistry()
		reg.Register(pmprovider.NewMulti(map[string]*pmprovider.Config{
			contextName: {Endpoint: pve.URL, TokenID: "test", TokenSecret: "test"},
		}, contextName))
		return reg
	}
	source, _, _ := migrationFixture(t, registry("source"))
	destination, _, _ := migrationFixture(t, registry("destination"))
	desired := migrationVM()
	if err := source.mirror.Save(ctx, desired); err != nil {
		t.Fatal(err)
	}
	exported, err := source.ExportResource(ctx, &apiv1.ExportResourceRequest{ApiVersion: desired.APIVersion, Kind: desired.Kind, Name: desired.Metadata.Name})
	if err != nil {
		t.Fatal(err)
	}
	bundle := exported.GetBundle()
	original := proto.Clone(bundle).(*apiv1.MigrationBundle)
	digest, _ := manifests.MigrationDigest(desired)
	if bundle.GetManifestDigest() != digest || bundle.GetResource().GetStatus() != nil || bundle.GetResource().GetSpec().AsMap()["cpu"].(map[string]any)["cores"] != float64(4) {
		t.Fatal("export must preserve complete desired intent rather than live CPU configuration")
	}
	if bundle.GetResource().GetMetadata().GetAnnotations()["example.io/note"] != "desired intent" {
		t.Fatal("export dropped desired annotations")
	}
	if stillManaged, err := source.manifests.Load(ctx, desired.APIVersion, desired.Kind, desired.Metadata.Name); err != nil || stillManaged == nil {
		t.Fatalf("export released ownership: %v", err)
	}
	request := &apiv1.AdoptResourceRequest{Bundle: bundle, ProviderContext: "destination"}
	uuid = "58bc2d47-5923-4a79-9d24-aa8f53a0ca7f"
	if _, err := destination.AdoptResource(ctx, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("wrong UUID must fail safely: %v", err)
	}
	if saved, _ := destination.manifests.Load(ctx, desired.APIVersion, desired.Kind, desired.Metadata.Name); saved != nil {
		t.Fatal("wrong UUID adoption persisted intent")
	}
	uuid = "8e0da4e0-01ef-441e-a64a-75db8614a535"
	adopted, err := destination.AdoptResource(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(adopted.GetResource().GetDrift()) == 0 {
		t.Fatal("adoption must retain live CPU drift without resizing")
	}
	if !proto.Equal(bundle, original) {
		t.Fatal("destination context override mutated the original source bundle")
	}
	if _, err := destination.AdoptResource(ctx, request); err != nil {
		t.Fatalf("identical adoption retry: %v", err)
	}
	saved, err := destination.manifests.Load(ctx, desired.APIVersion, desired.Kind, desired.Metadata.Name)
	if err != nil || saved.Spec["context"] != "destination" || saved.Metadata.Annotations["example.io/note"] != "desired intent" {
		t.Fatalf("destination intent was not adopted exactly with context override: %v", err)
	}
	projection := filepath.Join(destination.mirror.Root(), destination.mirror.RelPathFor(desired.APIVersion, desired.Kind, desired.Metadata.Name))
	if _, err := os.Stat(projection); err != nil {
		t.Fatalf("adoption did not materialize projection: %v", err)
	}
	conflicting := proto.Clone(bundle.GetResource()).(*apiv1.Resource)
	conflicting.Metadata.Annotations["example.io/note"] = "different destination intent"
	if _, err := destination.AdoptResource(ctx, &apiv1.AdoptResourceRequest{Resource: conflicting, ProviderContext: "destination"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("different existing intent was replaced: %v", err)
	}
	foreign := proto.Clone(bundle).(*apiv1.MigrationBundle)
	foreign.ControllerId = "another-controller"
	if _, err := source.ForgetResource(ctx, &apiv1.ForgetResourceRequest{Bundle: foreign}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("foreign controller bundle released source: %v", err)
	}
	// Full metadata participates in stale-export detection.
	desired.Metadata.Annotations["example.io/note"] = "newer intent"
	if err := source.manifests.Save(ctx, desired); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ForgetResource(ctx, &apiv1.ForgetResourceRequest{Bundle: bundle}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("stale bundle forget: %v", err)
	}
	desired.Metadata.Annotations["example.io/note"] = "desired intent"
	if err := source.manifests.Save(ctx, desired); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := source.ForgetResource(ctx, &apiv1.ForgetResourceRequest{Bundle: bundle}); err != nil {
			t.Fatalf("forget/retry: %v", err)
		}
	}
	if tracked, _ := source.manifests.Load(ctx, desired.APIVersion, desired.Kind, desired.Metadata.Name); tracked != nil {
		t.Fatal("forget left source desired tracking")
	}
	projection = filepath.Join(source.mirror.Root(), source.mirror.RelPathFor(desired.APIVersion, desired.Kind, desired.Metadata.Name))
	if _, err := os.Stat(projection); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forget left disk projection: %v", err)
	}
	wire, _ := resourceToProto(desired)
	if _, err := source.Apply(ctx, &apiv1.ApplyRequest{Resource: wire}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("forgotten Apply allowed: %v", err)
	}
	if _, err := source.Delete(ctx, &apiv1.DeleteRequest{ApiVersion: desired.APIVersion, Kind: desired.Kind, Name: desired.Metadata.Name}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("forgotten Delete allowed: %v", err)
	}
	if _, err := source.InvokeAction(ctx, &apiv1.InvokeActionRequest{ApiVersion: desired.APIVersion, Kind: desired.Kind, ResourceName: desired.Metadata.Name, Action: "start"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("forgotten runtime action allowed: %v", err)
	}
	// Explicit ordinary-manifest adoption reclaims management without Apply.
	if _, err := source.AdoptResource(ctx, &apiv1.AdoptResourceRequest{Resource: wire}); err != nil {
		t.Fatalf("explicit manifest adoption: %v", err)
	}
	if released, err := source.manifests.IsForgotten(ctx, desired.APIVersion, desired.Kind, desired.Metadata.Name); err != nil || released {
		t.Fatalf("explicit adoption did not clear release: %v", err)
	}
	if mutations != 0 {
		t.Fatalf("provider mutations = %d", mutations)
	}
}

func TestMigrationRequiresAdminAndConfiguredStore(t *testing.T) {
	h := newResourceHandler(providers.NewRegistry(), nil, nil, nil)
	calls := []func(context.Context) error{
		func(ctx context.Context) error {
			_, err := h.ExportResource(ctx, &apiv1.ExportResourceRequest{})
			return err
		},
		func(ctx context.Context) error {
			_, err := h.AdoptResource(ctx, &apiv1.AdoptResourceRequest{})
			return err
		},
		func(ctx context.Context) error {
			_, err := h.ForgetResource(ctx, &apiv1.ForgetResourceRequest{})
			return err
		},
	}
	for _, call := range calls {
		for _, role := range []auth.Role{auth.RoleViewer, auth.RoleEditor} {
			if err := call(ctxWithRole(role)); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("non-admin migration: %v", err)
			}
		}
		if err := call(ctxWithRole(auth.RoleAdmin)); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("unconfigured migration store: %v", err)
		}
	}
}

func TestMigrationRefusesRunningGitOpsAutomation(t *testing.T) {
	h, _, _ := migrationFixture(t, providers.NewRegistry())
	h.gitopsEnabled = true
	calls := []func() error{
		func() error {
			_, err := h.ExportResource(context.Background(), &apiv1.ExportResourceRequest{})
			return err
		},
		func() error {
			_, err := h.AdoptResource(context.Background(), &apiv1.AdoptResourceRequest{})
			return err
		},
		func() error {
			_, err := h.ForgetResource(context.Background(), &apiv1.ForgetResourceRequest{})
			return err
		},
	}
	for _, call := range calls {
		if err := call(); status.Code(err) != codes.FailedPrecondition || !strings.Contains(status.Convert(err).Message(), "restart") {
			t.Fatalf("live GitOps must block migration with actionable restart instructions: %v", err)
		}
	}
}

func TestMigrationMalformedAndUnsafeRequests(t *testing.T) {
	h, _, ops := migrationFixture(t, providers.NewRegistry())
	ctx := context.Background()
	wire, _ := resourceToProto(migrationVM())
	digest, _ := manifests.MigrationDigest(protoToResource(wire))
	bundle := &apiv1.MigrationBundle{Version: 1, Resource: wire, ControllerId: "source", ManifestDigest: digest, Identity: "identity"}
	for _, tc := range []struct {
		name    string
		request *apiv1.AdoptResourceRequest
		code    codes.Code
	}{
		{"neither input", &apiv1.AdoptResourceRequest{}, codes.InvalidArgument},
		{"both inputs", &apiv1.AdoptResourceRequest{Resource: wire, Bundle: bundle}, codes.InvalidArgument},
		{"unknown bundle version", &apiv1.AdoptResourceRequest{Bundle: &apiv1.MigrationBundle{Version: 2}}, codes.InvalidArgument},
		{"tampered digest", &apiv1.AdoptResourceRequest{Bundle: &apiv1.MigrationBundle{Version: 1, Resource: wire, ControllerId: "source", ManifestDigest: strings.Repeat("0", 64), Identity: "identity"}}, codes.InvalidArgument},
		{"unconfigured provider", &apiv1.AdoptResourceRequest{Resource: wire}, codes.FailedPrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := h.AdoptResource(ctx, tc.request); status.Code(err) != tc.code {
				t.Fatalf("code=%v, want %v: %v", status.Code(err), tc.code, err)
			}
		})
	}
	for _, mutate := range []func(*apiv1.Resource){
		func(r *apiv1.Resource) { r.Kind = "Cluster" },
		func(r *apiv1.Resource) { r.Children = []*apiv1.ResourceRef{{Kind: "VirtualMachine", Name: "child"}} },
		func(r *apiv1.Resource) { r.Metadata.OwnerRefs = []*apiv1.ResourceRef{{Kind: "Cluster", Name: "owner"}} },
	} {
		unsafe := proto.Clone(wire).(*apiv1.Resource)
		mutate(unsafe)
		if _, err := h.AdoptResource(ctx, &apiv1.AdoptResourceRequest{Resource: unsafe}); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("unsafe resource not refused: %v", err)
		}
	}
	if err := h.manifests.Save(ctx, migrationVM()); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.Submit(ctx, &operations.Operation{Type: operations.TypeApply, APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine", ResourceName: "unrelated", ManifestJSON: `{}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ExportResource(ctx, &apiv1.ExportResourceRequest{ApiVersion: wire.GetApiVersion(), Kind: wire.GetKind(), Name: wire.GetMetadata().GetName()}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("globally busy controller exported resource: %v", err)
	}
}

func TestMigrationRESTRoutesReachRegisteredHandlers(t *testing.T) {
	base, token := startGatewayTestServer(t)
	for _, route := range []string{"export", "adopt", "forget"} {
		req, err := http.NewRequest(http.MethodPost, base+"/v1/resources:"+route, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req) // #nosec G704 -- in-process test server
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			// FailedPrecondition maps to 400: store isn't configured in fixture.
			t.Fatalf("%s REST status=%d; handler must be registered", route, resp.StatusCode)
		}
	}
}

type migrationUnsupportedProvider struct{ fakeProvider }

func (*migrationUnsupportedProvider) Name() string    { return "proxmox" }
func (*migrationUnsupportedProvider) Kinds() []string { return []string{"VirtualMachine"} }

type migrationOwnedProvider struct {
	migrationUnsupportedProvider
	owned         bool
	children      bool
	identityReads int
}

func (p *migrationOwnedProvider) MigrationIdentity(context.Context, *protocol.Resource) (string, error) {
	p.identityReads++
	return "identity", nil
}

func (p *migrationOwnedProvider) OwnerOf(string, string) (string, string, bool) {
	return "Cluster", "owner", p.owned
}

func (p *migrationOwnedProvider) ChildrenOf(string, string) []providers.ResourceRef {
	if p.children {
		return []providers.ResourceRef{{APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine", Name: "child"}}
	}
	return nil
}

func TestMigrationRejectsProviderOwnershipAndMissingCapability(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider providers.Provider
		label    bool
	}{
		{"missing identity capability", &migrationUnsupportedProvider{}, false},
		{"registry-owned VM", &migrationOwnedProvider{owned: true}, false},
		{"registry composite children", &migrationOwnedProvider{children: true}, false},
		{"owner-labeled VM", &migrationOwnedProvider{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := providers.NewRegistry()
			reg.Register(tc.provider)
			h, _, _ := migrationFixture(t, reg)
			desired := migrationVM()
			if tc.label {
				desired.Metadata.Labels = map[string]string{providers.LabelOwnerKind: "Cluster", providers.LabelOwnerName: "owner"}
			}
			wire, _ := resourceToProto(desired)
			if _, err := h.AdoptResource(context.Background(), &apiv1.AdoptResourceRequest{Resource: wire}); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("unsafe provider migration: %v", err)
			}
			if p, ok := tc.provider.(*migrationOwnedProvider); ok && p.identityReads != 0 {
				t.Fatal("unsafe provider was consulted before rejecting ownership")
			}
			saved, err := h.manifests.Load(context.Background(), desired.APIVersion, desired.Kind, desired.Metadata.Name)
			if err != nil || saved != nil {
				t.Fatalf("unsafe provider adoption persisted state: %v", err)
			}
		})
	}
}
