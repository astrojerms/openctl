package proxmox

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

type sshHostActionFixture struct {
	initialUUID, afterUUID                string
	initialAddress, afterAddress          string
	status                                string
	stopped, unknownMAC                   bool
	replaceDuringConfig                   bool
	cancel                                context.CancelFunc
	submissions, polls, networks, configs *atomic.Int32
}

func sshActionPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	key, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public())
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func sshActionServer(t *testing.T, fixture *sshHostActionFixture) *httptest.Server {
	t.Helper()
	fixture.submissions = new(atomic.Int32)
	fixture.polls = new(atomic.Int32)
	fixture.networks = new(atomic.Int32)
	fixture.configs = new(atomic.Int32)
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		if r.Method != http.MethodGet && (r.Method != http.MethodPost || path != "/api2/json/nodes/pve1/qemu/101/agent/exec") {
			t.Errorf("host-key action attempted mutation: %s %s", r.Method, path)
			http.Error(w, "POISON", http.StatusForbidden)
			return
		}
		switch path {
		case "/api2/json/nodes/pve1/qemu":
			state := "running"
			if fixture.stopped {
				state = "stopped"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"vmid": 101, "name": "existing", "status": state}}})
		case "/api2/json/nodes/pve1/qemu/101/config":
			count := fixture.configs.Add(1)
			uuid := testVMUUID
			if fixture.initialUUID != "" {
				uuid = fixture.initialUUID
			}
			if fixture.submissions.Load() > 0 && fixture.afterUUID != "" {
				uuid = fixture.afterUUID
			}
			if fixture.replaceDuringConfig && count >= 2 {
				uuid = "1e0da4e0-01ef-441e-a64a-75db8614a535"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"name": "existing", "smbios1": "uuid=" + uuid, "net0": "virtio=AA:BB:CC:DD:EE:FF,bridge=vmbr0"}})
		case "/api2/json/nodes/pve1/qemu/101/agent/network-get-interfaces":
			fixture.networks.Add(1)
			addresses := fixture.initialAddress
			if addresses == "" {
				addresses = "10.0.0.8"
			}
			if fixture.submissions.Load() > 0 && fixture.afterAddress != "" {
				addresses = fixture.afterAddress
			}
			ips := []map[string]any{}
			if addresses != "none" {
				for address := range strings.SplitSeq(addresses, ",") {
					ips = append(ips, map[string]any{"ip-address-type": "ipv4", "ip-address": address})
				}
			}
			mac := "aa:bb:cc:dd:ee:ff"
			if fixture.unknownMAC {
				mac = "02:42:ac:11:00:01"
			}
			// A bridge address precedes the real NIC and must never become trust.
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"result": []map[string]any{
				{"name": "docker0", "hardware-address": "02:42:ac:11:00:02", "ip-addresses": []map[string]any{{"ip-address-type": "ipv4", "ip-address": "172.17.0.1"}}},
				{"name": "ens18", "hardware-address": mac, "ip-addresses": ips},
			}}})
		case "/api2/json/nodes/pve1/qemu/101/agent/exec":
			fixture.submissions.Add(1)
			_ = r.ParseForm()
			if !slices.Equal(r.PostForm["command"], []string{"/bin/cat", "/etc/ssh/ssh_host_ed25519_key.pub"}) || len(r.PostForm) != 1 {
				t.Errorf("unexpected guest execution: %v", r.PostForm)
			}
			w.Write([]byte(`{"data":{"pid":42}}`))
		case "/api2/json/nodes/pve1/qemu/101/agent/exec-status":
			fixture.polls.Add(1)
			if fixture.cancel != nil {
				fixture.cancel()
				w.Write([]byte(`{"data":{"exited":0}}`))
				return
			}
			if fixture.status != "" {
				w.Write([]byte(fixture.status))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"exited": 1, "exitcode": 0, "out-truncated": 0, "out-data": string(ssh.MarshalAuthorizedKey(sshActionPublicKey(t)))}})
		default:
			t.Errorf("unexpected host-key action request: %s %s", r.Method, path)
			http.NotFound(w, r)
		}
	}))
}

func TestSSHHostKeysActionDownloadsExactVerifiedKnownHosts(t *testing.T) {
	fixture := &sshHostActionFixture{}
	server := sshActionServer(t, fixture)
	defer server.Close()
	provider := boundSafetyProvider(t, server)
	result, err := provider.DoAction(context.Background(), kindVM, "existing", "ssh-host-keys")
	if err != nil {
		t.Fatal(err)
	}
	want := "10.0.0.8 " + string(ssh.MarshalAuthorizedKey(sshActionPublicKey(t)))
	if result == nil || result.DownloadFilename != "existing.known_hosts" || result.DownloadContent != want || result.URL != "" || result.Message != "" {
		t.Fatalf("unexpected download: %#v; want content %q", result, want)
	}
	if fixture.submissions.Load() != 1 || fixture.polls.Load() != 1 || fixture.networks.Load() != 2 {
		t.Fatalf("missing fresh evidence or resubmission: exec=%d polls=%d addresses=%d", fixture.submissions.Load(), fixture.polls.Load(), fixture.networks.Load())
	}
	if !slices.Contains(provider.Actions(kindVM), "ssh-host-keys") {
		t.Fatal("VM action is not discoverable")
	}
	var described bool
	for _, spec := range provider.ActionSpecs(kindVM) {
		if spec.Name == "ssh-host-keys" {
			described = true
			if spec.Label != "Download verified SSH host keys" || !strings.Contains(spec.Description, "read-only") || !strings.Contains(spec.Description, "TOFU") || !strings.Contains(spec.Description, "automatic trust installation") {
				t.Fatalf("missing explicit effect description: %#v", spec)
			}
		}
	}
	if !described || len(provider.Actions(kindNode)) != 0 || len(provider.ActionSpecs(kindNode)) != 0 {
		t.Fatal("incorrect kind-specific action descriptions")
	}
	if result, err := provider.DoAction(context.Background(), kindNode, "pve1", "ssh-host-keys"); err == nil || result != nil {
		t.Fatal("node accepted VM host-key action")
	}
}

