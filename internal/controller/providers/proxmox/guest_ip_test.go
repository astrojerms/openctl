package proxmox

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
	pmhandler "github.com/openctl/openctl/pkg/proxmox/handler"
)

func TestProviderGuestIPObservation(t *testing.T) {
	const validAgent = `{"data":{"result":[{"name":"ens18","hardware-address":"aa:bb:cc:dd:ee:ff","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"10.0.0.8"}]}]}}`
	const validConfig = `{"data":{"net0":"virtio=AA:BB:CC:DD:EE:FF,bridge=vmbr0","memory":4096}}`
	const poison = "POISON_https://private.example/token=secret"
	for _, tc := range []struct {
		name       string
		agent      string
		want       string
		code       string
		config     string
		vmState    string
		httpStatus int
	}{
		{name: "Docker bridge first", agent: `{"data":{"result":[{"name":"docker0","hardware-address":"02:42:ac:11:00:01","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"172.17.0.1"}]},{"name":"ens18","hardware-address":"aa:bb:cc:dd:ee:ff","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"10.0.0.8"}]}]}}`, want: "10.0.0.8", code: "guest_ip_available"},
		{name: "ambiguous guest address", agent: `{"data":{"result":[{"name":"ens18","hardware-address":"aa:bb:cc:dd:ee:ff","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"10.0.0.8"},{"ip-address-type":"ipv4","ip-address":"10.0.0.9"}]}]}}`, code: "guest_ip_ambiguous"},
		{name: "missing MAC evidence", agent: `{"data":{"result":[{"name":"ens18","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"10.0.0.8"}]}]}}`, code: "guest_ip_unavailable"},
		{name: "missing configured NIC", config: `{"data":{"memory":4096}}`, code: "vm_nic_unavailable"},
		{name: "stopped VM", vmState: "stopped", code: "vm_not_running"},
		{name: "agent read denied", agent: poison, httpStatus: http.StatusForbidden, code: "guest_agent_unavailable"},
		{name: "agent invalid JSON", agent: `{"data":` + poison, code: "guest_agent_invalid_response"},
	} {
		for _, api := range []string{"native", "legacy"} {
			t.Run(tc.name+"/"+api, func(t *testing.T) {
				var recovered atomic.Bool
				var agentCalls, configCalls, requests atomic.Int32
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Method != http.MethodGet {
						t.Errorf("unexpected mutation: %s %s", r.Method, r.URL.Path)
						http.Error(w, "read only", http.StatusMethodNotAllowed)
						return
					}
					switch r.URL.Path {
					case "/api2/json/nodes":
						_, _ = io.WriteString(w, `{"data":[{"node":"pve1"}]}`)
					case "/api2/json/nodes/pve1/qemu":
						state := tc.vmState
						if state == "" || recovered.Load() {
							state = "running"
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"vmid": 101, "name": "gpu", "status": state}}})
					case "/api2/json/nodes/pve1/qemu/101/config":
						configCalls.Add(1)
						config := tc.config
						if config == "" || recovered.Load() {
							config = validConfig
						}
						_, _ = io.WriteString(w, config)
					case "/api2/json/nodes/pve1/qemu/101/agent/network-get-interfaces":
						agentCalls.Add(1)
						if recovered.Load() {
							_, _ = io.WriteString(w, validAgent)
							return
						}
						if tc.httpStatus != 0 {
							w.WriteHeader(tc.httpStatus)
						}
						_, _ = io.WriteString(w, tc.agent)
					default:
						t.Errorf("unexpected request: %s", r.URL.Path)
						http.NotFound(w, r)
					}
				}))
				defer server.Close()
				config := &Config{Endpoint: server.URL, CAFile: fixtureCA(t, server), TokenID: "t", TokenSecret: "s"}
				provider := mustNewProvider(t, config)
				handler, err := pmhandler.New(config)
				if err != nil {
					t.Fatal(err)
				}
				read := func() (*protocol.Resource, error) {
					if api == "native" {
						return provider.Get(context.Background(), kindVM, "gpu")
					}
					response, err := handler.Handle(context.Background(), &protocol.Request{Action: protocol.ActionGet, ResourceType: kindVM, ResourceName: "gpu"})
					if err != nil {
						return nil, err
					}
					return response.Resource, nil
				}
				resource, err := read()
				if err != nil {
					t.Fatal(err)
				}
				assertGuestIPObservation(t, resource, tc.code, tc.want)
				wantAgentCalls := int32(1)
				if tc.code == "vm_not_running" || tc.code == "vm_nic_unavailable" {
					wantAgentCalls = 0
				}
				if agentCalls.Load() != wantAgentCalls || configCalls.Load() != 1 || requests.Load() != 3+wantAgentCalls {
					t.Fatalf("Get reads: agent=%d config=%d total=%d", agentCalls.Load(), configCalls.Load(), requests.Load())
				}

				var listed []*protocol.Resource
				if api == "native" {
					listed, err = provider.List(context.Background(), kindVM)
				} else {
					var response *protocol.Response
					response, err = handler.Handle(context.Background(), &protocol.Request{Action: protocol.ActionList, ResourceType: kindVM})
					if response != nil {
						listed = response.Resources
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(listed) != 1 {
					t.Fatalf("List returned %d resources, want 1", len(listed))
				}
				for _, key := range []string{"ip", "ipObservation"} {
					if _, present := listed[0].Status[key]; present {
						t.Fatalf("List unexpectedly published %s", key)
					}
				}
				if agentCalls.Load() != wantAgentCalls || configCalls.Load() != 2 || requests.Load() != 6+wantAgentCalls {
					t.Fatalf("List added unexpected reads: agent=%d config=%d total=%d", agentCalls.Load(), configCalls.Load(), requests.Load())
				}

				if tc.want == "" {
					recovered.Store(true)
					fresh, err := read()
					if err != nil {
						t.Fatal(err)
					}
					assertGuestIPObservation(t, fresh, "guest_ip_available", "10.0.0.8")
					if agentCalls.Load() != wantAgentCalls+1 || configCalls.Load() != 3 || requests.Load() != 10+wantAgentCalls {
						t.Fatalf("Recovery added unexpected reads: agent=%d config=%d total=%d", agentCalls.Load(), configCalls.Load(), requests.Load())
					}
					assertGuestIPObservation(t, resource, tc.code, "")
				}
			})
		}
	}
}

