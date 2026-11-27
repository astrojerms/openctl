package apps

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func sshTestIdentity(t *testing.T) (Target, ssh.Signer) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	return Target{Host: "127.0.0.1", User: "test", Port: 22, PrivateKeyFile: keyFile, KnownHostsFile: filepath.Join(dir, "known_hosts")}, signer
}

func TestSSHRequiresVerifiedKnownHostsBeforeDial(t *testing.T) {
	target, _ := sshTestIdentity(t)
	_, err := (sshRunner{}).Run(context.Background(), target, "true", nil)
	if err == nil || !strings.Contains(err.Error(), "verified SSH host key") {
		t.Fatalf("missing known_hosts must fail closed: %v", err)
	}
	var before *preCommandError
	if !errors.As(err, &before) {
		t.Fatalf("local setup failure lost no-command evidence: %v", err)
	}
}

func TestSSHRejectsUnknownHostKey(t *testing.T) {
	target, signer := sshTestIdentity(t)
	if err := os.WriteFile(target.KnownHostsFile, nil, 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	target.Port = listener.Addr().(*net.TCPAddr).Port
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		config := &ssh.ServerConfig{NoClientAuth: true}
		config.AddHostKey(signer)
		server, _, _, err := ssh.NewServerConn(conn, config)
		if err == nil {
			server.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = (sshRunner{}).Run(ctx, target, "true", nil)
	if err == nil || !strings.Contains(err.Error(), "host key") {
		t.Fatalf("unknown host key accepted: %v", err)
	}
	var before *preCommandError
	if !errors.As(err, &before) {
		t.Fatalf("host verification failure lost no-command evidence: %v", err)
	}
	<-done
}

func TestSSHHandshakeHonorsCancellation(t *testing.T) {
	target, signer := sshTestIdentity(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	target.Port = listener.Addr().(*net.TCPAddr).Port
	line := knownhosts.Line([]string{knownhosts.Normalize(listener.Addr().String())}, signer.PublicKey()) + "\n"
	if err := os.WriteFile(target.KnownHostsFile, []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	connected := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			connected <- conn
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := (sshRunner{}).Run(ctx, target, "true", nil); done <- err }()
	select {
	case conn := <-connected:
		defer conn.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("SSH never connected")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled handshake succeeded")
		}
		var before *preCommandError
		if !errors.As(err, &before) {
			t.Fatalf("cancelled handshake lost no-command evidence: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SSH handshake ignored context cancellation")
	}
}

func TestShellQuotingAndOutputBound(t *testing.T) {
	if got := quote("a'b;$(touch /tmp/no)"); got != "'a'\"'\"'b;$(touch /tmp/no)'" {
		t.Fatalf("unsafe quoting: %q", got)
	}
	var b boundedOutput
	data := []byte(strings.Repeat("x", 1024*1024+1))
	n, err := b.Write(data)
	if err != nil || n != len(data) || !b.truncated || b.Len() != 1024*1024 {
		t.Fatal("output limit did not bound memory")
	}
}

func TestSSHExecutionBoundaryRetainsLocatorOnRemoteExitOrLostReply(t *testing.T) {
	for _, lostReply := range []bool{false, true} {
		t.Run(fmt.Sprintf("lost-reply-%v", lostReply), func(t *testing.T) {
			target, signer := sshTestIdentity(t)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			target.Port = listener.Addr().(*net.TCPAddr).Port
			line := knownhosts.Line([]string{knownhosts.Normalize(listener.Addr().String())}, signer.PublicKey()) + "\n"
			if err := os.WriteFile(target.KnownHostsFile, []byte(line), 0600); err != nil {
				t.Fatal(err)
			}
			executed := make(chan struct{})
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				config := &ssh.ServerConfig{NoClientAuth: true}
				config.AddHostKey(signer)
				server, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					channel, requests, err := incoming.Accept()
					if err != nil {
						return
					}
					for request := range requests {
						if request.Type != "exec" {
							_ = request.Reply(false, nil)
							continue
						}
						close(executed)
						if lostReply {
							return
						}
						_ = request.Reply(true, nil)
						_, _ = channel.SendRequest("exit-status", false, []byte{0, 0, 0, 42})
						_ = channel.Close()
						return
					}
				}
			}()
			p, state := testProvider()
			r := composeResource()
			r.Spec["target"] = targetSpec(target)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err = p.Apply(ctx, r)
			var before *preCommandError
			if err == nil || errors.As(err, &before) || len(state.values) != 1 || !strings.Contains(err.Error(), "locator retained") {
				t.Fatalf("remote execution uncertainty must retain binding without pre-command evidence: %v", err)
			}
			select {
			case <-executed:
			default:
				t.Fatal("test server did not receive an SSH exec request")
			}
		})
	}
}