func TestSSHHostKeysActionRefusesUnverifiedTrust(t *testing.T) {
	for _, tc := range []struct {
		name            string
		fixture         sshHostActionFixture
		wantSubmissions int32
	}{
		{name: "immutable replacement", fixture: sshHostActionFixture{initialUUID: "1e0da4e0-01ef-441e-a64a-75db8614a535"}},
		{name: "replacement during config read", fixture: sshHostActionFixture{replaceDuringConfig: true}},
		{name: "stopped VM", fixture: sshHostActionFixture{stopped: true}},
		{name: "unknown address", fixture: sshHostActionFixture{initialAddress: "none"}},
		{name: "ambiguous address", fixture: sshHostActionFixture{initialAddress: "10.0.0.8,10.0.0.9"}},
		{name: "uncorrelated interface", fixture: sshHostActionFixture{unknownMAC: true}},
		{name: "replacement after execution", fixture: sshHostActionFixture{afterUUID: "1e0da4e0-01ef-441e-a64a-75db8614a535"}, wantSubmissions: 1},
		{name: "address changed after execution", fixture: sshHostActionFixture{afterAddress: "10.0.0.9"}, wantSubmissions: 1},
		{name: "address lost after execution", fixture: sshHostActionFixture{afterAddress: "none"}, wantSubmissions: 1},
		{name: "address ambiguous after execution", fixture: sshHostActionFixture{afterAddress: "10.0.0.8,10.0.0.9"}, wantSubmissions: 1},
		{name: "unknown execution status", fixture: sshHostActionFixture{status: `{"data":{"exited":1,"unrecognized":"POISON"}}`}, wantSubmissions: 1},
		{name: "truncated execution", fixture: sshHostActionFixture{status: `{"data":{"exited":1,"exitcode":0,"out-truncated":1,"out-data":"POISON"}}`}, wantSubmissions: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := sshActionServer(t, &tc.fixture)
			defer server.Close()
			provider := boundSafetyProvider(t, server)
			result, err := provider.DoAction(context.Background(), kindVM, "existing", "ssh-host-keys")
			if result != nil || err == nil || strings.Contains(err.Error(), "POISON") {
				t.Fatalf("unverified trust emitted or unsafe error: result=%#v err=%v", result, err)
			}
			if tc.fixture.submissions.Load() != tc.wantSubmissions || tc.fixture.polls.Load() > 1 {
				t.Fatalf("unexpected execution count: exec=%d polls=%d", tc.fixture.submissions.Load(), tc.fixture.polls.Load())
			}
		})
	}
}

func TestSSHHostKeysActionRequiresRecordedIdentityEvenForLegacyProvider(t *testing.T) {
	for _, emptyStore := range []bool{false, true} {
		fixture := &sshHostActionFixture{}
		server := sshActionServer(t, fixture)
		provider := mustNewProvider(t, &Config{Endpoint: server.URL, CAFile: fixtureCA(t, server)})
		if emptyStore {
			provider.SetIdentityStore(memoryIdentities{})
		}
		result, err := provider.DoAction(context.Background(), kindVM, "existing", "ssh-host-keys")
		server.Close()
		if result != nil || err == nil || !strings.Contains(err.Error(), "recorded management identity") || fixture.configs.Load() != 0 || fixture.submissions.Load() != 0 {
			t.Fatalf("name-only lookup allowed: result=%#v err=%v", result, err)
		}
	}
}

func TestSSHHostKeysActionCancellationDoesNotResubmit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fixture := &sshHostActionFixture{cancel: cancel}
	server := sshActionServer(t, fixture)
	defer server.Close()
	provider := boundSafetyProvider(t, server)
	result, err := provider.DoAction(ctx, kindVM, "existing", "ssh-host-keys")
	if result != nil || !errors.Is(err, context.Canceled) || fixture.submissions.Load() != 1 || fixture.polls.Load() != 1 {
		t.Fatalf("cancelled action emitted trust or retried: result=%#v err=%v exec=%d polls=%d", result, err, fixture.submissions.Load(), fixture.polls.Load())
	}
}

func TestSSHHostKeysActionRefusesInsecureProviderTLS(t *testing.T) {
	fixture := &sshHostActionFixture{}
	server := sshActionServer(t, fixture)
	defer server.Close()
	provider := mustNewMultiProvider(t, map[string]*Config{"site": {Endpoint: server.URL, InsecureSkipVerify: true}}, "site")
	provider.SetIdentityStore(memoryIdentities{"existing": {Context: "site", Endpoint: server.URL, Node: "pve1", VMID: 101, Identity: "proxmox:pve1:101:" + testVMUUID}})
	result, err := provider.DoAction(context.Background(), kindVM, "existing", "ssh-host-keys")
	if result != nil || err == nil || !strings.Contains(err.Error(), "certificate-verified") || fixture.submissions.Load() != 0 {
		t.Fatalf("insecure provider emitted trust: result=%#v err=%v", result, err)
	}
}
