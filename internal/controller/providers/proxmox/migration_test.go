package proxmox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

func TestMigrationIdentityReadsSelectedContextAndActualUUID(t *testing.T) {
	wrong := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("migration scanned the wrong context: %s", r.URL.Path)
		http.Error(w, "wrong context", http.StatusInternalServerError)
	}))
	defer wrong.Close()
	for _, tc := range []struct {
		name, smbios, node string
		template, count    int
		wantErr            bool
	}{
		{"actual identity", "uuid=8E0DA4E0-01EF-441E-A64A-75DB8614A535,manufacturer=example", "pve1", 0, 1, false},
		{"missing UUID", "manufacturer=example", "pve1", 0, 1, true},
		{"invalid UUID", "uuid=not-a-uuid", "pve1", 0, 1, true},
		{"nil UUID", "uuid=00000000-0000-0000-0000-000000000000", "pve1", 0, 1, true},
		{"duplicate UUID", "uuid=8e0da4e0-01ef-441e-a64a-75db8614a535,uuid=8e0da4e0-01ef-441e-a64a-75db8614a535", "pve1", 0, 1, true},
		{"wrong node", "uuid=8e0da4e0-01ef-441e-a64a-75db8614a535", "other", 0, 1, true},
		{"template", "uuid=8e0da4e0-01ef-441e-a64a-75db8614a535", "pve1", 1, 1, true},
		{"missing VM", "uuid=8e0da4e0-01ef-441e-a64a-75db8614a535", "pve1", 0, 0, true},
		{"ambiguous VM", "uuid=8e0da4e0-01ef-441e-a64a-75db8614a535", "pve1", 0, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			right := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("migration mutated provider: %s %s", r.Method, r.URL.Path)
					http.Error(w, "mutation forbidden", http.StatusMethodNotAllowed)
					return
				}
				reads++
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api2/json/nodes":
					_, _ = w.Write([]byte(`{"data":[{"node":"pve1"}]}`))
				case "/api2/json/nodes/pve1/qemu":
					vms := []any{}
					for i := range tc.count {
						vms = append(vms, map[string]any{"name": "existing", "vmid": 101 + i, "template": tc.template})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": vms})
				case "/api2/json/nodes/pve1/qemu/101/config":
					_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"smbios1": tc.smbios}})
				default:
					t.Errorf("unexpected identity read: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer right.Close()
			p := NewMulti(map[string]*Config{
				"source":      {Endpoint: wrong.URL, TokenID: "test", TokenSecret: "test"},
				"destination": {Endpoint: right.URL, TokenID: "test", TokenSecret: "test"},
			}, "source")
			identity, err := p.MigrationIdentity(context.Background(), &protocol.Resource{
				Kind: kindVM, Metadata: protocol.ResourceMetadata{Name: "existing"},
				Spec: map[string]any{"context": "destination", "node": tc.node},
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("identity error = %v, wantErr=%v", err, tc.wantErr)
			}
			if !tc.wantErr && identity != "proxmox:pve1:101:8e0da4e0-01ef-441e-a64a-75db8614a535" {
				t.Fatalf("identity = %q", identity)
			}
			if reads == 0 {
				t.Fatal("identity was fabricated without reading provider")
			}
		})
	}
}
