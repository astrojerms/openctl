package proxmox

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
	pmhandler "github.com/openctl/openctl/pkg/proxmox/handler"
)

func TestProviderGuestIPMatchesConfiguredNIC(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent string
		want  string
	}{
		{"Docker bridge first", `{"data":{"result":[{"name":"docker0","hardware-address":"02:42:ac:11:00:01","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"172.17.0.1"}]},{"name":"ens18","hardware-address":"aa:bb:cc:dd:ee:ff","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"10.0.0.8"}]}]}}`, "10.0.0.8"},
		{"ambiguous guest address", `{"data":{"result":[{"name":"ens18","hardware-address":"aa:bb:cc:dd:ee:ff","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"10.0.0.8"},{"ip-address-type":"ipv4","ip-address":"10.0.0.9"}]}]}}`, ""},
		{"missing MAC evidence", `{"data":{"result":[{"name":"ens18","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"10.0.0.8"}]}]}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("unexpected mutation: %s %s", r.Method, r.URL.Path)
					http.Error(w, "read only", http.StatusMethodNotAllowed)
					return
				}
				switch r.URL.Path {
				case "/api2/json/nodes":
					_, _ = io.WriteString(w, `{"data":[{"node":"pve1"}]}`)
				case "/api2/json/nodes/pve1/qemu":
					_, _ = io.WriteString(w, `{"data":[{"vmid":101,"name":"gpu","status":"running"}]}`)
				case "/api2/json/nodes/pve1/qemu/101/config":
					_, _ = io.WriteString(w, `{"data":{"net0":"virtio=AA:BB:CC:DD:EE:FF,bridge=vmbr0","memory":4096}}`)
				case "/api2/json/nodes/pve1/qemu/101/agent/network-get-interfaces":
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
			for name, read := range map[string]func() (*protocol.Resource, error){
				"native": func() (*protocol.Resource, error) {
					return provider.Get(context.Background(), kindVM, "gpu")
				},
				"legacy": func() (*protocol.Resource, error) {
					response, err := handler.Handle(context.Background(), &protocol.Request{Action: protocol.ActionGet, ResourceType: kindVM, ResourceName: "gpu"})
					if err != nil {
						return nil, err
					}
					return response.Resource, nil
				},
			} {
				t.Run(name, func(t *testing.T) {
					resource, err := read()
					if err != nil {
						t.Fatal(err)
					}
					if resource == nil {
						t.Fatal("VM observation unavailable")
					}
					got, present := resource.Status["ip"]
					if tc.want == "" {
						if present {
							t.Fatalf("unverifiable guest address must be absent, got %v", got)
						}
					} else if got != tc.want {
						t.Fatalf("guest SSH/reference address = %v, want configured NIC address %s", got, tc.want)
					}
				})
			}
		})
	}
}
