package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

// TestApplyVM_ExistingResizesInPlace covers the CONTROLLER.md decision to update
// an existing atomic VM in place for the resizable fields (memory, CPU, disk
// growth) rather than no-op: re-applying a VM that already exists with changed
// memory/cpu and a larger disk must push a config PUT and a disk resize — but
// must NOT clone/recreate the VM.
func TestApplyVM_ExistingResizesInPlace(t *testing.T) {
	var mu sync.Mutex
	mutating := map[string]int{} // op -> count
	var configPut map[string]any

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case path == "/api2/json/nodes" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"data":[{"node":"pve1","status":"online"}]}`))
		case path == "/api2/json/nodes/pve1/qemu" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"data":[{"vmid":100,"name":"vm0","status":"stopped","node":"pve1","template":0}]}`))
		case strings.HasSuffix(path, "/config") && r.Method == http.MethodGet:
			// Current config: a 32G scsi0 so the 80G request is a genuine grow.
			_, _ = w.Write([]byte(`{"data":{"scsi0":"local-lvm:vm-100-disk-0,size=32G"}}`))
		case strings.HasSuffix(path, "/clone") && r.Method == http.MethodPost:
			mutating["clone"]++
			_, _ = w.Write([]byte(`{"data":""}`))
		case strings.HasSuffix(path, "/config") && r.Method == http.MethodPut:
			mutating["config-put"]++
			_ = r.ParseForm()
			configPut = map[string]any{}
			for k := range r.Form {
				configPut[k] = r.Form.Get(k)
			}
			_, _ = w.Write([]byte(`{"data":""}`))
		case strings.HasSuffix(path, "/resize") && r.Method == http.MethodPut:
			mutating["resize"]++
			_, _ = w.Write([]byte(`{"data":""}`))
		default:
			_, _ = w.Write([]byte(`{"data":""}`))
		}
	}))
	defer srv.Close()

	h := New(&protocol.ProviderConfig{Endpoint: srv.URL, TokenID: "t", TokenSecret: "s", Node: "pve1"})
	resp, err := h.Handle(context.Background(), &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionApply,
		ResourceType: "VirtualMachine",
		Manifest: &protocol.Resource{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "vm0"},
			Spec: map[string]any{
				"node":   "pve1",
				"cpu":    map[string]any{"cores": float64(8), "sockets": float64(1)},
				"memory": map[string]any{"size": float64(16384)},
				"disks": []any{
					map[string]any{"name": "scsi0", "size": "80G"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("apply existing VM: %v", err)
	}
	if resp.Status != protocol.StatusSuccess {
		t.Fatalf("status = %s (%+v), want success", resp.Status, resp.Error)
	}
	if resp.Resource == nil || resp.Resource.Metadata.Name != "vm0" {
		t.Fatalf("expected the observed vm0 resource, got %+v", resp.Resource)
	}

	mu.Lock()
	defer mu.Unlock()
	if mutating["clone"] != 0 {
		t.Errorf("in-place resize must not clone/recreate; clone called %d times", mutating["clone"])
	}
	if mutating["config-put"] == 0 {
		t.Error("expected a config PUT to update memory/cpu in place")
	}
	if mutating["resize"] == 0 {
		t.Error("expected a disk resize (32G -> 80G)")
	}
	if got := configPut["memory"]; got != "16384" {
		t.Errorf("config PUT memory = %v, want 16384", got)
	}
	if got := configPut["cores"]; got != "8" {
		t.Errorf("config PUT cores = %v, want 8", got)
	}
}
