package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestServerEndToEndMTLS spins up the agent with real mTLS certs and verifies
// a client with the proper cert can hit /v1/info, while a client without can't.
func TestServerEndToEndMTLS(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := mintCA(t)
	serverCertPEM, serverKeyPEM := mintLeaf(t, caCert, caKey, "localhost", []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	clientCertPEM, clientKeyPEM := mintLeaf(t, caCert, caKey, "test-controller", nil, nil)

	caPath := writeFile(t, dir, "ca.pem", encodePEM("CERTIFICATE", caCert.Raw))
	serverCertPath := writeFile(t, dir, "server.pem", serverCertPEM)
	serverKeyPath := writeFile(t, dir, "server.key", serverKeyPEM)

	// Pick a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	s, err := New(Options{
		Listen:   addr,
		CertFile: serverCertPath,
		KeyFile:  serverKeyPath,
		CAFile:   caPath,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- s.ListenAndServe() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})

	waitForPort(t, addr, 2*time.Second)

	caPool := x509.NewCertPool()
	caPool.AddCert(caCert)

	t.Run("authorized client succeeds", func(t *testing.T) {
		clientPair, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
		if err != nil {
			t.Fatalf("client keypair: %v", err)
		}
		client := &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:      caPool,
				Certificates: []tls.Certificate{clientPair},
				MinVersion:   tls.VersionTLS12,
			},
		}}
		resp, err := client.Get("https://" + addr + "/v1/info")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		var info Info
		if err := json.Unmarshal(body, &info); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if info.AgentVersion != Version {
			t.Errorf("AgentVersion = %q, want %q", info.AgentVersion, Version)
		}
	})

	t.Run("client without cert is rejected", func(t *testing.T) {
		client := &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    caPool,
				MinVersion: tls.VersionTLS12,
			},
		}}
		resp, err := client.Get("https://" + addr + "/v1/info")
		if err == nil {
			_ = resp.Body.Close()
			t.Fatal("expected mTLS handshake to fail without client cert")
		}
	})
}

func mintCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca key: %v", err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("ca cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse ca: %v", err)
	}
	return cert, key
}

func mintLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, cn string, dnsNames []string, ips []net.IP) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("leaf key: %v", err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("leaf cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return encodePEM("CERTIFICATE", der), encodePEM("EC PRIVATE KEY", keyDER)
}

func encodePEM(kind string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der})
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func waitForPort(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", addr)
}
