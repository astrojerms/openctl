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

// TestApplyVM_ExistingIsNoOp guards the locked "apply on existing atomic
// resource = no-op + surface drift" decision (CONTROLLER.md:23) at the handler
// level: re-applying a VM that already exists must NOT mutate it — no clone, no
// config PUT, no disk resize — even when the manifest carries config-worthy
// fields and a disk size that the old (pre-fix) update path would have pushed.
// It must instead return the observed state.
func TestApplyVM_ExistingIsNoOp(t *testing.T) {
	var mu sync.Mutex
	var getVMs int
	mutating := map[string]int{} // path suffix -> count

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case path == "/api2/json/nodes" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"data":[{"node":"pve1","status":"online"}]}`))
		case path == "/api2/json/nodes/pve1/qemu" && r.Method == http.MethodGet:
			getVMs++
			_, _ = w.Write([]byte(`{"data":[{"vmid":100,"name":"vm0","status":"stopped","node":"pve1","template":0}]}`))
		case strings.HasSuffix(path, "/config") && r.Method == http.MethodGet:
			// GetVMConfig — a read, allowed on the no-op path.
			_, _ = w.Write([]byte(`{"data":{}}`))
		case strings.HasSuffix(path, "/clone") && r.Method == http.MethodPost:
			mutating["clone"]++
			_, _ = w.Write([]byte(`{"data":""}`))
		case strings.HasSuffix(path, "/config") && r.Method == http.MethodPut:
			mutating["config-put"]++
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
				"node": "pve1",
				// Config-worthy fields + a sized disk: the old update path would
				// have pushed these via ConfigureVM/ResizeVMDisk.
				"cpus":     float64(8),
				"memoryMB": float64(16384),
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
	for op, n := range mutating {
		if n > 0 {
			t.Errorf("no-op apply performed a mutating call %q (%d times); re-apply of an existing VM must not mutate", op, n)
		}
	}
	// A single VM listing observes existence + state — no redundant second GetVM.
	if getVMs != 1 {
		t.Errorf("qemu list (GetVM) called %d times, want 1 (single observe on the no-op path)", getVMs)
	}
}
