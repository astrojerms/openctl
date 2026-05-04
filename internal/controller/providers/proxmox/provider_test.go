package proxmox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/pkg/protocol"
)

// mockProxmox returns a TLS test server that serves canned responses for
// each path the proxmox client hits. Returning empty 200s for unknown paths
// keeps tests focused on the calls they care about.
func mockProxmox(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, body := range routes {
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		})
	}
	return httptest.NewTLSServer(mux)
}

func TestProviderName(t *testing.T) {
	p := New(&Config{})
	if p.Name() != "proxmox" {
		t.Errorf("Name = %q, want proxmox", p.Name())
	}
	if len(p.Kinds()) != 1 || p.Kinds()[0] != "VirtualMachine" {
		t.Errorf("Kinds = %v, want [VirtualMachine]", p.Kinds())
	}
}

func TestProviderRejectsWrongKind(t *testing.T) {
	p := New(&Config{})
	for _, op := range []func() error{
		func() error { _, e := p.Apply(context.Background(), &protocol.Resource{Kind: "Other"}); return e },
		func() error { _, e := p.Get(context.Background(), "Other", "x"); return e },
		func() error { _, e := p.List(context.Background(), "Other"); return e },
		func() error { return p.Delete(context.Background(), "Other", "x") },
	} {
		if err := op(); err == nil || !strings.Contains(err.Error(), "does not handle kind") {
			t.Errorf("want kind-validation error, got %v", err)
		}
	}
}

func TestProviderListReturnsVMs(t *testing.T) {
	srv := mockProxmox(t, map[string]string{
		"/api2/json/nodes":           `{"data":[{"node":"pve1","status":"online"}]}`,
		"/api2/json/nodes/pve1/qemu": `{"data":[{"vmid":100,"name":"web-01","status":"running","node":"pve1","template":0},{"vmid":200,"name":"tpl-x","status":"stopped","node":"pve1","template":1}]}`,
	})
	defer srv.Close()

	p := New(&Config{
		Endpoint:    srv.URL,
		TokenID:     "fake@pam!t",
		TokenSecret: "secret",
	})

	resources, err := p.List(context.Background(), "VirtualMachine")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Templates should be filtered out by listVMs.
	if len(resources) != 1 {
		t.Fatalf("List returned %d resources, want 1 (templates filtered)", len(resources))
	}
	if resources[0].Metadata.Name != "web-01" {
		t.Errorf("name = %q, want web-01", resources[0].Metadata.Name)
	}
}

func TestProviderGetNotFoundMapsToSentinel(t *testing.T) {
	// Empty VM list → GetVM returns "VM not found" error → handler returns
	// NOT_FOUND → provider should map to providers.NotFoundError.
	srv := mockProxmox(t, map[string]string{
		"/api2/json/nodes":           `{"data":[{"node":"pve1","status":"online"}]}`,
		"/api2/json/nodes/pve1/qemu": `{"data":[]}`,
	})
	defer srv.Close()

	p := New(&Config{Endpoint: srv.URL, TokenID: "t", TokenSecret: "s"})

	_, err := p.Get(context.Background(), "VirtualMachine", "missing")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	var nf *providers.NotFoundError
	if !errors.As(err, &nf) {
		t.Errorf("want providers.NotFoundError, got %T: %v", err, err)
	}
}

func TestProviderDeleteIsIdempotent(t *testing.T) {
	srv := mockProxmox(t, map[string]string{
		"/api2/json/nodes":           `{"data":[{"node":"pve1","status":"online"}]}`,
		"/api2/json/nodes/pve1/qemu": `{"data":[]}`,
	})
	defer srv.Close()

	p := New(&Config{Endpoint: srv.URL, TokenID: "t", TokenSecret: "s"})

	if err := p.Delete(context.Background(), "VirtualMachine", "missing"); err != nil {
		t.Errorf("delete on missing VM should be idempotent, got: %v", err)
	}
}
