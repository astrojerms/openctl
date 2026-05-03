// openctl-controller is the persistent reconciler that backs the openctl
// CLI. See CONTROLLER.md for the architecture and DEVELOPMENT.md for the
// dev workflow.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/server"
	"github.com/openctl/openctl/internal/controller/storage"
	tlspkg "github.com/openctl/openctl/internal/controller/tls"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		printUsage()
		return fmt.Errorf("missing subcommand")
	}
	switch os.Args[1] {
	case "serve":
		return runServe(os.Args[2:])
	case "version":
		fmt.Println(server.ServerVersion)
		return nil
	case "-h", "--help", "help":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("unknown subcommand %q", os.Args[1])
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `usage: openctl-controller <subcommand> [flags]

Subcommands:
  serve     Run the controller in the foreground
  version   Print the controller version
  help      Show this message

Run 'openctl-controller serve --help' for serve flags.`)
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "controller state directory")
	listen := fs.String("listen", "127.0.0.1:9444", "gRPC listen address (host:port)")
	noAuth := fs.Bool("no-auth", false, "disable token auth (only for localhost-only setups)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	tokenPath := filepath.Join(*dir, "token")
	token, err := auth.LoadOrCreateToken(tokenPath)
	if err != nil {
		return err
	}

	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return fmt.Errorf("parse listen %q: %w", *listen, err)
	}
	tlsDir := filepath.Join(*dir, "tls")
	mat, err := tlspkg.EnsureMaterial(tlsDir, host, []net.IP{
		net.ParseIP("127.0.0.1"),
		net.ParseIP("::1"),
	})
	if err != nil {
		return fmt.Errorf("ensure tls material: %w", err)
	}

	dbPath := filepath.Join(*dir, "state.db")
	db, err := storage.Open(context.Background(), dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	opts := server.Options{
		Listen:   *listen,
		CertFile: mat.ServerCertPath,
		KeyFile:  mat.ServerKeyPath,
	}
	if !*noAuth {
		opts.Token = token
	}

	srv, err := server.New(opts)
	if err != nil {
		return err
	}

	log.Printf("openctl-controller %s listening on %s", server.ServerVersion, *listen)
	log.Printf("  state dir: %s", *dir)
	log.Printf("  ca cert:   %s", mat.CACertPath)
	if *noAuth {
		log.Printf("  auth:      DISABLED (--no-auth)")
	} else {
		log.Printf("  token:     %s", tokenPath)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve() }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return err
	case sig := <-sigCh:
		log.Printf("received %s, shutting down", sig)
		srv.Stop()
		return nil
	}
}

func defaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "./openctl-controller"
	}
	return filepath.Join(home, ".openctl", "controller")
}
