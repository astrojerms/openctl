package certs

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"testing"
)

func TestGenerateBundleAndLoadRoundTrip(t *testing.T) {
	nodes := []NodeIdentity{
		{Name: "dev-cp-0", IP: "192.168.1.50"},
		{Name: "dev-worker-0", IP: "192.168.1.51"},
	}
	b, err := GenerateBundle("dev", nodes)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(b.ServerCerts) != 2 {
		t.Fatalf("want 2 server certs, got %d", len(b.ServerCerts))
	}

	dir := t.TempDir()
	if err := b.WriteTo(dir); err != nil {
		t.Fatalf("write: %v", err)
	}
	loaded, err := LoadBundle(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.ServerCerts) != 2 {
		t.Fatalf("loaded: want 2 server certs, got %d", len(loaded.ServerCerts))
	}
	for _, n := range nodes {
		if _, ok := loaded.ServerCerts[n.Name]; !ok {
			t.Errorf("missing server cert for %s after reload", n.Name)
		}
	}
}

func TestServerCertHasNodeIPAsSAN(t *testing.T) {
	b, err := GenerateBundle("dev", []NodeIdentity{{Name: "n1", IP: "10.0.0.5"}})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	sc := b.ServerCerts["n1"]

	keypair, err := tls.X509KeyPair(sc.CertPEM, sc.KeyPEM)
	if err != nil {
		t.Fatalf("parse keypair: %v", err)
	}
	leaf, err := x509.ParseCertificate(keypair.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}

	want := net.ParseIP("10.0.0.5")
	found := false
	for _, ip := range leaf.IPAddresses {
		if ip.Equal(want) {
			found = true
		}
	}
	if !found {
		t.Errorf("server cert SAN missing IP %s; got %v", want, leaf.IPAddresses)
	}
}

func TestClientAndServerCertsChainToCA(t *testing.T) {
	b, err := GenerateBundle("dev", []NodeIdentity{{Name: "n1", IP: "10.0.0.5"}})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b.CACertPEM) {
		t.Fatal("append CA")
	}

	verify := func(name string, certPEM []byte, usage x509.ExtKeyUsage) {
		t.Helper()
		block, _ := pem.Decode(certPEM)
		if block == nil {
			t.Fatalf("%s: no PEM block", name)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("%s parse: %v", name, err)
		}
		_, err = cert.Verify(x509.VerifyOptions{
			Roots:     pool,
			KeyUsages: []x509.ExtKeyUsage{usage},
		})
		if err != nil {
			t.Errorf("%s verify: %v", name, err)
		}
	}

	verify("client", b.ClientCertPEM, x509.ExtKeyUsageClientAuth)
	verify("server", b.ServerCerts["n1"].CertPEM, x509.ExtKeyUsageServerAuth)
}
