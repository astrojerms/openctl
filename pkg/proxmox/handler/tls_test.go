package handler

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
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

func mustNewHandler(t *testing.T, cfg *protocol.ProviderConfig) *Handler {
	t.Helper()
	h, err := New(cfg)
	if err != nil {
		t.Fatalf("construct handler: %v", err)
	}
	return h
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	badCA := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(badCA, []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		cfg  *protocol.ProviderConfig
	}{
		{name: "nil"},
		{name: "empty endpoint", cfg: &protocol.ProviderConfig{}},
		{name: "plaintext", cfg: &protocol.ProviderConfig{Endpoint: "http://127.0.0.1:8006"}},
		{name: "plaintext insecure", cfg: &protocol.ProviderConfig{Endpoint: "http://127.0.0.1:8006", InsecureSkipVerify: true}},
		{name: "missing CA", cfg: &protocol.ProviderConfig{Endpoint: "https://127.0.0.1:8006", CAFile: filepath.Join(t.TempDir(), "missing.pem")}},
		{name: "malformed CA", cfg: &protocol.ProviderConfig{Endpoint: "https://127.0.0.1:8006", CAFile: badCA}},
		{name: "conflicting trust", cfg: &protocol.ProviderConfig{Endpoint: "https://127.0.0.1:8006", CAFile: badCA, InsecureSkipVerify: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := New(tc.cfg)
			if err == nil || h != nil {
				t.Fatalf("invalid configuration returned usable handler: handler=%v err=%v", h, err)
			}
		})
	}
}

func TestHandlerTLSVerificationProtectsCredentials(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "PVEAPIToken=fixture@pam!token=fixture-secret" {
			http.Error(w, "missing API token", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"node":"pve1","status":"online"}]}`))
	}))
	defer server.Close()

	cfg := protocol.ProviderConfig{Endpoint: server.URL, TokenID: "fixture@pam!token", TokenSecret: "fixture-secret"}
	h := mustNewHandler(t, &cfg)
	request := &protocol.Request{Action: protocol.ActionList, ResourceType: "ProxmoxNode"}
	resp, err := h.Handle(context.Background(), request)
	var unknownAuthority x509.UnknownAuthorityError
	if !errors.As(err, &unknownAuthority) || resp != nil || requests.Load() != 0 {
		t.Fatalf("untrusted HTTPS must fail before sending credentials: resp=%+v err=%v requests=%d", resp, err, requests.Load())
	}

	cfg.CAFile = fixtureCA(t, server)
	h = mustNewHandler(t, &cfg)
	resp, err = h.Handle(context.Background(), request)
	if err != nil || resp == nil || resp.Status != protocol.StatusSuccess || len(resp.Resources) != 1 || resp.Resources[0].Metadata.Name != "pve1" {
		t.Fatalf("trusted HTTPS node read: resp=%+v err=%v", resp, err)
	}

	cfg.CAFile = ""
	cfg.InsecureSkipVerify = true
	h = mustNewHandler(t, &cfg)
	resp, err = h.Handle(context.Background(), request)
	if err != nil || resp == nil || resp.Status != protocol.StatusSuccess || len(resp.Resources) != 1 || requests.Load() != 2 {
		t.Fatalf("explicit HTTPS insecure opt-in: resp=%+v err=%v requests=%d", resp, err, requests.Load())
	}
}
