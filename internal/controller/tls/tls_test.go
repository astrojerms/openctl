package tls

import (
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"testing"
)

func TestEnsureMaterialGeneratesAllThreeFiles(t *testing.T) {
	dir := t.TempDir()
	mat, err := EnsureMaterial(dir, "test-host", []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("EnsureMaterial: %v", err)
	}
	for _, p := range []string{mat.CACertPath, mat.ServerCertPath, mat.ServerKeyPath} {
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty", p)
		}
	}
}

func TestEnsureMaterialIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	mat1, err := EnsureMaterial(dir, "h", []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	caBytes1, _ := os.ReadFile(mat1.CACertPath)
	srvBytes1, _ := os.ReadFile(mat1.ServerCertPath)

	mat2, err := EnsureMaterial(dir, "h", []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	caBytes2, _ := os.ReadFile(mat2.CACertPath)
	srvBytes2, _ := os.ReadFile(mat2.ServerCertPath)

	if string(caBytes1) != string(caBytes2) {
		t.Error("CA cert changed between calls")
	}
	if string(srvBytes1) != string(srvBytes2) {
		t.Error("Server cert changed between calls")
	}
}

func TestServerCertChainsToCAWithExpectedSANs(t *testing.T) {
	dir := t.TempDir()
	mat, err := EnsureMaterial(dir, "test-host", []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")})
	if err != nil {
		t.Fatalf("EnsureMaterial: %v", err)
	}

	caPEM, _ := os.ReadFile(mat.CACertPath)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("AppendCertsFromPEM CA")
	}

	srvPEM, _ := os.ReadFile(mat.ServerCertPath)
	block, _ := pem.Decode(srvPEM)
	if block == nil {
		t.Fatal("decode server cert PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse server cert: %v", err)
	}

	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSName:   "test-host",
	}); err != nil {
		t.Errorf("verify against test-host: %v", err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSName:   "localhost",
	}); err != nil {
		t.Errorf("verify against localhost: %v", err)
	}
	// IP SAN check — Verify can also handle this via the IPAddresses list.
	hasIP := false
	want := net.ParseIP("127.0.0.1")
	for _, ip := range cert.IPAddresses {
		if ip.Equal(want) {
			hasIP = true
		}
	}
	if !hasIP {
		t.Errorf("server cert IP SANs missing 127.0.0.1: %v", cert.IPAddresses)
	}
}
