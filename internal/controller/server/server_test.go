package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	tlspkg "github.com/openctl/openctl/internal/controller/tls"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

// TestServerEndToEndPing spins up the real Server with real TLS material and
// verifies a properly authenticated client gets a response, while one
// without a token is rejected.
func TestServerEndToEndPing(t *testing.T) {
	dir := t.TempDir()
	mat, err := tlspkg.EnsureMaterial(dir, "localhost", []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("tls material: %v", err)
	}

	const tok = "deadbeef-token"
	srv, err := New(Options{
		CertFile: mat.ServerCertPath,
		KeyFile:  mat.ServerKeyPath,
		Token:    tok,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.ServeListener(ln) }()
	t.Cleanup(srv.Stop)

	addr := ln.Addr().String()

	t.Run("authorized client succeeds", func(t *testing.T) {
		conn := dial(t, addr, mat.CACertPath)
		defer func() { _ = conn.Close() }()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)

		resp, err := apiv1.NewPingServiceClient(conn).Ping(ctx, &apiv1.PingRequest{Message: "hello"})
		if err != nil {
			t.Fatalf("Ping: %v", err)
		}
		if resp.GetEcho() != "hello" {
			t.Errorf("echo = %q, want hello", resp.GetEcho())
		}
		if resp.GetServerVersion() != ServerVersion {
			t.Errorf("serverVersion = %q, want %q", resp.GetServerVersion(), ServerVersion)
		}
	})

	t.Run("unauthorized client rejected", func(t *testing.T) {
		conn := dial(t, addr, mat.CACertPath)
		defer func() { _ = conn.Close() }()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// no auth metadata

		_, err := apiv1.NewPingServiceClient(conn).Ping(ctx, &apiv1.PingRequest{})
		if err == nil {
			t.Fatal("want auth error, got nil")
		}
		st, ok := status.FromError(err)
		if !ok {
			t.Fatalf("error not a gRPC status: %v", err)
		}
		if st.Code() != codes.Unauthenticated {
			t.Errorf("code = %v, want Unauthenticated", st.Code())
		}
	})
}

// TestServerNoAuthMode exercises the --no-auth path: an unauthenticated
// client should succeed.
func TestServerNoAuthMode(t *testing.T) {
	dir := t.TempDir()
	mat, err := tlspkg.EnsureMaterial(dir, "localhost", []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{
		CertFile: mat.ServerCertPath,
		KeyFile:  mat.ServerKeyPath,
		// Token: "" => no auth
	})
	if err != nil {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() { _ = srv.ServeListener(ln) }()
	t.Cleanup(srv.Stop)

	conn := dial(t, ln.Addr().String(), mat.CACertPath)
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := apiv1.NewPingServiceClient(conn).Ping(ctx, &apiv1.PingRequest{Message: "no-auth"})
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if resp.GetEcho() != "no-auth" {
		t.Errorf("echo = %q, want no-auth", resp.GetEcho())
	}
}

func dial(t *testing.T, addr, caPath string) *grpc.ClientConn {
	t.Helper()
	caData, err := os.ReadFile(caPath) // #nosec G304 -- test path
	if err != nil {
		t.Fatalf("read ca: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caData) {
		t.Fatal("append ca")
	}
	creds := credentials.NewTLS(&tls.Config{
		RootCAs:    pool,
		ServerName: "localhost",
		MinVersion: tls.VersionTLS12,
	})
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	return conn
}
