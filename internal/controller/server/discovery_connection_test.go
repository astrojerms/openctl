package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/providers"
	pmprovider "github.com/openctl/openctl/internal/controller/providers/proxmox"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	pmclient "github.com/openctl/openctl/pkg/proxmox/client"
)

func TestConnectionFailureCategoriesNeverExposeUpstreamData(t *testing.T) {
	const secret = "PRIVATE-UPSTREAM-DATA"
	cert := &x509.Certificate{Subject: pkix.Name{CommonName: secret}, DNSNames: []string{secret}}
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"untrusted", x509.UnknownAuthorityError{Cert: cert}, "tls_untrusted"},
		{"hostname", x509.HostnameError{Certificate: cert, Host: secret}, "tls_hostname_mismatch"},
		{"certificate", x509.CertificateInvalidError{Cert: cert, Reason: x509.Expired, Detail: secret}, "tls_certificate_invalid"},
		{"verification", &tls.CertificateVerificationError{UnverifiedCertificates: []*x509.Certificate{cert}, Err: errors.New(secret)}, "tls_handshake_failed"},
		{"tls protocol", tls.RecordHeaderError{Msg: secret}, "tls_handshake_failed"},
		{"authentication", &pmclient.APIError{StatusCode: 401, Body: secret}, "unauthorized"},
		{"permissions", &pmclient.APIError{StatusCode: 403, Body: secret}, "permission_denied"},
		{"route", &pmclient.APIError{StatusCode: 404, Body: secret}, "endpoint_not_found"},
		{"redirect", &pmclient.APIError{StatusCode: 302, Body: secret}, "redirect_rejected"},
		{"rate limit", &pmclient.APIError{StatusCode: 429, Body: secret}, "rate_limited"},
		{"provider down", &pmclient.APIError{StatusCode: 503, Body: secret}, "provider_unavailable"},
		{"other status", &pmclient.APIError{StatusCode: 418, Body: secret}, "connection_failed"},
		{"dns", &net.DNSError{Err: secret, Name: secret, IsNotFound: true}, "dns_failed"},
		{"dns timeout", &net.DNSError{Err: secret, Name: secret, IsTimeout: true}, "timeout"},
		{"deadline", context.DeadlineExceeded, "timeout"},
		{"canceled", context.Canceled, "canceled"},
		{"unreachable", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, "endpoint_unreachable"},
		{"unknown", errors.New(secret), "connection_failed"},
		// Do not classify by error text: a provider can return arbitrary text.
		{"spoofed status", errors.New("401 x509: certificate signed by unknown authority " + secret), "connection_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &discoveryProvider{connectionErr: fmt.Errorf("upstream %w", &url.Error{Op: "Get", URL: "https://user:" + secret + "@private.invalid/?token=" + secret, Err: tc.err})}
			reg := providers.NewRegistry()
			reg.Register(p)
			h := newResourceHandler(reg, nil, nil, nil)
			out, err := h.TestDiscoveryConnection(ctxWithRole(auth.RoleViewer), &apiv1.TestDiscoveryConnectionRequest{ApiVersion: "fake.openctl.io/v1", Context: "site"})
			if err != nil || out.GetConnected() || out.GetFailureCode() != tc.code || out.GetMessage() == "" {
				t.Fatalf("incorrect safe connection outcome: %+v (%v)", out, err)
			}
			if strings.Contains(out.GetMessage(), secret) || strings.Contains(out.GetMessage(), "private.invalid") || strings.Contains(out.GetFailureCode(), secret) {
				t.Fatal("upstream data leaked into connection outcome")
			}
		})
	}
}

func TestNativeConnectionFailuresRemainReadOnlyAndRedacted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		trust  bool
		code   string
	}{
		{"untrusted", 200, false, "tls_untrusted"},
		{"authentication", 401, true, "unauthorized"},
		{"permissions", 403, true, "permission_denied"},
		{"missing route", 404, true, "endpoint_not_found"},
		{"rate limit", 429, true, "rate_limited"},
		{"provider down", 503, true, "provider_unavailable"},
		{"redirect", 302, true, "redirect_rejected"},
		{"connected", 200, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests, writes, redirects atomic.Int32
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet {
					writes.Add(1)
				}
				if r.URL.Path == "/redirect-target" {
					redirects.Add(1)
				}
				if r.Header.Get("Authorization") != "PVEAPIToken=fixture-id=PRIVATE-TOKEN" {
					t.Error("native client did not use configured API credentials")
				}
				w.Header().Set("Content-Type", "application/json")
				if tc.status == 302 {
					w.Header().Set("Location", "/redirect-target?token=PRIVATE-BODY")
				}
				w.WriteHeader(tc.status)
				if tc.status == 200 {
					_, _ = w.Write([]byte(`{"data":[]}`))
				} else {
					_, _ = w.Write([]byte(`{"error":"PRIVATE-BODY https://user:PRIVATE-TOKEN@private.invalid/"}`))
				}
			}))
			defer upstream.Close()
			cfg := &pmprovider.Config{Endpoint: upstream.URL, TokenID: "fixture-id", TokenSecret: "PRIVATE-TOKEN"}
			if tc.trust {
				cfg.CAFile = proxmoxFixtureCA(t, upstream)
			}
			p, err := pmprovider.NewMulti(map[string]*pmprovider.Config{"site": cfg}, "site")
			if err != nil {
				t.Fatal(err)
			}
			reg := providers.NewRegistry()
			reg.Register(p)
			h := newResourceHandler(reg, nil, nil, nil)
			out, err := h.TestDiscoveryConnection(ctxWithRole(auth.RoleViewer), &apiv1.TestDiscoveryConnectionRequest{Context: "site"})
			if err != nil || out.GetFailureCode() != tc.code || out.GetConnected() != (tc.code == "") {
				t.Fatalf("native connection outcome=%+v (%v)", out, err)
			}
			for _, secret := range []string{"PRIVATE-TOKEN", "PRIVATE-BODY", "private.invalid"} {
				if strings.Contains(out.GetMessage(), secret) || strings.Contains(out.GetFailureCode(), secret) {
					t.Fatal("native provider leaked upstream data")
				}
			}
			if writes.Load() != 0 || redirects.Load() != 0 {
				t.Fatal("connection testing mutated the provider or followed a redirect")
			}
			if !tc.trust && requests.Load() != 0 {
				t.Fatal("untrusted TLS received authenticated HTTP requests")
			}
			if tc.trust && requests.Load() == 0 {
				t.Fatal("trusted connection did not exercise the native provider")
			}
		})
	}
}
