package cli

import (
	"context"
	"crypto/tls"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/openctl/openctl/internal/config"
	controllertls "github.com/openctl/openctl/internal/controller/tls"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

type authenticatedPing struct {
	apiv1.UnimplementedPingServiceServer
}

func (authenticatedPing) Ping(ctx context.Context, _ *apiv1.PingRequest) (*apiv1.PingResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	auth := md.Get("authorization")
	if len(auth) != 1 || auth[0] != "Bearer fixture-token" {
		return nil, status.Error(codes.Unauthenticated, "invalid fixture credential")
	}
	return &apiv1.PingResponse{}, nil
}

func TestDialControllerHomeCredentialPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	previous := globalConfig
	t.Cleanup(func() { globalConfig = previous })
	state := filepath.Join(home, ".openctl", "controller")
	material, err := controllertls.EnsureMaterial(filepath.Join(state, "tls"), "localhost", []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(state, "token")
	if err := os.WriteFile(tokenPath, []byte(" fixture-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cert, err := tls.LoadX509KeyPair(material.ServerCertPath, material.ServerKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}})))
	apiv1.RegisterPingServiceServer(server, authenticatedPing{})
	t.Cleanup(server.Stop)
	go func() { _ = server.Serve(listener) }()
	for _, tc := range []struct {
		name, token, ca string
	}{
		{"absolute", tokenPath, material.CACertPath},
		{"token-home", "~/.openctl/controller/token", material.CACertPath},
		{"ca-home", tokenPath, "~/.openctl/controller/tls/ca.crt"},
		{"both-home", "~/.openctl/controller/token", "~/.openctl/controller/tls/ca.crt"},
		{"local-defaults", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Controller: &config.Controller{URL: listener.Addr().String(), TokenFile: tc.token, CAFile: tc.ca}}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := cfg.SaveToFile(path); err != nil {
				t.Fatal(err)
			}
			globalConfig, err = config.LoadFromFile(path)
			if err != nil {
				t.Fatal(err)
			}
			conn, token, err := dialController()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
			if _, err := apiv1.NewPingServiceClient(conn).Ping(ctx, &apiv1.PingRequest{}); err != nil {
				t.Fatalf("authenticate using configured credential paths: %v", err)
			}
		})
	}
}
