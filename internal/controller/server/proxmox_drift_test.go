package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/openctl/openctl/internal/controller/providers"
	pmprovider "github.com/openctl/openctl/internal/controller/providers/proxmox"
	"github.com/openctl/openctl/internal/controller/reconciler"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
)

// Exercise real Proxmox decoding and observation through the controller's
// consumers. Inventory deliberately differs from configured hardware, as it
// does after a running VM's configuration changes without a reboot.
func TestProxmoxDriftUsesLiveConfiguration(t *testing.T) {
	var mu sync.Mutex
	config := map[string]any{
		"name": "dev-system", "cores": 8, "sockets": "1", "memory": "10240",
		"scsi0": "local-lvm:vm-101-disk-0,size=300G",
		"ide2":  "local-lvm:vm-101-cloudinit,media=cdrom,size=4M",
		"net0":  "virtio=BC:24:11:CC:5D:77,bridge=vmbr0",
		"agent": "1", "ciuser": "ubuntu", "ipconfig0": "ip=dhcp",
		"sshkeys": url.PathEscape("ssh-ed25519 AAAA development-key"),
	}
	configUnavailable := false
	pve := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			t.Errorf("unexpected mutation: %s %s", req.Method, req.URL.Path)
			http.Error(w, "read-only fixture", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/api2/json/nodes":
			_, _ = w.Write([]byte(`{"data":[{"node":"pve1"}]}`))
		case "/api2/json/nodes/pve1/qemu":
			_, _ = w.Write([]byte(`{"data":[{"vmid":101,"name":"dev-system","status":"stopped","cpus":2,"maxmem":2147483648}]}`))
		case "/api2/json/nodes/pve1/qemu/101/config":
			mu.Lock()
			defer mu.Unlock()
			if configUnavailable {
				http.Error(w, "configuration unavailable", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": config})
		default:
			t.Errorf("unexpected read: %s", req.URL.Path)
			http.NotFound(w, req)
		}
	}))
	defer pve.Close()

	reg := providers.NewRegistry()
	reg.Register(pmprovider.New(&pmprovider.Config{Endpoint: pve.URL, TokenID: "test", TokenSecret: "test"}))
	addr, mat, store := startTestServerWithManifests(t, reg)
	conn := dialTestServer(t, addr, mat.CACertPath)
	defer func() { _ = conn.Close() }()
	desired := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine",
		Metadata: protocol.ResourceMetadata{
			Name: "dev-system", Annotations: map[string]string{"openctl.io/autoReconcile": "true"},
		},
		Spec: map[string]any{
			"context": "homelab", "node": "pve1", "template": map[string]any{"name": "ubuntu"},
			"startOnCreate": true,
			"cpu":           map[string]any{"cores": 8, "sockets": 1}, "memory": map[string]any{"size": 10240},
			"disks":    []any{map[string]any{"name": "scsi0", "storage": "local-lvm", "size": "307200M"}},
			"networks": []any{map[string]any{"name": "net0", "bridge": "vmbr0", "model": "virtio", "firewall": false}},
			"agent":    map[string]any{"enabled": true},
			"cloudInit": map[string]any{
				"user": "ubuntu", "sshKeys": []any{"ssh-ed25519 AAAA development-key"},
				"ipConfig": map[string]any{"net0": map[string]any{"ip": "dhcp"}},
				"password": map[string]any{"$secret": "initial-password"},
				"packages": []any{"build-essential"}, "runcmd": []any{"true"},
			},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := store.Save(ctx, desired); err != nil {
		t.Fatal(err)
	}
	client := apiv1.NewResourceServiceClient(conn)
	get := func() *apiv1.Resource {
		t.Helper()
		response, err := client.Get(ctx, &apiv1.GetRequest{ApiVersion: desired.APIVersion, Kind: desired.Kind, Name: desired.Metadata.Name})
		if err != nil {
			t.Fatal(err)
		}
		return response.GetResource()
	}
	list := func() *apiv1.Resource {
		t.Helper()
		response, err := client.List(ctx, &apiv1.ListRequest{ApiVersion: desired.APIVersion, Kind: desired.Kind})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.GetResources()) != 1 {
			t.Fatalf("managed inventory = %v, want dev-system only", response.GetResources())
		}
		return response.GetResources()[0]
	}
	for _, read := range []func() *apiv1.Resource{get, list} {
		if resource := read(); len(resource.GetDrift()) != 0 {
			t.Fatalf("configured VM reported false drift: %v", resource.GetDrift())
		}
	}

	applies := 0
	rec := reconciler.New(reg, store, 0).WithAutoApply(func(context.Context, *protocol.Resource) error {
		applies++
		return nil
	})
	rec.ReconcileOnce(ctx)
	if applies != 0 {
		t.Fatal("matching live config must not trigger automatic apply")
	}

	stream, err := client.Watch(ctx, &apiv1.WatchRequest{ApiVersion: desired.APIVersion, Kind: desired.Kind, Name: desired.Metadata.Name})
	if err != nil {
		t.Fatal(err)
	}
	first, err := stream.Recv()
	if err != nil || first.GetType() != apiv1.WatchEvent_ADDED || len(first.GetResource().GetDrift()) != 0 {
		t.Fatalf("initial watch snapshot = %v, %v; want clean ADDED", first, err)
	}
	mu.Lock()
	config["scsi0"] = "local-lvm:vm-101-disk-0,size=320G"
	mu.Unlock()
	changed, err := stream.Recv()
	if err != nil || changed.GetType() != apiv1.WatchEvent_MODIFIED {
		t.Fatalf("live disk change = %v, %v; want MODIFIED", changed, err)
	}
	for _, resource := range []*apiv1.Resource{changed.GetResource(), get(), list()} {
		drift := resource.GetDrift()
		if len(drift) != 1 || drift[0].GetPath() != "spec.disks[0].size" {
			t.Fatalf("live disk change must remain visible: %v", drift)
		}
	}
	rec.ReconcileOnce(ctx)
	if applies != 1 {
		t.Fatalf("actual live config drift triggered %d automatic applies, want 1", applies)
	}

	// A failed configuration read is an outage, not missing hardware or a
	// deleted VM. Get/List fail, reconciliation does not enqueue mutations,
	// and a name-scoped Watch terminates as unavailable instead of DELETED.
	mu.Lock()
	configUnavailable = true
	mu.Unlock()
	if _, err := client.Get(ctx, &apiv1.GetRequest{ApiVersion: desired.APIVersion, Kind: desired.Kind, Name: desired.Metadata.Name}); status.Code(err) != codes.Internal {
		t.Fatalf("configuration outage on Get: %v", err)
	}
	if _, err := client.List(ctx, &apiv1.ListRequest{ApiVersion: desired.APIVersion, Kind: desired.Kind}); status.Code(err) != codes.Internal {
		t.Fatalf("configuration outage on List: %v", err)
	}
	rec.ReconcileOnce(ctx)
	if applies != 1 {
		t.Fatal("unavailable observations must not trigger automatic apply")
	}
	if event, err := stream.Recv(); status.Code(err) != codes.Unavailable {
		t.Fatalf("configuration outage on Watch: event=%v error=%v; want Unavailable, never DELETED", event, err)
	}
}
