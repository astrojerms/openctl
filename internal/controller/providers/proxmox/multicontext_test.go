package proxmox

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/pkg/protocol"
)

// TestHandlerForContext covers endpoint selection without any HTTP: empty
// picks the default, a named context picks itself, and an unknown context
// fails fast so a typo'd spec.context surfaces at apply time.
func TestHandlerForContext(t *testing.T) {
	p := NewMulti(map[string]*Config{
		"siteA": {Endpoint: "https://a"},
		"siteB": {Endpoint: "https://b"},
	}, "siteA")

	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "siteA", false},
		{"siteA", "siteA", false},
		{"siteB", "siteB", false},
		{"bogus", "", true},
	}
	for _, c := range cases {
		_, name, err := p.handlerForContext(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("handlerForContext(%q): want error, got nil", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("handlerForContext(%q): %v", c.in, err)
			continue
		}
		if name != c.want {
			t.Errorf("handlerForContext(%q) = %q, want %q", c.in, name, c.want)
		}
	}
}

// TestNewSingleContextDefaults ensures the single-endpoint constructor keeps
// working: the sole context is the default, selected by an empty spec.context.
func TestNewSingleContextDefaults(t *testing.T) {
	p := New(&Config{Endpoint: "https://only"})
	_, name, err := p.handlerForContext("")
	if err != nil {
		t.Fatalf("handlerForContext(\"\"): %v", err)
	}
	if name != "" {
		t.Errorf("default context = %q, want \"\" (the sole context)", name)
	}
}

// TestApplyUnknownContextErrors: a VM naming a context that isn't configured
// is rejected before any endpoint call.
func TestApplyUnknownContextErrors(t *testing.T) {
	p := NewMulti(map[string]*Config{"siteA": {Endpoint: "https://a"}}, "siteA")
	_, err := p.Apply(context.Background(), &protocol.Resource{
		Kind:     "VirtualMachine",
		Metadata: protocol.ResourceMetadata{Name: "vm0"},
		Spec:     map[string]any{"context": "nope"},
	})
	if err == nil || !strings.Contains(err.Error(), "no configured context") {
		t.Errorf("want no-configured-context error, got %v", err)
	}
}

// TestListMergesAcrossEndpoints: List(VirtualMachine) returns VMs from every
// configured endpoint, merged into one result set.
func TestListMergesAcrossEndpoints(t *testing.T) {
	srvA := mockProxmox(t, map[string]string{
		"/api2/json/nodes":           `{"data":[{"node":"pveA","status":"online"}]}`,
		"/api2/json/nodes/pveA/qemu": `{"data":[{"vmid":100,"name":"web-01","status":"running","node":"pveA","template":0}]}`,
	})
	defer srvA.Close()
	srvB := mockProxmox(t, map[string]string{
		"/api2/json/nodes":           `{"data":[{"node":"pveB","status":"online"}]}`,
		"/api2/json/nodes/pveB/qemu": `{"data":[{"vmid":200,"name":"cache-01","status":"running","node":"pveB","template":0}]}`,
	})
	defer srvB.Close()

	p := NewMulti(map[string]*Config{
		"siteA": {Endpoint: srvA.URL, TokenID: "t", TokenSecret: "s"},
		"siteB": {Endpoint: srvB.URL, TokenID: "t", TokenSecret: "s"},
	}, "siteA")

	vms, err := p.List(context.Background(), "VirtualMachine")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := map[string]bool{}
	for _, vm := range vms {
		got[vm.Metadata.Name] = true
	}
	if !got["web-01"] || !got["cache-01"] {
		t.Errorf("merged list = %v, want both web-01 and cache-01", got)
	}
}

// TestGetRoutesToOwningEndpoint: a VM present only on the second endpoint is
// still found — locate scans past the endpoint that lacks it.
func TestGetRoutesToOwningEndpoint(t *testing.T) {
	srvA := mockProxmox(t, map[string]string{
		"/api2/json/nodes":           `{"data":[{"node":"pveA","status":"online"}]}`,
		"/api2/json/nodes/pveA/qemu": `{"data":[]}`,
	})
	defer srvA.Close()
	srvB := mockProxmox(t, map[string]string{
		"/api2/json/nodes":           `{"data":[{"node":"pveB","status":"online"}]}`,
		"/api2/json/nodes/pveB/qemu": `{"data":[{"vmid":200,"name":"cache-01","status":"running","node":"pveB","template":0}]}`,
	})
	defer srvB.Close()

	p := NewMulti(map[string]*Config{
		"siteA": {Endpoint: srvA.URL, TokenID: "t", TokenSecret: "s"},
		"siteB": {Endpoint: srvB.URL, TokenID: "t", TokenSecret: "s"},
	}, "siteA")

	res, err := p.Get(context.Background(), "VirtualMachine", "cache-01")
	if err != nil {
		t.Fatalf("Get(cache-01): %v", err)
	}
	if res.Metadata.Name != "cache-01" {
		t.Errorf("got %q, want cache-01", res.Metadata.Name)
	}
	// The scan should have cached the owning context for later reads.
	if v, ok := p.index.Load("cache-01"); !ok || v.(string) != "siteB" {
		t.Errorf("index[cache-01] = %v (ok=%v), want siteB", v, ok)
	}
}

// TestGetNotFoundAcrossAllEndpoints: absent from every endpoint maps to the
// NotFound sentinel.
func TestGetNotFoundAcrossAllEndpoints(t *testing.T) {
	routes := map[string]string{
		"/api2/json/nodes":          `{"data":[{"node":"pve","status":"online"}]}`,
		"/api2/json/nodes/pve/qemu": `{"data":[]}`,
	}
	srvA := mockProxmox(t, routes)
	defer srvA.Close()
	srvB := mockProxmox(t, routes)
	defer srvB.Close()

	p := NewMulti(map[string]*Config{
		"siteA": {Endpoint: srvA.URL, TokenID: "t", TokenSecret: "s"},
		"siteB": {Endpoint: srvB.URL, TokenID: "t", TokenSecret: "s"},
	}, "siteA")

	_, err := p.Get(context.Background(), "VirtualMachine", "ghost")
	var nf *providers.NotFoundError
	if !errors.As(err, &nf) {
		t.Errorf("want providers.NotFoundError, got %T: %v", err, err)
	}
}
