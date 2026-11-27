package proxmox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openctl/openctl/internal/controller/provideridentity"
	"github.com/openctl/openctl/pkg/protocol"
)

type memoryIdentities map[string]*provideridentity.Identity

func (s memoryIdentities) LoadIdentity(_ context.Context, name string) (*provideridentity.Identity, error) {
	return s[name], nil
}
func (s memoryIdentities) SaveIdentity(_ context.Context, name string, id *provideridentity.Identity) error {
	if old := s[name]; old != nil && *old != *id {
		return fmt.Errorf("identity conflict")
	}
	s[name] = id
	return nil
}
func (s memoryIdentities) DeleteIdentity(_ context.Context, name string) error {
	delete(s, name)
	return nil
}

const testVMUUID = "8e0da4e0-01ef-441e-a64a-75db8614a535"

func vmSafetyServer(t *testing.T, vms string, uuid string, writes *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			*writes = append(*writes, r.Method+" "+r.URL.Path)
			_, _ = w.Write([]byte(`{"data":""}`))
			return
		}
		switch r.URL.Path {
		case "/api2/json/nodes":
			_, _ = w.Write([]byte(`{"data":[{"node":"pve1"}]}`))
		case "/api2/json/nodes/pve1/qemu":
			_, _ = w.Write([]byte(`{"data":` + vms + `}`))
		case "/api2/json/nodes/pve1/qemu/101/config", "/api2/json/nodes/pve1/qemu/102/config":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"name": "existing", "smbios1": "uuid=" + uuid, "memory": "2048", "cores": 2, "cipassword": "NEVER-EXPOSE"}})
		default:
			t.Errorf("unexpected read %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

func boundSafetyProvider(t *testing.T, srv *httptest.Server) *Provider {
	t.Helper()
	p := NewMulti(map[string]*Config{"site": {Endpoint: srv.URL}}, "site")
	p.SetIdentityStore(memoryIdentities{"existing": {Context: "site", Endpoint: srv.URL, Node: "pve1", VMID: 101, Identity: "proxmox:pve1:101:" + testVMUUID}})
	return p
}

func TestBoundVMRejectsUUIDReplacementForEveryOrdinaryOperation(t *testing.T) {
	var writes []string
	srv := vmSafetyServer(t, `[{"vmid":101,"name":"existing","status":"stopped"}]`, "1e0da4e0-01ef-441e-a64a-75db8614a535", &writes)
	defer srv.Close()
	p := boundSafetyProvider(t, srv)
	ctx := context.Background()
	manifest := &protocol.Resource{Kind: kindVM, Metadata: protocol.ResourceMetadata{Name: "existing"}, Spec: map[string]any{"context": "site", "node": "pve1"}}
	if _, err := p.Get(ctx, kindVM, "existing"); err == nil {
		t.Error("Get accepted replacement")
	}
	if _, err := p.Apply(ctx, manifest); err == nil {
		t.Error("Apply accepted replacement")
	}
	if err := p.Delete(ctx, kindVM, "existing"); err == nil {
		t.Error("Delete accepted replacement")
	}
	if _, err := p.DoAction(ctx, kindVM, "existing", "start"); err == nil {
		t.Error("action accepted replacement")
	}
	if len(writes) != 0 {
		t.Fatalf("replacement was mutated: %v", writes)
	}
}

func TestBoundDeleteUsesVMIDNotAmbiguousName(t *testing.T) {
	var writes []string
	srv := vmSafetyServer(t, `[{"vmid":102,"name":"existing","status":"stopped"},{"vmid":101,"name":"existing","status":"stopped"}]`, testVMUUID, &writes)
	defer srv.Close()
	p := boundSafetyProvider(t, srv)
	if err := p.Delete(context.Background(), kindVM, "existing"); err != nil {
		t.Fatal(err)
	}
	if len(writes) != 1 || writes[0] != "DELETE /api2/json/nodes/pve1/qemu/101" {
		t.Fatalf("wrong mutation target: %v", writes)
	}
}

func TestUnboundDuplicateNamesNeverRouteOrMutate(t *testing.T) {
	var writes []string
	srv := vmSafetyServer(t, `[{"vmid":101,"name":"existing"},{"vmid":102,"name":"existing"}]`, testVMUUID, &writes)
	defer srv.Close()
	p := New(&Config{Endpoint: srv.URL})
	if _, err := p.Get(context.Background(), kindVM, "existing"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("Get error=%v", err)
	}
	if err := p.Delete(context.Background(), kindVM, "existing"); err == nil {
		t.Fatal("ambiguous deletion allowed")
	}
	if len(writes) != 0 {
		t.Fatalf("ambiguous VM mutated: %v", writes)
	}
}

func TestApplyCannotAdoptUntrackedVM(t *testing.T) {
	var writes []string
	srv := vmSafetyServer(t, `[{"vmid":101,"name":"existing"}]`, testVMUUID, &writes)
	defer srv.Close()
	p := New(&Config{Endpoint: srv.URL})
	p.SetIdentityStore(memoryIdentities{})
	_, err := p.Apply(context.Background(), &protocol.Resource{Kind: kindVM, Metadata: protocol.ResourceMetadata{Name: "existing"}, Spec: map[string]any{"node": "pve1"}})
	if err == nil {
		t.Fatal("Apply implicitly adopted existing VM")
	}
	if len(writes) != 0 {
		t.Fatalf("untracked VM mutated: %v", writes)
	}
}

func TestDiscoveryIsReadOnlyAndExcludesWriteOnlySecrets(t *testing.T) {
	var writes []string
	srv := vmSafetyServer(t, `[{"vmid":101,"name":"existing","status":"stopped"}]`, testVMUUID, &writes)
	defer srv.Close()
	p := NewMulti(map[string]*Config{"site": {Endpoint: srv.URL}}, "site")
	p.SetIdentityStore(memoryIdentities{})
	if err := p.TestConnection(context.Background(), "site"); err != nil {
		t.Fatal(err)
	}
	items, err := p.Discover(context.Background(), "site")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Context != "site" || items[0].Node != "pve1" || items[0].VMID != 101 || !items[0].Adoptable || items[0].Managed || items[0].Identity != "proxmox:pve1:101:"+testVMUUID {
		t.Fatalf("inventory=%+v", items)
	}
	encoded, err := json.Marshal(items[0].Resource)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "NEVER-EXPOSE") || strings.Contains(string(encoded), "password") {
		t.Fatalf("inventory leaked secret: %s", encoded)
	}
	if len(writes) != 0 {
		t.Fatalf("discovery mutated provider: %v", writes)
	}
}

