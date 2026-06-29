// Package certs handles per-cluster CA + leaf certificate generation for the
// k3s agent. One CA per cluster, one server cert per node (with the node's IP
// as a SAN), and one client cert for the controller.
package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	caValidity     = 10 * 365 * 24 * time.Hour
	leafValidity   = 5 * 365 * 24 * time.Hour
	caFileName     = "ca.pem"
	caKeyFileName  = "ca.key"
	clientCertName = "client.pem"
	clientKeyName  = "client.key"
)

// NodeIdentity is the input for minting a per-node server certificate.
type NodeIdentity struct {
	Name string // node name (e.g. "dev-cp-0")
	IP   string // node IP, used as a SAN so plugin can dial by IP
}

// ServerCert is the per-node server cert + key, PEM-encoded.
type ServerCert struct {
	CertPEM []byte
	KeyPEM  []byte
}

// Bundle is everything needed to bootstrap mTLS on a cluster: the CA, the
// controller's client cert, and one server cert per node.
type Bundle struct {
	CACertPEM     []byte
	CAKeyPEM      []byte
	ClientCertPEM []byte
	ClientKeyPEM  []byte
	ServerCerts   map[string]ServerCert // keyed by node name
}

// GenerateBundle mints a fresh CA and all leaf certs for the given cluster.
func GenerateBundle(clusterName string, nodes []NodeIdentity) (*Bundle, error) {
	caCert, caKey, err := generateCA(clusterName)
	if err != nil {
		return nil, fmt.Errorf("generate CA: %w", err)
	}

	clientCertPEM, clientKeyPEM, err := generateLeaf(caCert, caKey, leafSpec{
		commonName: "openctl-controller",
		extKeyUsages: []x509.ExtKeyUsage{
			x509.ExtKeyUsageClientAuth,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("generate client cert: %w", err)
	}

	serverCerts := make(map[string]ServerCert, len(nodes))
	for _, n := range nodes {
		ip := net.ParseIP(n.IP)
		if ip == nil {
			return nil, fmt.Errorf("node %q has invalid IP %q", n.Name, n.IP)
		}
		certPEM, keyPEM, err := generateLeaf(caCert, caKey, leafSpec{
			commonName:   n.Name,
			dnsNames:     []string{n.Name},
			ipAddresses:  []net.IP{ip},
			extKeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		})
		if err != nil {
			return nil, fmt.Errorf("generate server cert for %s: %w", n.Name, err)
		}
		serverCerts[n.Name] = ServerCert{CertPEM: certPEM, KeyPEM: keyPEM}
	}

	caCertPEM := encodePEM("CERTIFICATE", caCert.Raw)
	caKeyPEM, err := marshalKeyPEM(caKey)
	if err != nil {
		return nil, fmt.Errorf("marshal CA key: %w", err)
	}

	return &Bundle{
		CACertPEM:     caCertPEM,
		CAKeyPEM:      caKeyPEM,
		ClientCertPEM: clientCertPEM,
		ClientKeyPEM:  clientKeyPEM,
		ServerCerts:   serverCerts,
	}, nil
}

// MintServerCerts extends an existing Bundle with server certs for new
// nodes, signed by the bundle's existing CA. Used by the count-up path
// when adding nodes to a live cluster: the cluster's CA must stay the same
// so existing agents keep trusting it. The returned bundle is the same
// pointer with ServerCerts populated for the new nodes; the caller can
// re-persist it via WriteTo.
//
// Returns an error if the bundle's CA cert or key fails to parse, if any
// node has an invalid IP, or if leaf-cert generation fails. Existing
// ServerCerts entries are left intact.
func (b *Bundle) MintServerCerts(nodes []NodeIdentity) error {
	if len(nodes) == 0 {
		return nil
	}
	caCert, err := parseCertPEM(b.CACertPEM)
	if err != nil {
		return fmt.Errorf("parse CA cert: %w", err)
	}
	caKey, err := parseKeyPEM(b.CAKeyPEM)
	if err != nil {
		return fmt.Errorf("parse CA key: %w", err)
	}
	if b.ServerCerts == nil {
		b.ServerCerts = make(map[string]ServerCert, len(nodes))
	}
	for _, n := range nodes {
		ip := net.ParseIP(n.IP)
		if ip == nil {
			return fmt.Errorf("node %q has invalid IP %q", n.Name, n.IP)
		}
		certPEM, keyPEM, err := generateLeaf(caCert, caKey, leafSpec{
			commonName:   n.Name,
			dnsNames:     []string{n.Name},
			ipAddresses:  []net.IP{ip},
			extKeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		})
		if err != nil {
			return fmt.Errorf("mint server cert for %s: %w", n.Name, err)
		}
		b.ServerCerts[n.Name] = ServerCert{CertPEM: certPEM, KeyPEM: keyPEM}
	}
	return nil
}

func parseCertPEM(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parseKeyPEM(keyPEM []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

// WriteTo persists CA + client cert to dir; per-node server certs to
// dir/<node>-server.{pem,key}. Files are mode 0600.
func (b *Bundle) WriteTo(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	files := map[string][]byte{
		caFileName:     b.CACertPEM,
		caKeyFileName:  b.CAKeyPEM,
		clientCertName: b.ClientCertPEM,
		clientKeyName:  b.ClientKeyPEM,
	}
	for name, server := range b.ServerCerts {
		files[name+"-server.pem"] = server.CertPEM
		files[name+"-server.key"] = server.KeyPEM
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}

// LoadBundle reads back a bundle previously written by WriteTo. Server certs
// are discovered by filename pattern `<node>-server.{pem,key}`.
func LoadBundle(dir string) (*Bundle, error) {
	read := func(name string) ([]byte, error) {
		data, err := os.ReadFile(filepath.Join(dir, name)) // #nosec G304 -- name is from controlled filenames in this package
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		return data, nil
	}

	caCertPEM, err := read(caFileName)
	if err != nil {
		return nil, err
	}
	caKeyPEM, err := read(caKeyFileName)
	if err != nil {
		return nil, err
	}
	clientCertPEM, err := read(clientCertName)
	if err != nil {
		return nil, err
	}
	clientKeyPEM, err := read(clientKeyName)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	serverCerts := make(map[string]ServerCert)
	for _, e := range entries {
		name := e.Name()
		const suffix = "-server.pem"
		if !strings.HasSuffix(name, suffix) {
			continue
		}
		nodeName := name[:len(name)-len(suffix)]
		certPEM, err := read(name)
		if err != nil {
			return nil, err
		}
		keyPEM, err := read(nodeName + "-server.key")
		if err != nil {
			return nil, err
		}
		serverCerts[nodeName] = ServerCert{CertPEM: certPEM, KeyPEM: keyPEM}
	}

	return &Bundle{
		CACertPEM:     caCertPEM,
		CAKeyPEM:      caKeyPEM,
		ClientCertPEM: clientCertPEM,
		ClientKeyPEM:  clientKeyPEM,
		ServerCerts:   serverCerts,
	}, nil
}

func generateCA(clusterName string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "openctl-k3s-agent CA: " + clusterName},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

type leafSpec struct {
	commonName   string
	dnsNames     []string
	ipAddresses  []net.IP
	extKeyUsages []x509.ExtKeyUsage
}

func generateLeaf(ca *x509.Certificate, caKey *ecdsa.PrivateKey, spec leafSpec) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: spec.commonName},
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     time.Now().Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  spec.extKeyUsages,
		DNSNames:     spec.dnsNames,
		IPAddresses:  spec.ipAddresses,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	keyPEMBytes, err := marshalKeyPEM(key)
	if err != nil {
		return nil, nil, err
	}
	return encodePEM("CERTIFICATE", der), keyPEMBytes, nil
}

func marshalKeyPEM(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return encodePEM("EC PRIVATE KEY", der), nil
}

func encodePEM(kind string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der})
}

func serial() *big.Int {
	// 128-bit random serial; collision probability negligible.
	maxSerial := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, maxSerial)
	if err != nil {
		// Fallback to time-based; only hit if crypto/rand is unavailable, which
		// would already have killed the cert generation upstream.
		return big.NewInt(time.Now().UnixNano())
	}
	return n
}
