package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestVerifiedConnectionRejectsUnknownAndChangedHostsBeforeCommands(t *testing.T) {
	for _, trust := range []string{"matching", "unknown", "changed"} {
		t.Run(trust, func(t *testing.T) {
			_, serverPrivate, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			serverKey, err := xssh.NewSignerFromKey(serverPrivate)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			var commands atomic.Int32
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				connection, err := listener.Accept()
				if err != nil {
					return
				}
				defer connection.Close()
				config := &xssh.ServerConfig{NoClientAuth: true}
				config.AddHostKey(serverKey)
				server, channels, requests, err := xssh.NewServerConn(connection, config)
				if err != nil {
					return
				}
				defer server.Close()
				go xssh.DiscardRequests(requests)
				for channel := range channels {
					stream, requests, err := channel.Accept()
					if err != nil {
						return
					}
					for request := range requests {
						if request.Type != "exec" {
							_ = request.Reply(false, nil)
							continue
						}
						commands.Add(1)
						_ = request.Reply(true, nil)
						_, _ = io.WriteString(stream, "verified-install-target\n")
						_, _ = stream.SendRequest("exit-status", false, xssh.Marshal(struct{ Status uint32 }{}))
						_ = stream.Close()
						break
					}
				}
			}()
			t.Cleanup(func() { _ = listener.Close(); <-finished })
			_, userPrivate, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := x509.MarshalPKCS8PrivateKey(userPrivate)
			if err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			privatePath := filepath.Join(directory, "identity")
			if err := os.WriteFile(privatePath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), 0o600); err != nil {
				t.Fatal(err)
			}
			pin := serverKey.PublicKey()
			if trust == "changed" {
				_, otherPrivate, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				other, err := xssh.NewSignerFromKey(otherPrivate)
				if err != nil {
					t.Fatal(err)
				}
				pin = other.PublicKey()
			}
			content := ""
			if trust != "unknown" {
				content = knownhosts.Line([]string{listener.Addr().String()}, pin) + "\n"
			}
			trustPath := filepath.Join(directory, "known_hosts")
			if err := os.WriteFile(trustPath, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().(*net.TCPAddr)
			client, err := NewClient("127.0.0.1", address.Port, "operator", privatePath)
			if err != nil {
				t.Fatal(err)
			}
			err = client.ConnectVerified(trustPath)
			if trust != "matching" {
				if err == nil {
					_ = client.Close()
					t.Fatal("unverified target was accepted")
				}
				if commands.Load() != 0 {
					t.Fatal("a command ran on an unverified target")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			output, err := client.Run("inspect-install-target")
			if err != nil || output != "verified-install-target\n" || commands.Load() != 1 {
				t.Fatalf("verified target command: %q, %v, commands=%d", output, err, commands.Load())
			}
		})
	}
}
