package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/provideridentity"
	"github.com/openctl/openctl/internal/controller/providers"
	pmprovider "github.com/openctl/openctl/internal/controller/providers/proxmox"
	"github.com/openctl/openctl/internal/controller/storage"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type canonicalOwnerProvider struct {
	fakeProvider
	child *protocol.Resource
}

func (p *canonicalOwnerProvider) Name() string    { return "cluster" }
func (p *canonicalOwnerProvider) Kinds() []string { return []string{"Cluster"} }
func (p *canonicalOwnerProvider) OwnerOf(kind, name string) (string, string, bool) {
	return "Cluster", "owner", kind == "VirtualMachine" && name == "existing"
}
func (p *canonicalOwnerProvider) Plan(context.Context, *protocol.Resource) (*providers.PlanResult, error) {
	return &providers.PlanResult{Children: []*protocol.Resource{p.child}}, nil
}

func TestIdentityConfirmationUsesCanonicalPlacementWithoutAdoptionOrProviderWrites(t *testing.T) {
	for _, composite := range []bool{false, true} {
		name := "standalone"
		if composite {
			name = "composite child"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			writes := 0
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					writes++
					http.Error(w, "mutation forbidden", 405)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api2/json/nodes":
					_, _ = w.Write([]byte(`{"data":[{"node":"pve1"}]}`))
				case "/api2/json/nodes/pve1/qemu":
					_, _ = w.Write([]byte(`{"data":[{"vmid":101,"name":"existing","status":"stopped"}]}`))
				case "/api2/json/nodes/pve1/qemu/101/config":
					_, _ = w.Write([]byte(`{"data":{"smbios1":"uuid=8e0da4e0-01ef-441e-a64a-75db8614a535","cores":2,"memory":"2048"}}`))
				default:
					t.Errorf("unexpected read %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store := manifests.New(db)
			bindings := provideridentity.New(db)
			pm := pmprovider.NewMulti(map[string]*pmprovider.Config{"site": {Endpoint: srv.URL}}, "site")
			pm.SetIdentityStore(bindings)
			reg := providers.NewRegistry()
			reg.Register(pm)
			vm := &protocol.Resource{APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine", Metadata: protocol.ResourceMetadata{Name: "existing"}, Spec: map[string]any{"context": "site", "node": "pve1"}}
			if composite {
				reg.Register(&canonicalOwnerProvider{child: vm})
				if err := store.Save(ctx, &protocol.Resource{APIVersion: "cluster.openctl.io/v1", Kind: "Cluster", Metadata: protocol.ResourceMetadata{Name: "owner"}, Spec: map[string]any{}}); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := store.Save(ctx, vm); err != nil {
					t.Fatal(err)
				}
			}
			h := newResourceHandler(reg, nil, nil, store)
			inventory, err := h.DiscoverResources(ctxWithRole(auth.RoleViewer), &apiv1.DiscoverResourcesRequest{Context: "site"})
			if err != nil {
				t.Fatal(err)
			}
			if len(inventory.Resources) != 1 || !inventory.Resources[0].IdentityConfirmationRequired || !inventory.Resources[0].Managed || inventory.Resources[0].Adoptable {
				t.Fatalf("unrecoverable managed inventory: %+v", inventory)
			}
			req := &apiv1.ConfirmResourceIdentityRequest{ApiVersion: vm.APIVersion, Kind: vm.Kind, Name: vm.Metadata.Name, ProviderContext: "other", Identity: inventory.Resources[0].Identity}
			if _, err := h.ConfirmResourceIdentity(ctxWithRole(auth.RoleViewer), req); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("viewer confirmation err=%v", err)
			}
			if _, err := h.ConfirmResourceIdentity(ctxWithRole(auth.RoleAdmin), req); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("placement override err=%v", err)
			}
			req.ProviderContext = "site"
			if _, err := h.ConfirmResourceIdentity(ctxWithRole(auth.RoleAdmin), req); err != nil {
				t.Fatal(err)
			}
			id, err := bindings.LoadIdentity(ctx, "existing")
			if err != nil || id == nil || id.VMID != 101 || id.Context != "site" {
				t.Fatalf("binding=%+v err=%v", id, err)
			}
			tracked, err := store.Load(ctx, vm.APIVersion, vm.Kind, vm.Metadata.Name)
			if err != nil {
				t.Fatal(err)
			}
			if composite && tracked != nil {
				t.Fatal("confirmation adopted child as independently managed")
			}
			if !composite && (tracked == nil || manifests.Hash(tracked) != manifests.Hash(vm)) {
				t.Fatal("confirmation changed saved desired intent")
			}
			if writes != 0 {
				t.Fatalf("confirmation mutated infrastructure %d times", writes)
			}
		})
	}
}
