package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListVMsFailsClosedWhenAnyNodeInventoryFails(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api2/json/nodes":
			_, _ = w.Write([]byte(`{"data":[{"node":"healthy"},{"node":"unavailable"}]}`))
		case "/api2/json/nodes/healthy/qemu":
			_, _ = w.Write([]byte(`{"data":[{"vmid":101,"name":"visible"}]}`))
		case "/api2/json/nodes/unavailable/qemu":
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected inventory path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := trustedTestClient(t, srv, "test", "secret")
	vms, err := c.ListVMs(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unavailable") || vms != nil {
		t.Fatalf("partial inventory accepted: vms=%v err=%v", vms, err)
	}
	if _, err := c.GetVM(context.Background(), "missing-on-unavailable"); err == nil {
		t.Fatal("missing VM lookup hid partial inventory outage")
	}
}
