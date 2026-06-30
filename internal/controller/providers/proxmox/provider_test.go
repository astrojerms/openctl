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
	kinds := p.Kinds()
	want := map[string]bool{"VirtualMachine": false, "ProxmoxNode": false}
	for _, k := range kinds {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected kind %q in Kinds()", k)
			continue
		}
		want[k] = true
	}
	for k, seen := range want {
		if !seen {
			t.Errorf("Kinds() missing %q (got %v)", k, kinds)
		}
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

func TestProviderRejectsApplyAndDeleteOnNode(t *testing.T) {
	p := New(&Config{})
	if _, err := p.Apply(context.Background(), &protocol.Resource{Kind: "ProxmoxNode"}); err == nil || !strings.Contains(err.Error(), "observed-only") {
		t.Errorf("Apply on ProxmoxNode: want observed-only error, got %v", err)
	}
	if err := p.Delete(context.Background(), "ProxmoxNode", "pve1"); err == nil || !strings.Contains(err.Error(), "observed-only") {
		t.Errorf("Delete on ProxmoxNode: want observed-only error, got %v", err)
	}
}

func TestProviderListNodes(t *testing.T) {
	srv := mockProxmox(t, map[string]string{
		"/api2/json/nodes": `{"data":[
			{"node":"pve1","status":"online","maxcpu":8,"cpu":0.12,"maxmem":34359738368,"mem":8589934592,"maxdisk":2147483648000,"disk":536870912000,"uptime":12345,"level":""},
			{"node":"pve2","status":"online","maxcpu":4,"cpu":0.05,"maxmem":17179869184,"mem":4294967296,"maxdisk":1073741824000,"disk":107374182400,"uptime":54321,"level":""}
		]}`,
	})
	defer srv.Close()

	p := New(&Config{Endpoint: srv.URL, TokenID: "t", TokenSecret: "s"})

	got, err := p.List(context.Background(), "ProxmoxNode")
	if err != nil {
		t.Fatalf("List ProxmoxNode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d nodes, want 2", len(got))
	}
	if got[0].Metadata.Name != "pve1" || got[1].Metadata.Name != "pve2" {
		t.Errorf("node names = %q,%q, want pve1,pve2", got[0].Metadata.Name, got[1].Metadata.Name)
	}
	if state, _ := got[0].Status["state"].(string); state != "online" {
		t.Errorf("pve1 state = %q, want online", state)
	}
	cpu, _ := got[0].Status["cpu"].(map[string]any)
	if cpu == nil || cpu["cores"] != 8 {
		t.Errorf("pve1 cpu.cores = %v, want 8", cpu)
	}
}

func TestProviderGetNodeNotFound(t *testing.T) {
	srv := mockProxmox(t, map[string]string{
		"/api2/json/nodes": `{"data":[{"node":"pve1","status":"online"}]}`,
	})
	defer srv.Close()

	p := New(&Config{Endpoint: srv.URL, TokenID: "t", TokenSecret: "s"})

	_, err := p.Get(context.Background(), "ProxmoxNode", "missing")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	var nf *providers.NotFoundError
	if !errors.As(err, &nf) {
		t.Errorf("want providers.NotFoundError, got %T: %v", err, err)
	}
}

func TestProviderChildrenOfNodeReturnsVMs(t *testing.T) {
	srv := mockProxmox(t, map[string]string{
		"/api2/json/nodes": `{"data":[{"node":"pve1","status":"online"},{"node":"pve2","status":"online"}]}`,
		"/api2/json/nodes/pve1/qemu": `{"data":[
			{"vmid":100,"name":"web-01","status":"running","node":"pve1","template":0},
			{"vmid":101,"name":"db-01","status":"running","node":"pve1","template":0}
		]}`,
		"/api2/json/nodes/pve2/qemu": `{"data":[
			{"vmid":200,"name":"cache-01","status":"running","node":"pve2","template":0}
		]}`,
	})
	defer srv.Close()

	p := New(&Config{Endpoint: srv.URL, TokenID: "t", TokenSecret: "s"})

	children := p.ChildrenOf("ProxmoxNode", "pve1")
	if len(children) != 2 {
		t.Fatalf("ChildrenOf(pve1) returned %d, want 2 (web-01, db-01)", len(children))
	}
	names := map[string]bool{}
	for _, c := range children {
		if c.APIVersion != "proxmox.openctl.io/v1" || c.Kind != "VirtualMachine" {
			t.Errorf("child apiVersion/kind = %q/%q, want proxmox.openctl.io/v1/VirtualMachine", c.APIVersion, c.Kind)
		}
		names[c.Name] = true
	}
	if !names["web-01"] || !names["db-01"] {
		t.Errorf("missing expected children, got %v", names)
	}

	if got := p.ChildrenOf("ProxmoxNode", "pve2"); len(got) != 1 || got[0].Name != "cache-01" {
		t.Errorf("ChildrenOf(pve2) = %v, want [cache-01]", got)
	}

	if got := p.ChildrenOf("VirtualMachine", "web-01"); got != nil {
		t.Errorf("ChildrenOf on non-Node kind should return nil, got %v", got)
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
