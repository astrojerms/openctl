package proxmox

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func fixtureCA(t *testing.T, server *httptest.Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "proxmox-ca.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustNewProvider(t *testing.T, cfg *Config) *Provider {
	t.Helper()
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("construct provider: %v", err)
	}
	return p
}

func mustNewMultiProvider(t *testing.T, configs map[string]*Config, defaultContext string) *Provider {
	t.Helper()
	p, err := NewMulti(configs, defaultContext)
	if err != nil {
		t.Fatalf("construct multi-context provider: %v", err)
	}
	return p
}

func TestNewRejectsNilConfiguration(t *testing.T) {
	p, err := New(nil)
	if err == nil || p != nil {
		t.Fatalf("nil configuration returned usable provider: provider=%v err=%v", p, err)
	}
}

func TestNewMultiRejectsInvalidSiblingTrust(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	ca := fixtureCA(t, server)
	writeCA := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	emptyCA := writeCA("empty.pem", nil)
	badCA := writeCA("malformed.pem", []byte("not a PEM certificate"))
	noCertCA := writeCA("non-certificate.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not a certificate")}))
	for _, tc := range []struct {
		name string
		cfg  *Config
	}{
		{name: "nil"},
		{name: "empty endpoint", cfg: &Config{}},
		{name: "plaintext", cfg: &Config{Endpoint: "http://127.0.0.1:8006"}},
		{name: "plaintext insecure", cfg: &Config{Endpoint: "http://127.0.0.1:8006", InsecureSkipVerify: true}},
		{name: "missing CA", cfg: &Config{Endpoint: server.URL, CAFile: filepath.Join(t.TempDir(), "missing.pem")}},
		{name: "empty CA", cfg: &Config{Endpoint: server.URL, CAFile: emptyCA}},
		{name: "malformed CA", cfg: &Config{Endpoint: server.URL, CAFile: badCA}},
		{name: "no certificate", cfg: &Config{Endpoint: server.URL, CAFile: noCertCA}},
		{name: "conflicting trust", cfg: &Config{Endpoint: server.URL, CAFile: ca, InsecureSkipVerify: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cfg != nil {
				tc.cfg.TokenID = "private@pam!token"
				tc.cfg.TokenSecret = "never-log-token-secret"
			}
			for _, defaultContext := range []string{"a-valid", "z-broken"} {
				p, err := NewMulti(map[string]*Config{
					"a-valid":  {Endpoint: server.URL, CAFile: ca},
					"z-broken": tc.cfg,
				}, defaultContext)
				if err == nil || p != nil {
					t.Fatalf("invalid sibling returned partial provider: provider=%v err=%v", p, err)
				}
				if !strings.Contains(err.Error(), `context "z-broken"`) || strings.Contains(err.Error(), "never-log-token-secret") || strings.Contains(err.Error(), "private@pam!token") {
					t.Fatalf("expected safe failing-context diagnostic: %v", err)
				}
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("construction must not send requests: got %d", requests.Load())
	}
}

func TestProviderTLSVerificationProtectsDiscovery(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "PVEAPIToken=fixture@pam!token=fixture-secret" {
			http.Error(w, "unexpected request or API token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api2/json/nodes":
			w.Write([]byte(`{"data":[{"node":"pve1"}]}`))
		case "/api2/json/nodes/pve1/qemu":
			w.Write([]byte(`{"data":[{"vmid":101,"name":"existing","status":"stopped"}]}`))
		case "/api2/json/nodes/pve1/qemu/101/config":
			w.Write([]byte(`{"data":{"cores":2,"memory":"4096","smbios1":"uuid=11111111-1111-4111-8111-111111111111"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := &Config{Endpoint: server.URL, TokenID: "fixture@pam!token", TokenSecret: "fixture-secret"}
	p := mustNewMultiProvider(t, map[string]*Config{"site": cfg}, "site")
	items, err := p.Discover(context.Background(), "site")
	var unknownAuthority x509.UnknownAuthorityError
	if !errors.As(err, &unknownAuthority) || items != nil || requests.Load() != 0 {
		t.Fatalf("untrusted discovery must fail before sending credentials: items=%+v err=%v requests=%d", items, err, requests.Load())
	}
	if resource, err := p.Get(context.Background(), kindVM, "existing"); !errors.As(err, &unknownAuthority) || resource != nil || requests.Load() != 0 {
		t.Fatalf("untrusted read must fail before sending credentials: resource=%+v err=%v requests=%d", resource, err, requests.Load())
	}

	cfg.CAFile = fixtureCA(t, server)
	p = mustNewMultiProvider(t, map[string]*Config{"site": cfg}, "site")
	if err := p.TestConnection(context.Background(), "site"); err != nil {
		t.Fatalf("trusted connection: %v", err)
	}
	items, err = p.Discover(context.Background(), "site")
	if err != nil || len(items) != 1 || items[0].Context != "site" || items[0].VMID != 101 || items[0].Resource.Metadata.Name != "existing" {
		t.Fatalf("trusted discovery: items=%+v err=%v", items, err)
	}
	resource, err := p.Get(context.Background(), kindVM, "existing")
	if err != nil || resource == nil || resource.Metadata.Name != "existing" || resource.Spec["context"] != "site" {
		t.Fatalf("trusted read: resource=%+v err=%v", resource, err)
	}
}
