// Package tls handles the controller's self-signed TLS material: a CA + a
// server cert/key pair generated on first start. The CA cert is shared with
// clients via openctl config so they can verify the server.
package tls

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
	"time"
)

const (
	caValidity   = 10 * 365 * 24 * time.Hour
	leafValidity = 5 * 365 * 24 * time.Hour
)

// Material is the set of files used to serve TLS for the controller. The CA
// cert path is intended to be distributed to clients; the server cert + key
// stay server-side.
type Material struct {
	CACertPath     string
	ServerCertPath string
	ServerKeyPath  string
}

// EnsureMaterial generates a fresh CA + server cert pair under dir if any of
// the three files are missing. If all exist, it returns them as-is — making
// this safe to call on every controller start.
func EnsureMaterial(dir, host string, ips []net.IP) (*Material, error) {
	mat := &Material{
		CACertPath:     filepath.Join(dir, "ca.crt"),
		ServerCertPath: filepath.Join(dir, "tls.crt"),
		ServerKeyPath:  filepath.Join(dir, "tls.key"),
	}
	if exists(mat.CACertPath) && exists(mat.ServerCertPath) && exists(mat.ServerKeyPath) {
		return mat, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	caCert, caKey, err := generateCA()
	if err != nil {
		return nil, fmt.Errorf("generate CA: %w", err)
	}
	srvCertPEM, srvKeyPEM, err := generateServerCert(caCert, caKey, host, ips)
	if err != nil {
		return nil, fmt.Errorf("generate server cert: %w", err)
	}
	caPEM := encodePEM("CERTIFICATE", caCert.Raw)

	if err := os.WriteFile(mat.CACertPath, caPEM, 0o644); err != nil { // #nosec G306 -- CA cert is public
		return nil, fmt.Errorf("write ca: %w", err)
	}
	if err := os.WriteFile(mat.ServerCertPath, srvCertPEM, 0o644); err != nil { // #nosec G306 -- cert is public
		return nil, fmt.Errorf("write server cert: %w", err)
	}
	if err := os.WriteFile(mat.ServerKeyPath, srvKeyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write server key: %w", err)
	}
	return mat, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func generateCA() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "openctl-controller CA"},
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

func generateServerCert(ca *x509.Certificate, caKey *ecdsa.PrivateKey, host string, ips []net.IP) ([]byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	dnsNames := []string{"localhost"}
	if host != "" && host != "localhost" {
		dnsNames = append(dnsNames, host)
	}
	tpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "openctl-controller"},
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     time.Now().Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return encodePEM("CERTIFICATE", der), encodePEM("EC PRIVATE KEY", keyDER), nil
}

func encodePEM(kind string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der})
}

func serial() *big.Int {
	maxSerial := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, maxSerial)
	if err != nil {
		return big.NewInt(time.Now().UnixNano())
	}
	return n
}
