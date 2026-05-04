package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"

	"github.com/openctl/openctl/internal/config"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

func newPingCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ping",
		Short: "Ping the controller",
		Long: `Sends a Ping RPC to the controller and prints its version.

Useful for verifying connectivity, TLS, and auth in one command. Reads the
controller URL, token file path, and CA cert path from ~/.openctl/config.yaml
(see "controller:" section). Defaults are set up for a local-Mac install at
~/.openctl/controller/.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPing(cmd.Context())
		},
	}
}

func runPing(ctx context.Context) error {
	conn, token, err := dialController()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	client := apiv1.NewPingServiceClient(conn)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)

	resp, err := client.Ping(ctx, &apiv1.PingRequest{Message: "ping"})
	if err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	fmt.Printf("ok: echo=%q server-version=%s\n", resp.GetEcho(), resp.GetServerVersion())
	return nil
}

// DialController returns a gRPC client connection to the controller plus the
// API token to send on each request. Defaults match the local-Mac install
// layout so a fresh controller works without any config edits. Exported so
// other CLI commands (apply/get/delete) reuse the same connection setup.
func DialController() (*grpc.ClientConn, string, error) {
	return dialController()
}

func dialController() (*grpc.ClientConn, string, error) {
	cc := globalConfig.Controller
	if cc == nil {
		cc = &config.Controller{}
	}

	url := cc.URL
	if url == "" {
		url = "127.0.0.1:9444"
	}
	url = strings.TrimPrefix(url, "https://")
	url = strings.TrimPrefix(url, "http://")

	tokenFile := cc.TokenFile
	if tokenFile == "" {
		home, _ := os.UserHomeDir()
		tokenFile = filepath.Join(home, ".openctl", "controller", "token")
	}
	caFile := cc.CAFile
	if caFile == "" {
		home, _ := os.UserHomeDir()
		caFile = filepath.Join(home, ".openctl", "controller", "tls", "ca.crt")
	}

	tokenData, err := os.ReadFile(tokenFile) // #nosec G304 -- path from config
	if err != nil {
		return nil, "", fmt.Errorf("read token from %s: %w", tokenFile, err)
	}
	token := strings.TrimSpace(string(tokenData))
	if token == "" {
		return nil, "", fmt.Errorf("token file %s is empty", tokenFile)
	}

	caData, err := os.ReadFile(caFile) // #nosec G304 -- path from config
	if err != nil {
		return nil, "", fmt.Errorf("read CA from %s: %w", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caData) {
		return nil, "", fmt.Errorf("parse CA %s: no PEM blocks found", caFile)
	}

	creds := credentials.NewTLS(&tls.Config{
		RootCAs:    pool,
		MinVersion: tls.VersionTLS12,
	})

	conn, err := grpc.NewClient(url, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, "", fmt.Errorf("dial controller %s: %w", url, err)
	}
	return conn, token, nil
}
