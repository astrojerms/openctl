package client

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func writeTestCA(t *testing.T, bundle []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, bundle, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func serverTestCA(t *testing.T, server *httptest.Server) string {
	t.Helper()
	return writeTestCA(t, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
}

func trustedTestClient(t *testing.T, server *httptest.Server, tokenID, tokenSecret string) *Client {
	t.Helper()
	c, err := New(server.URL, tokenID, tokenSecret, TLSOptions{CAFile: serverTestCA(t, server)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.httpClient.CloseIdleConnections)
	return c
}

func independentTestCertificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "wrong.invalid"},
		DNSNames:              []string{"wrong.invalid"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestTLSCertificateTrustBeforeAuthentication(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.Header.Get("Authorization"); got != "PVEAPIToken=root@pam!tls=private-secret" {
			t.Errorf("trusted request had incorrect authorization")
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	_, unrelatedCA := independentTestCertificate(t)
	for _, tc := range []struct {
		name    string
		options TLSOptions
		trusted bool
	}{
		{"default rejects untrusted certificate", TLSOptions{}, false},
		{"configured CA authenticates", TLSOptions{CAFile: serverTestCA(t, server)}, true},
		{"wrong CA rejects certificate", TLSOptions{CAFile: writeTestCA(t, unrelatedCA)}, false},
		{"explicit insecure HTTPS authenticates", TLSOptions{InsecureSkipVerify: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := requests.Load()
			c, err := New(server.URL, "root@pam!tls", "private-secret", tc.options)
			if err != nil {
				t.Fatal(err)
			}
			defer c.httpClient.CloseIdleConnections()
			_, err = c.ListNodes(context.Background())
			if tc.trusted {
				if err != nil || requests.Load() != before+1 {
					t.Fatalf("trusted API request failed: requests=%d err=%v", requests.Load()-before, err)
				}
			} else {
				var unknown x509.UnknownAuthorityError
				if !errors.As(err, &unknown) || requests.Load() != before {
					t.Fatalf("untrusted certificate reached HTTP handler: requests=%d err=%v", requests.Load()-before, err)
				}
			}
			if err != nil && strings.Contains(err.Error(), "private-secret") {
				t.Fatal("API token appeared in transport error")
			}
		})
	}
}

func TestTLSRejectsWrongHostnameBeforeAuthentication(t *testing.T) {
	certificate, bundle := independentTestCertificate(t)
	var requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}}
	server.StartTLS()
	defer server.Close()
	c, err := New(server.URL, "token", "secret", TLSOptions{CAFile: writeTestCA(t, bundle)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.httpClient.CloseIdleConnections()
	_, err = c.ListNodes(context.Background())
	var hostname x509.HostnameError
	if !errors.As(err, &hostname) || requests.Load() != 0 {
		t.Fatalf("hostname mismatch did not fail before authentication: requests=%d err=%v", requests.Load(), err)
	}
}

func TestNewRejectsInvalidConfigurationBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	validCA := serverTestCA(t, server)
	validBundle, err := os.ReadFile(validCA)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, endpoint string
		options        TLSOptions
	}{
		{"plaintext", strings.Replace(server.URL, "https:", "http:", 1), TLSOptions{}},
		{"plaintext insecure", strings.Replace(server.URL, "https:", "http:", 1), TLSOptions{InsecureSkipVerify: true}},
		{"missing scheme", "pve.example.com:8006", TLSOptions{}},
		{"missing host", "https:///api", TLSOptions{}},
		{"malformed URL", "https://[invalid", TLSOptions{}},
		{"invalid port", "https://pve.example.com:65536", TLSOptions{}},
		{"empty port", "https://pve.example.com:", TLSOptions{}},
		{"invalid IP", "https://[invalid]", TLSOptions{}},
		{"URL credentials", strings.Replace(server.URL, "https://", "https://private-secret@", 1), TLSOptions{}},
		{"query", server.URL + "?token=private-secret", TLSOptions{}},
		{"empty query", server.URL + "?", TLSOptions{}},
		{"fragment", server.URL + "#private-secret", TLSOptions{}},
		{"empty fragment", server.URL + "#", TLSOptions{}},
		{"missing CA file", server.URL, TLSOptions{CAFile: filepath.Join(t.TempDir(), "missing.pem")}},
		{"unreadable CA directory", server.URL, TLSOptions{CAFile: t.TempDir()}},
		{"empty CA", server.URL, TLSOptions{CAFile: writeTestCA(t, nil)}},
		{"invalid PEM", server.URL, TLSOptions{CAFile: writeTestCA(t, []byte("not a certificate"))}},
		{"invalid DER", server.URL, TLSOptions{CAFile: writeTestCA(t, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("invalid")}))}},
		{"non-certificate PEM", server.URL, TLSOptions{CAFile: writeTestCA(t, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("invalid")}))}},
		{"partially invalid bundle", server.URL, TLSOptions{CAFile: writeTestCA(t, append(validBundle, []byte("invalid trailing PEM")...))}},
		{"malformed block before valid certificate", server.URL, TLSOptions{CAFile: writeTestCA(t, append([]byte("-----BEGIN CERTIFICATE-----\ninvalid\n-----END CERTIFICATE-----\n"), validBundle...))}},
		{"mutually exclusive trust", server.URL, TLSOptions{CAFile: validCA, InsecureSkipVerify: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := New(tc.endpoint, "token", "private-secret", tc.options)
			if err == nil || c != nil {
				t.Fatal("invalid configuration produced a usable client")
			}
			if strings.Contains(err.Error(), "private-secret") {
				t.Fatal("API token or URL credential appeared in configuration error")
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatal("invalid configuration contacted the API")
	}
}

func TestTLSRefusesRedirectsWithoutDestinationRequests(t *testing.T) {
	var destinationRequests atomic.Int32
	destination := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationRequests.Add(1)
		_, _ = w.Write([]byte(`{"data":[]}`))
	})
	plainDestination := httptest.NewServer(destination)
	defer plainDestination.Close()
	tlsDestination := httptest.NewTLSServer(destination)
	defer tlsDestination.Close()
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		for _, target := range []string{plainDestination.URL, tlsDestination.URL, "/redirected"} {
			t.Run(http.StatusText(status)+"/"+target, func(t *testing.T) {
				var sourceRequests atomic.Int32
				source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/api2/json/nodes" {
						destination.ServeHTTP(w, r)
						return
					}
					sourceRequests.Add(1)
					if r.Header.Get("Authorization") != "PVEAPIToken=token=secret" {
						t.Error("redirect source did not receive expected authorization")
					}
					http.Redirect(w, r, target, status)
				}))
				defer source.Close()
				// Trust both HTTPS servers so a followed redirect cannot be hidden
				// by an unrelated destination trust failure.
				bundle := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: source.Certificate().Raw}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsDestination.Certificate().Raw})...)
				c, err := New(source.URL, "token", "secret", TLSOptions{CAFile: writeTestCA(t, bundle)})
				if err != nil {
					t.Fatal(err)
				}
				defer c.httpClient.CloseIdleConnections()
				if _, err := c.ListNodes(context.Background()); err == nil {
					t.Fatal("redirect was accepted")
				}
				if sourceRequests.Load() != 1 || destinationRequests.Load() != 0 {
					t.Fatalf("redirect reached destination: source=%d destination=%d", sourceRequests.Load(), destinationRequests.Load())
				}
			})
		}
	}
}

func TestTLSPreservesEndpointBasePath(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/proxmox/api2/json/nodes" {
			t.Errorf("endpoint base path was not preserved: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	c, err := New(server.URL+"/proxmox/", "token", "secret", TLSOptions{CAFile: serverTestCA(t, server)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.httpClient.CloseIdleConnections()
	if _, err := c.ListNodes(context.Background()); err != nil || requests.Load() != 1 {
		t.Fatalf("base-path API request failed: requests=%d err=%v", requests.Load(), err)
	}
}

func TestTLSRejectsProtocolsBelowTLS12(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11}
	server.StartTLS()
	defer server.Close()
	c, err := New(server.URL, "token", "secret", TLSOptions{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.httpClient.CloseIdleConnections()
	if _, err := c.ListNodes(context.Background()); err == nil || requests.Load() != 0 {
		t.Fatalf("obsolete TLS authenticated: requests=%d err=%v", requests.Load(), err)
	}
}
