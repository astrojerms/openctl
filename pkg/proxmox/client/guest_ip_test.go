package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGetVMIPAddress(t *testing.T) {
	const mac = "AA:BB:CC:DD:EE:FF"
	iface := func(hardware string, addresses ...string) AgentNetworkInterface {
		result := AgentNetworkInterface{Name: "ens18", HWAddr: hardware}
		for _, address := range addresses {
			result.IPAddresses = append(result.IPAddresses, AgentIPAddress{Type: "ipv4", Address: address})
		}
		return result
	}
	config := func(networks map[string]any) *VMConfig { return &VMConfig{Raw: networks} }
	primary := config(map[string]any{"net0": "virtio=" + mac + ",bridge=vmbr0"})
	secondary := config(map[string]any{"net2": "e1000=" + mac + ",bridge=vmbr1"})
	docker := iface("02:42:ac:11:00:01", "172.17.0.1")
	guest := iface("aa:bb:cc:dd:ee:ff", "10.0.0.8")
	for _, tc := range []struct {
		name       string
		config     *VMConfig
		interfaces []AgentNetworkInterface
		want       string
		wantCode   string
	}{
		{"docker before guest", primary, []AgentNetworkInterface{docker, guest}, "10.0.0.8", ""},
		{"docker after guest", primary, []AgentNetworkInterface{guest, docker}, "10.0.0.8", ""},
		{"secondary configured NIC", secondary, []AgentNetworkInterface{docker, guest}, "10.0.0.8", ""},
		{"alternate agent MAC spelling", primary, []AgentNetworkInterface{iface("aa-bb-cc-dd-ee-ff", "10.0.0.8")}, "10.0.0.8", ""},
		{"duplicate address observations", primary, []AgentNetworkInterface{guest, guest}, "10.0.0.8", ""},
		{"skip unusable addresses", primary, []AgentNetworkInterface{iface(mac, "invalid", "0.0.0.0", "127.0.0.2", "169.254.1.2", "224.0.0.1", "255.255.255.255", "2001:db8::1", "::ffff:10.0.0.8", "10.0.0.8")}, "10.0.0.8", ""},
		{"no config", nil, []AgentNetworkInterface{guest}, "", "vm_nic_unavailable"},
		{"no configured NIC", config(map[string]any{}), []AgentNetworkInterface{guest}, "", "vm_nic_unavailable"},
		{"non-network config key", config(map[string]any{"network": "virtio=" + mac}), []AgentNetworkInterface{guest}, "", "vm_nic_unavailable"},
		{"malformed configured MAC", config(map[string]any{"net0": "virtio=invalid"}), []AgentNetworkInterface{guest}, "", "vm_nic_unavailable"},
		{"no matching MAC", primary, []AgentNetworkInterface{docker}, "", "guest_ip_unavailable"},
		{"missing agent MAC", primary, []AgentNetworkInterface{iface("", "10.0.0.8")}, "", "guest_ip_unavailable"},
		{"invalid agent MAC", primary, []AgentNetworkInterface{iface("invalid", "10.0.0.8")}, "", "guest_ip_unavailable"},
		{"no usable address", primary, []AgentNetworkInterface{iface(mac, "169.254.1.2", "::1")}, "", "guest_ip_unavailable"},
		{"empty agent interfaces", primary, nil, "", "guest_ip_unavailable"},
		{"wrong address family label", primary, []AgentNetworkInterface{{HWAddr: mac, IPAddresses: []AgentIPAddress{{Type: "ipv6", Address: "10.0.0.8"}}}}, "", "guest_ip_unavailable"},
		{"multiple addresses on one NIC", primary, []AgentNetworkInterface{iface(mac, "10.0.0.8", "10.0.0.9")}, "", "guest_ip_ambiguous"},
		{"multiple configured NIC addresses", config(map[string]any{"net0": "virtio=" + mac, "net1": "virtio=AA:BB:CC:DD:EE:01"}), []AgentNetworkInterface{guest, iface("AA:BB:CC:DD:EE:01", "10.0.1.8")}, "", "guest_ip_ambiguous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/api2/json/nodes/pve1/qemu/101/agent/network-get-interfaces" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"result": tc.interfaces}})
			}))
			defer server.Close()
			client := trustedTestClient(t, server, "t", "s")
			got, err := client.GetVMIPAddress(context.Background(), "pve1", 101, tc.config)
			if got != tc.want || (err == nil) != (tc.want != "") {
				t.Fatalf("address=%q error=%v, want address=%q (error unless unambiguous)", got, err, tc.want)
			}
			if tc.wantCode != "" {
				var failure *GuestIPError
				if !errors.As(err, &failure) || failure.Code() != tc.wantCode {
					t.Fatalf("error=%v, want guest address code %s", err, tc.wantCode)
				}
			}
			wantCalls := int32(1)
			if tc.wantCode == "vm_nic_unavailable" {
				wantCalls = 0
			}
			if calls.Load() != wantCalls {
				t.Fatalf("agent calls=%d, want %d", calls.Load(), wantCalls)
			}
		})
	}
}

func TestGuestIPFailureKeepsPrivateCause(t *testing.T) {
	const poison = "POISON_https://private.example/token=secret"
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		code     string
		canceled bool
	}{
		{"HTTP failure", http.StatusForbidden, poison, "guest_agent_unavailable", false},
		{"JSON failure", http.StatusOK, `{"data":{"result":"` + poison + `"}}`, "guest_agent_invalid_response", false},
		{"JSON syntax failure", http.StatusOK, `{"data":` + poison, "guest_agent_invalid_response", false},
		{"canceled read", http.StatusOK, "", "guest_agent_unavailable", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := trustedTestClient(t, server, "t", "s")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			ip, err := client.GetVMIPAddress(ctx, "pve1", 101, &VMConfig{Raw: map[string]any{"net0": "virtio=AA:BB:CC:DD:EE:FF"}})
			var failure *GuestIPError
			if ip != "" || !errors.As(err, &failure) || failure.Code() != tc.code {
				t.Fatalf("address=%q error=%v, want code %s", ip, err, tc.code)
			}
			if strings.Contains(err.Error(), poison) || strings.Contains(err.Error(), server.URL) {
				t.Fatalf("public error contains provider details: %s", err)
			}
			switch {
			case tc.canceled:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("lost cancellation cause")
				}
			case tc.status >= 400:
				var apiError *APIError
				if !errors.As(err, &apiError) || apiError.Body != poison {
					t.Fatal("lost HTTP cause")
				}
			default:
				var decodeError *json.UnmarshalTypeError
				var syntaxError *json.SyntaxError
				if !errors.As(err, &decodeError) && !errors.As(err, &syntaxError) {
					t.Fatal("lost JSON cause")
				}
			}
		})
	}
}
