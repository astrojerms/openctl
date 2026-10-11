package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/openctl/openctl/internal/controller/providers"
	pmprovider "github.com/openctl/openctl/internal/controller/providers/proxmox"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

func TestForgetWaitsForAnAlreadyStartedRuntimeAction(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	finish := make(chan struct{})
	var release sync.Once
	releaseAction := func() { release.Do(func() { close(finish) }) }
	pve := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api2/json/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"node": "pve1"}}})
		case "/api2/json/nodes/pve1/qemu":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"name": "existing", "vmid": 101, "status": "stopped"}}})
		case "/api2/json/nodes/pve1/qemu/101/config":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"name": "existing", "cores": 2, "memory": "2048", "smbios1": "uuid=8e0da4e0-01ef-441e-a64a-75db8614a535"}})
		case "/api2/json/nodes/pve1/qemu/101/status/start":
			close(started)
			<-finish
			_ = json.NewEncoder(w).Encode(map[string]any{"data": "UPID:runtime-action"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer pve.Close()
	defer releaseAction()
	reg := providers.NewRegistry()
	reg.Register(pmprovider.NewMulti(map[string]*pmprovider.Config{"source": {Endpoint: pve.URL, TokenID: "fixture", TokenSecret: "fixture"}}, "source"))
	h, _, _ := migrationFixture(t, reg)
	vm := migrationVM()
	if err := h.mirror.Save(ctx, vm); err != nil {
		t.Fatal(err)
	}
	exported, err := h.ExportResource(ctx, &apiv1.ExportResourceRequest{ApiVersion: vm.APIVersion, Kind: vm.Kind, Name: vm.Metadata.Name})
	if err != nil {
		t.Fatal(err)
	}
	actionResult := make(chan error, 1)
	go func() {
		_, err := h.InvokeAction(ctx, &apiv1.InvokeActionRequest{ApiVersion: vm.APIVersion, Kind: vm.Kind, ResourceName: vm.Metadata.Name, Action: "start"})
		actionResult <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime action did not start")
	}
	forgetResult := make(chan error, 1)
	go func() {
		_, err := h.ForgetResource(ctx, &apiv1.ForgetResourceRequest{Bundle: exported.Bundle})
		forgetResult <- err
	}()
	select {
	case err := <-forgetResult:
		t.Fatalf("ownership release returned while an earlier VM action was still in flight: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	releaseAction()
	select {
	case err := <-actionResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runtime action did not finish")
	}
	select {
	case err := <-forgetResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ownership release did not finish after the VM action")
	}
	if desired, err := h.manifests.Load(ctx, vm.APIVersion, vm.Kind, vm.Metadata.Name); err != nil || desired != nil {
		t.Fatalf("release did not remove managed intent: %v, %v", desired, err)
	}
}