func TestBoundOperationsIgnoreUnrelatedUnavailableNode(t *testing.T) {
	globalReads := 0
	var writes []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			writes = append(writes, r.Method+" "+r.URL.Path)
			_, _ = w.Write([]byte(`{"data":""}`))
			return
		}
		switch r.URL.Path {
		case "/api2/json/nodes":
			globalReads++
			_, _ = w.Write([]byte(`{"data":[{"node":"pve1"},{"node":"offline"}]}`))
		case "/api2/json/nodes/pve1/qemu":
			_, _ = w.Write([]byte(`{"data":[{"vmid":101,"name":"existing","status":"stopped"}]}`))
		case "/api2/json/nodes/offline/qemu":
			http.Error(w, "unrelated node unavailable", http.StatusServiceUnavailable)
		case "/api2/json/nodes/pve1/qemu/101/config":
			_, _ = w.Write([]byte(`{"data":{"smbios1":"uuid=` + testVMUUID + `","cores":2,"memory":"2048"}}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := boundSafetyProvider(t, srv)
	ctx := context.Background()
	if _, err := p.Get(ctx, kindVM, "existing"); err != nil {
		t.Fatalf("bound Get: %v", err)
	}
	manifest := &protocol.Resource{Kind: kindVM, Metadata: protocol.ResourceMetadata{Name: "existing"}, Spec: map[string]any{"context": "site", "node": "pve1"}}
	if _, err := p.Apply(ctx, manifest); err != nil {
		t.Fatalf("bound Apply: %v", err)
	}
	if _, err := p.DoAction(ctx, kindVM, "existing", "console"); err != nil {
		t.Fatalf("bound action: %v", err)
	}
	if err := p.Delete(ctx, kindVM, "existing"); err != nil {
		t.Fatalf("bound Delete: %v", err)
	}
	if globalReads != 0 {
		t.Fatalf("bound operations queried unrelated inventory %d times", globalReads)
	}
	if len(writes) != 1 || writes[0] != "DELETE /api2/json/nodes/pve1/qemu/101" {
		t.Fatalf("wrong mutations: %v", writes)
	}
	if _, err := p.Discover(ctx, "site"); err == nil {
		t.Fatal("unbound discovery accepted partial inventory")
	}
}

func TestBoundNodeOutageIsNotAnAbsentVM(t *testing.T) {
	for _, statusCode := range []int{http.StatusNotFound, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(statusCode), func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("outage triggered mutation: %s", r.Method)
				}
				if r.URL.Path != "/api2/json/nodes/pve1/qemu" {
					t.Errorf("outage lookup escaped recorded node: %s", r.URL.Path)
				}
				http.Error(w, "recorded node unavailable", statusCode)
			}))
			defer srv.Close()
			p := boundSafetyProvider(t, srv)
			if _, err := p.Get(context.Background(), kindVM, "existing"); err == nil || isMissingVM(err) {
				t.Fatalf("outage reported as absence: %v", err)
			}
			if err := p.Delete(context.Background(), kindVM, "existing"); err == nil {
				t.Fatal("outage treated as successful delete")
			}
			if id, _ := p.identities.LoadIdentity(context.Background(), "existing"); id == nil {
				t.Fatal("outage released immutable binding")
			}
		})
	}
}
