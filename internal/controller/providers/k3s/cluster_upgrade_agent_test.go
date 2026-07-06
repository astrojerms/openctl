package k3s

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openctl/openctl/pkg/k3s/agent/certs"
)

// mtlsAgentServer stands up an httptest server presenting node "cp0"'s server
// cert and requiring a client cert verified against the bundle CA — the same
// mTLS the real agent enforces — with scripted /v1/upgrade/k3s and /v1/info
// handlers. It lets us exercise agentNodeUpgrader end to end without the real
// agent (whose upgrade handler runs an actual k3s download).
func mtlsAgentServer(t *testing.T, bundle *certs.Bundle, node, infoVersion string, upgradeStatus int) (endpoint string, upgradeCalls *atomic.Int32) {
	t.Helper()
	sc := bundle.ServerCerts[node]
	serverCert, err := tls.X509KeyPair(sc.CertPEM, sc.KeyPEM)
	if err != nil {
		t.Fatalf("server keypair: %v", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(bundle.CACertPEM) {
		t.Fatal("append CA")
	}

	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/upgrade/k3s", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(upgradeStatus)
	})
	mux.HandleFunc("/v1/info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"k3sVersion":"` + infoVersion + `"}`))
	})

	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "https://"), &calls
}

func testBundle(t *testing.T) *certs.Bundle {
	t.Helper()
	// Server cert must be valid for 127.0.0.1 (httptest listens there) so the
	// agent client's TLS verification passes.
	bundle, err := certs.GenerateBundle("upgrade-test", []certs.NodeIdentity{{Name: "cp0", IP: "127.0.0.1"}})
	if err != nil {
		t.Fatalf("certs: %v", err)
	}
	return bundle
}

func newTestUpgrader(bundle *certs.Bundle, endpoints map[string]string) *agentNodeUpgrader {
	u := newAgentNodeUpgrader(agentCertBundle{
		CACertPEM:     bundle.CACertPEM,
		ClientCertPEM: bundle.ClientCertPEM,
		ClientKeyPEM:  bundle.ClientKeyPEM,
	}, endpoints)
	u.healthPoll = 5 * time.Millisecond // keep the test fast
	u.healthTimeout = 2 * time.Second
	return u
}

// Upgrade posts to the agent and Health reads back the running version over
// real mTLS.
func TestAgentNodeUpgrader_UpgradeThenHealth(t *testing.T) {
	bundle := testBundle(t)
	// The real agent returns 204 No Content on a successful upgrade.
	ep, calls := mtlsAgentServer(t, bundle, "cp0", "v1.30.5+k3s1", http.StatusNoContent)
	u := newTestUpgrader(bundle, map[string]string{"cp0": ep})

	node := upgradeNode{Name: "cp0", Role: roleServer}
	if err := u.Upgrade(context.Background(), node, "v1.30.5+k3s1"); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("agent upgrade endpoint hit %d times, want 1", calls.Load())
	}
	got, err := u.Health(context.Background(), node)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if got != "v1.30.5+k3s1" {
		t.Errorf("Health version = %q, want v1.30.5+k3s1", got)
	}
}

// A node with no known endpoint fails clearly rather than panicking.
func TestAgentNodeUpgrader_UnknownEndpoint(t *testing.T) {
	bundle := testBundle(t)
	u := newTestUpgrader(bundle, map[string]string{}) // no endpoints
	if err := u.Upgrade(context.Background(), upgradeNode{Name: "ghost"}, "v2"); err == nil {
		t.Fatal("expected an error for a node with no known agent endpoint")
	}
}

// A failing upgrade (agent returns non-2xx) surfaces as an error.
func TestAgentNodeUpgrader_UpgradeError(t *testing.T) {
	bundle := testBundle(t)
	ep, _ := mtlsAgentServer(t, bundle, "cp0", "v1", http.StatusInternalServerError)
	u := newTestUpgrader(bundle, map[string]string{"cp0": ep})
	if err := u.Upgrade(context.Background(), upgradeNode{Name: "cp0"}, "v2"); err == nil {
		t.Fatal("expected an error when the agent upgrade returns 500")
	}
}

// The whole rolling-upgrade core drives the real agent upgrader: cp0 upgrades
// and comes back at the target, integrating #87's rollingUpgrade with the
// production nodeUpgrader.
func TestAgentNodeUpgrader_WithRollingUpgrade(t *testing.T) {
	bundle := testBundle(t)
	ep, calls := mtlsAgentServer(t, bundle, "cp0", "v2", http.StatusNoContent)
	u := newTestUpgrader(bundle, map[string]string{"cp0": ep})

	res, err := rollingUpgrade(context.Background(),
		[]upgradeNode{{Name: "cp0", Role: roleServer, Version: "v1"}}, "v2", u)
	if err != nil {
		t.Fatalf("rollingUpgrade: %v", err)
	}
	if len(res.Upgraded) != 1 || res.Upgraded[0] != "cp0" {
		t.Errorf("Upgraded = %v, want [cp0]", res.Upgraded)
	}
	if calls.Load() != 1 {
		t.Errorf("upgrade calls = %d, want 1", calls.Load())
	}
}
