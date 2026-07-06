package proxmox

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/providers/providertest"
	"github.com/openctl/openctl/pkg/protocol"
)

// fakeProxmox is a minimal stateful Proxmox VE API — just enough to drive the
// VirtualMachine provider through a full clone → get → list → delete
// lifecycle: one node (pve1), an id counter, and a name→vmid map that clone
// populates and delete clears. Every VM reports "stopped" so the handler's
// deleteVM never hits its 5-second stop-and-wait. Anything the create path
// touches incidentally (config, disk resize, guest-agent IP) gets a benign
// success from the default arm, keeping the fake small.
type fakeProxmox struct {
	mu     sync.Mutex
	vms    map[string]int // name -> vmid
	nextID int
}

func newFakeProxmox() *fakeProxmox {
	return &fakeProxmox{vms: map[string]int{}, nextID: 100}
}

func (f *fakeProxmox) newServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case path == "/api2/json/nodes" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"node": "pve1", "status": "online"}},
			})
		case path == "/api2/json/cluster/nextid" && r.Method == http.MethodGet:
			f.nextID++
			_ = json.NewEncoder(w).Encode(map[string]any{"data": strconv.Itoa(f.nextID)})
		case path == "/api2/json/nodes/pve1/qemu" && r.Method == http.MethodGet:
			list := make([]map[string]any, 0, len(f.vms))
			for name, vmid := range f.vms {
				list = append(list, map[string]any{
					"vmid": vmid, "name": name, "status": "stopped", "node": "pve1", "template": 0,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": list})
		case strings.HasSuffix(path, "/clone") && r.Method == http.MethodPost:
			_ = r.ParseForm()
			newid, _ := strconv.Atoi(r.Form.Get("newid"))
			f.vms[r.Form.Get("name")] = newid
			_ = json.NewEncoder(w).Encode(map[string]any{"data": ""})
		case r.Method == http.MethodDelete && strings.Contains(path, "/qemu/"):
			vmid, _ := strconv.Atoi(path[strings.LastIndex(path, "/")+1:])
			for name, id := range f.vms {
				if id == vmid {
					delete(f.vms, name)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": ""})
		default:
			// config / resize / start / guest-agent network-get / etc.
			_ = json.NewEncoder(w).Encode(map[string]any{"data": ""})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestProxmoxVMConformance runs the shared providers.Provider conformance
// battery against the compiled-in Proxmox provider (the flagship first-party
// provider), driven by the stateful fake above. Completes conformance
// coverage across the compiled-in, external-plugin, and Terraform-host
// providers.
//
// Capabilities note: proxmox VM Apply is a no-op on an existing VM (returns
// observed state, surfaces drift, does not mutate) per CONTROLLER.md:23, so
// this binds with NoOpOnExisting=true — the battery asserts a re-Apply returns
// the same observed state without mutating.
func TestProxmoxVMConformance(t *testing.T) {
	providertest.Suite{
		NewProvider: func(t *testing.T) (providers.Provider, func()) {
			srv := newFakeProxmox().newServer(t)
			p := New(&Config{Endpoint: srv.URL, TokenID: "t", TokenSecret: "s"})
			return p, func() {}
		},
		Kind: kindVM,
		Manifest: func(name string) *protocol.Resource {
			return &protocol.Resource{
				APIVersion: "proxmox.openctl.io/v1",
				Kind:       kindVM,
				Metadata:   protocol.ResourceMetadata{Name: name},
				Spec: map[string]any{
					"node":          "pve1",
					"template":      map[string]any{"vmid": float64(9000)},
					"startOnCreate": false,
				},
			}
		},
		Capabilities: providertest.Capabilities{SupportsList: true, NoOpOnExisting: true},
	}.Run(t)
}
