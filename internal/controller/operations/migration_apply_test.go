package operations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/providers"
	pmprovider "github.com/openctl/openctl/internal/controller/providers/proxmox"
	"github.com/openctl/openctl/internal/controller/storage"
	"github.com/openctl/openctl/pkg/protocol"
)

func TestExplicitApplyConvergesAfterReadOnlyAdoption(t *testing.T) {
	ctx := context.Background()
	var liveCPU atomic.Int64
	liveCPU.Store(2)
	pve := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api2/json/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"node": "pve1"}}})
		case "/api2/json/nodes/pve1/qemu":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"name": "runner", "vmid": 101, "status": "stopped"}}})
		case "/api2/json/nodes/pve1/qemu/101/config":
			if r.Method == http.MethodPut {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
					http.Error(w, "form", 400)
					return
				}
				cores, err := strconv.ParseInt(r.Form.Get("cores"), 10, 64)
				if err != nil {
					t.Error(err)
					http.Error(w, "cores", 400)
					return
				}
				liveCPU.Store(cores)
				_ = json.NewEncoder(w).Encode(map[string]any{"data": nil})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"name": "runner", "cores": liveCPU.Load(), "sockets": 1, "memory": "2048"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer pve.Close()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	intent := manifests.New(db)
	vm := &protocol.Resource{APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine", Metadata: protocol.ResourceMetadata{Name: "runner"}, Spec: map[string]any{"node": "pve1", "cpu": map[string]any{"cores": 4}, "memory": map[string]any{"size": 2048}}}
	if err := intent.Adopt(ctx, vm); err != nil {
		t.Fatal(err)
	}
	if liveCPU.Load() != 2 {
		t.Fatal("adoption changed live hardware")
	}
	reg := providers.NewRegistry()
	reg.Register(pmprovider.New(&pmprovider.Config{Endpoint: pve.URL, TokenID: "fixture", TokenSecret: "fixture"}))
	ops := New(db, 0)
	dispatcher := NewDispatcher(ops, reg, intent, 0)
	manifest, err := json.Marshal(vm)
	if err != nil {
		t.Fatal(err)
	}
	op, err := ops.Submit(ctx, &Operation{Type: TypeApply, APIVersion: vm.APIVersion, Kind: vm.Kind, ResourceName: vm.Metadata.Name, ManifestJSON: string(manifest)})
	if err != nil {
		t.Fatal(err)
	}
	runPendingOp(t, dispatcher, ops, op.ID, StatusSucceeded)
	if liveCPU.Load() != 4 {
		t.Fatalf("explicit apply incorrectly treated adopted desired state as already converged: live CPU = %d, want 4", liveCPU.Load())
	}
}