func assertGuestIPObservation(t *testing.T, resource *protocol.Resource, code, ip string) {
	t.Helper()
	if resource == nil {
		t.Fatal("VM observation unavailable")
	}
	observation, ok := resource.Status["ipObservation"].(map[string]any)
	state := "unavailable"
	if ip != "" {
		state = "available"
	}
	message, hasMessage := observation["message"].(string)
	if !ok || len(observation) != 3 || observation["state"] != state || observation["code"] != code || !hasMessage || message == "" {
		t.Fatalf("unexpected guest address observation: %v", observation)
	}
	got, present := resource.Status["ip"]
	if (ip == "" && present) || (ip != "" && got != ip) {
		t.Fatalf("guest address=%v present=%v, want %q", got, present, ip)
	}
	for _, key := range []string{"ready", "checks"} {
		if _, present := resource.Status[key]; present {
			t.Fatalf("address evidence unexpectedly published %s", key)
		}
	}
	public, err := json.Marshal(resource.Status)
	if err != nil {
		t.Fatal(err)
	}
	if ip == "" && strings.Contains(string(public), "10.0.0.8") {
		t.Fatalf("unavailable status contains a candidate address: %s", public)
	}
	for _, forbidden := range []string{"POISON", "https://", "token=", "aa:bb:cc:dd:ee:ff", "AA:BB:CC:DD:EE:FF", "10.0.0.9", "172.17.0.1"} {
		if strings.Contains(string(public), forbidden) {
			t.Fatalf("public status contains provider details: %s", public)
		}
	}
}
