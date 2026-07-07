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

func TestParseProxmoxSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		bad  bool
	}{
		{"50G", 50 << 30, false},
		{"512M", 512 << 20, false},
		{"1T", 1 << 40, false},
		{"1024K", 1024 << 10, false},
		{"0", 0, false},
		{"1073741824", 1073741824, false}, // bare bytes
		{" 32G ", 32 << 30, false},
		{"", 0, true},
		{"abc", 0, true},
		{"-5G", 0, true},
	}
	for _, c := range cases {
		got, err := parseProxmoxSize(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("parseProxmoxSize(%q) = %d, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseProxmoxSize(%q) error: %v", c.in, err)
		} else if got != c.want {
			t.Errorf("parseProxmoxSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestCurrentDiskSize(t *testing.T) {
	raw := map[string]any{
		"scsi0": "local-lvm:vm-100-disk-0,size=32G,ssd=1",
		"ide2":  "local-lvm:cloudinit,media=cdrom", // no size=
		"net0":  12345,                             // non-string
	}
	if n, ok := currentDiskSize(raw, "scsi0"); !ok || n != 32<<30 {
		t.Errorf("scsi0 = (%d,%v), want (%d,true)", n, ok, int64(32)<<30)
	}
	if _, ok := currentDiskSize(raw, "ide2"); ok {
		t.Error("ide2 has no size= — want ok=false")
	}
	if _, ok := currentDiskSize(raw, "net0"); ok {
		t.Error("net0 is non-string — want ok=false")
	}
	if _, ok := currentDiskSize(raw, "scsi9"); ok {
		t.Error("scsi9 absent — want ok=false")
	}
}

// resizeServer builds a fake Proxmox that reports one existing VM (vmid 100)
// whose scsi0 is currentDiskSize, and records mutating calls.
func resizeServer(t *testing.T, currentDiskSize string, mutating map[string]int, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case p == "/api2/json/nodes" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"data":[{"node":"pve1","status":"online"}]}`))
		case p == "/api2/json/nodes/pve1/qemu" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"data":[{"vmid":100,"name":"vm0","status":"stopped","node":"pve1","template":0}]}`))
		case strings.HasSuffix(p, "/config") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"data":{"scsi0":"local-lvm:vm-100-disk-0,size=` + currentDiskSize + `"}}`))
		case strings.HasSuffix(p, "/resize") && r.Method == http.MethodPut:
			mutating["resize"]++
			_, _ = w.Write([]byte(`{"data":""}`))
		case strings.HasSuffix(p, "/config") && r.Method == http.MethodPut:
			mutating["config-put"]++
			_, _ = w.Write([]byte(`{"data":""}`))
		default:
			_, _ = w.Write([]byte(`{"data":""}`))
		}
	}))
}

func applyDiskSize(t *testing.T, srv *httptest.Server, size string) (*protocol.Response, error) {
	t.Helper()
	h := New(&protocol.ProviderConfig{Endpoint: srv.URL, TokenID: "t", TokenSecret: "s", Node: "pve1"})
	return h.Handle(context.Background(), &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionApply,
		ResourceType: "VirtualMachine",
		Manifest: &protocol.Resource{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "vm0"},
			Spec: map[string]any{
				"node":  "pve1",
				"disks": []any{map[string]any{"name": "scsi0", "size": size}},
			},
		},
	})
}

// A disk already at its target size is left alone — no resize call.
func TestResizeDisks_SkipsUnchanged(t *testing.T) {
	var mu sync.Mutex
	m := map[string]int{}
	srv := resizeServer(t, "32G", m, &mu)
	defer srv.Close()
	if _, err := applyDiskSize(t, srv, "32G"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if m["resize"] != 0 {
		t.Errorf("resize called %d times for an unchanged disk, want 0", m["resize"])
	}
}

// Growing a disk issues a resize.
func TestResizeDisks_Grows(t *testing.T) {
	var mu sync.Mutex
	m := map[string]int{}
	srv := resizeServer(t, "32G", m, &mu)
	defer srv.Close()
	if _, err := applyDiskSize(t, srv, "64G"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if m["resize"] != 1 {
		t.Errorf("resize called %d times growing 32G->64G, want 1", m["resize"])
	}
}

// A shrink request is rejected with a clear error and no resize call.
func TestResizeDisks_RejectsShrink(t *testing.T) {
	var mu sync.Mutex
	m := map[string]int{}
	srv := resizeServer(t, "64G", m, &mu)
	defer srv.Close()
	_, err := applyDiskSize(t, srv, "32G")
	if err == nil || !strings.Contains(err.Error(), "shrink") {
		t.Fatalf("apply shrink: err = %v, want a shrink error", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if m["resize"] != 0 {
		t.Errorf("resize called %d times on a rejected shrink, want 0", m["resize"])
	}
}
