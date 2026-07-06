package proxmox

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

// TestApplyReturnsObservedVM guards the Provider contract that Apply returns
// the observed resource. The underlying handler reports apply/create success
// without echoing a Resource, so Apply reads the VM back before returning. A
// nil result here regresses that contract (and would fail the shared
// providertest conformance battery once proxmox is bound to it).
//
// This drives the apply-on-existing path: the VM "vm0" already exists, so the
// handler's applyVM takes the update branch (no clone), and Apply then reads
// it back. A permissive catch-all keeps incidental calls (config, guest-agent
// IP) benign so the test asserts only the return-value contract.
func TestApplyReturnsObservedVM(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api2/json/nodes":
			_, _ = io.WriteString(w, `{"data":[{"node":"pve1","status":"online"}]}`)
		case "/api2/json/nodes/pve1/qemu":
			_, _ = io.WriteString(w, `{"data":[{"vmid":100,"name":"vm0","status":"running","node":"pve1","template":0}]}`)
		default:
			// config / resize / agent-network-get / etc. — benign success so
			// the update path completes without hitting an unhandled route.
			_, _ = io.WriteString(w, `{"data":""}`)
		}
	}))
	defer srv.Close()

	p := New(&Config{Endpoint: srv.URL, TokenID: "t", TokenSecret: "s"})
	got, err := p.Apply(context.Background(), &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       kindVM,
		Metadata:   protocol.ResourceMetadata{Name: "vm0"},
		Spec:       map[string]any{"node": "pve1"},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got == nil {
		t.Fatal("Apply returned a nil resource; the Provider contract requires the observed resource")
	}
	if got.Kind != kindVM || got.Metadata.Name != "vm0" {
		t.Errorf("Apply returned %s/%s, want %s/vm0", got.Kind, got.Metadata.Name, kindVM)
	}
	if got.APIVersion == "" {
		t.Error("Apply result APIVersion is empty; providers must stamp it")
	}
}
