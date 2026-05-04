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

	"github.com/openctl/openctl/internal/config"
	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/providers"
	pmprovider "github.com/openctl/openctl/internal/controller/providers/proxmox"
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

	registry, registered, err := buildRegistry()
	if err != nil {
		return err
	}

	opts := server.Options{
		Listen:   *listen,
		CertFile: mat.ServerCertPath,
		KeyFile:  mat.ServerKeyPath,
		Registry: registry,
	}
	if !*noAuth {
		opts.Token = token
	}

	srv, err := server.New(opts)
	if err != nil {
		return err
	}

	log.Printf("openctl-controller %s listening on %s", server.ServerVersion, *listen)
	log.Printf("  state dir:   %s", *dir)
	log.Printf("  ca cert:     %s", mat.CACertPath)
	if *noAuth {
		log.Printf("  auth:        DISABLED (--no-auth)")
	} else {
		log.Printf("  token:       %s", tokenPath)
	}
	if len(registered) == 0 {
		log.Printf("  providers:   (none — add a `proxmox:` section to ~/.openctl/config.yaml to enable)")
	} else {
		for _, name := range registered {
			log.Printf("  provider:    %s", name)
		}
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

// buildRegistry constructs the Provider registry from ~/.openctl/config.yaml.
// Currently registers only the proxmox provider, using the default context's
// credentials. Returns the registry plus a list of registered provider names
// for logging.
//
// If the config file or proxmox section is missing, the registry is left
// empty — the controller still starts; resource RPCs will return errors
// pointing the user at the missing config.
func buildRegistry() (*providers.Registry, []string, error) {
	registry := providers.NewRegistry()
	var names []string

	cfg, err := config.Load()
	if err != nil {
		// Missing config file is acceptable — controller starts empty.
		return registry, nil, nil
	}

	if _, ok := cfg.Providers["proxmox"]; ok {
		pcfg, err := cfg.GetProviderConfig("proxmox", "")
		if err != nil {
			return nil, nil, fmt.Errorf("load proxmox config: %w", err)
		}
		if pcfg.Endpoint != "" {
			registry.Register(pmprovider.New(pcfg))
			names = append(names, "proxmox")
		}
	}

	return registry, names, nil
}
