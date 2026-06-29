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
	"time"

	"github.com/openctl/openctl/internal/config"
	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/internal/controller/providers"
	k3sprovider "github.com/openctl/openctl/internal/controller/providers/k3s"
	pmprovider "github.com/openctl/openctl/internal/controller/providers/proxmox"
	"github.com/openctl/openctl/internal/controller/server"
	"github.com/openctl/openctl/internal/controller/storage"
	tlspkg "github.com/openctl/openctl/internal/controller/tls"
	"github.com/openctl/openctl/pkg/protocol"
)

// retainPerResource controls how many completed ops per resource the GC
// keeps around. Reasonable for a homelab; configurable later if needed.
const retainPerResource = 50

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
	case "install":
		return runInstall(os.Args[2:])
	case "uninstall":
		return runUninstall(os.Args[2:])
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
  serve      Run the controller in the foreground
  install    Install the controller as a per-user LaunchAgent (macOS)
  uninstall  Remove the LaunchAgent install
  version    Print the controller version
  help       Show this message

Run 'openctl-controller <subcommand> --help' for per-subcommand flags.`)
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "controller state directory")
	listen := fs.String("listen", "127.0.0.1:9444", "gRPC listen address (host:port)")
	httpListen := fs.String("http-listen", "127.0.0.1:9445", "HTTP gateway + UI listen address (empty to disable)")
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
	ctx := context.Background()
	db, err := storage.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	registry, registered, err := buildRegistry()
	if err != nil {
		return err
	}

	// Operations store + dispatcher. On startup, mark any ops that were
	// running when the previous controller died as `interrupted` — this is
	// the "no auto-resume" half of the operation-model decision.
	opStore := operations.New(db, retainPerResource)
	if n, err := opStore.MarkRunningInterrupted(ctx); err != nil {
		return fmt.Errorf("mark interrupted ops: %w", err)
	} else if n > 0 {
		log.Printf("marked %d previously-running operation(s) as interrupted", n)
	}
	manifestStore := manifests.New(db)

	// UI Phase U2.1: optionally mirror the controller's desired-state to a
	// directory on disk so users (and git) can see the applied config. The
	// SQLite store is canonical; the disk tree is a materialized projection.
	// `sink` becomes the dispatcher's ManifestSink — either the bare store
	// or the DiskMirror wrapping it.
	var sink operations.ManifestSink = manifestStore
	var diskMirror *manifests.DiskMirror
	var gitRepo *manifests.Repo
	if cfg, err := config.Load(); err == nil {
		mirror, mErr := buildDiskMirror(ctx, manifestStore, cfg)
		if mErr != nil {
			return fmt.Errorf("init disk mirror: %w", mErr)
		}
		if mirror != nil {
			sink = mirror
			diskMirror = mirror
			log.Printf("  manifests:   %s", mirror.Root())

			// UI Phase U2.2: optionally wire git tracking on top of the disk
			// mirror. Each successful apply/delete commits to the local repo
			// with a structured message; remote push is governed by the
			// configured push mode. Hook is best-effort — git failures log
			// but never bubble back into the dispatcher (apply ops never
			// fail because of a flaky remote).
			gitRepo, err = buildGitRepo(ctx, mirror, cfg)
			if err != nil {
				return fmt.Errorf("init git tracking: %w", err)
			}
			if gitRepo != nil {
				log.Printf("  git:         %s (branch=%s pushMode=%s)",
					gitRepo.Dir(), gitRepo.Branch(), gitRepo.PushMode())
				if gitRepo.Remote() != "" {
					log.Printf("  git remote:  %s", gitRepo.Remote())
				}
				gitRepo.StartPeriodicPush(ctx, log.Printf)
			}
		}
	}

	dispatcher := operations.NewDispatcher(opStore, registry, sink, 0)
	dispatcher.Start(ctx)
	defer dispatcher.Stop()

	sessionStore := auth.NewSessionStore(db)

	opts := server.Options{
		Listen:     *listen,
		CertFile:   mat.ServerCertPath,
		KeyFile:    mat.ServerKeyPath,
		Registry:   registry,
		Operations: opStore,
		Dispatcher: dispatcher,
		Manifests:  manifestStore,
		Sessions:   sessionStore,
		DiskMirror: diskMirror,
		Repo:       gitRepo,
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

	// HTTP gateway (UI Phase U1.3+U1.5). Dials the gRPC listener over TLS
	// using the CA we generated above; serves /v1/* (REST) and /ui/*
	// (embed.FS assets) on a sibling port. Disable with --http-listen="".
	if *httpListen != "" {
		caBytes, err := os.ReadFile(mat.CACertPath) // #nosec G304 -- controller-owned path
		if err != nil {
			return fmt.Errorf("read CA cert: %w", err)
		}
		go func() {
			log.Printf("openctl-controller HTTP gateway listening on %s", *httpListen)
			log.Printf("  UI:          http://%s/ui/", *httpListen)
			if err := server.ServeHTTPGateway(ctx, *httpListen, *listen, caBytes, host); err != nil {
				errCh <- fmt.Errorf("http gateway: %w", err)
			}
		}()
	}

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

// buildGitRepo initializes a git repo over the disk mirror when config has
// `manifests.git.enabled: true`. Returns nil when disabled (the disk mirror
// works fine without git). Attaches a post-write hook to the mirror so
// every materialize/delete becomes a commit; periodic push (when
// configured) is started by the caller via repo.StartPeriodicPush.
func buildGitRepo(ctx context.Context, mirror *manifests.DiskMirror, cfg *config.Config) (*manifests.Repo, error) {
	if cfg == nil || cfg.Manifests == nil || cfg.Manifests.Git == nil || !cfg.Manifests.Git.Enabled {
		return nil, nil
	}
	g := cfg.Manifests.Git

	var interval time.Duration
	if g.PushInterval != "" {
		d, err := time.ParseDuration(g.PushInterval)
		if err != nil {
			return nil, fmt.Errorf("parse manifests.git.pushInterval %q: %w", g.PushInterval, err)
		}
		interval = d
	}

	repo, err := manifests.NewRepo(manifests.RepoOptions{
		Dir:          mirror.Root(),
		Branch:       g.Branch,
		Remote:       g.Remote,
		PushMode:     g.PushMode,
		PushInterval: interval,
	})
	if err != nil {
		return nil, err
	}
	if err := repo.EnsureInit(ctx); err != nil {
		return nil, fmt.Errorf("git init: %w", err)
	}
	mirror.SetHook(manifests.GitHook(repo, repo.PushMode() == manifests.PushModeOnCommit))
	return repo, nil
}

// buildDiskMirror resolves the manifest-mirror root from config (defaulting
// to ~/.openctl/manifests) and returns a wrapped Store ready for the
// dispatcher. Returns nil if the user explicitly disables the mirror via
// `manifests: { dir: "" }` in their config — the bare store still gets
// used in that case.
//
// Runs a startup reconciliation so files that vanished while the controller
// was down (e.g. user wiped ~/.openctl/manifests/) get re-materialized
// before the dispatcher resumes. Orphan files (no matching SQLite row) are
// logged but left alone.
func buildDiskMirror(ctx context.Context, store *manifests.Store, cfg *config.Config) (*manifests.DiskMirror, error) {
	root, err := resolveManifestDir(cfg)
	if err != nil {
		return nil, err
	}
	if root == "" {
		return nil, nil
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create manifest dir: %w", err)
	}
	mirror := manifests.NewDiskMirror(store, root)
	report, err := mirror.Reconcile(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconcile manifest dir: %w", err)
	}
	for _, ref := range report.MissingOnDisk {
		log.Printf("  manifests:   re-materialized missing file for %s/%s", ref.Kind, ref.Name)
	}
	for _, rel := range report.OrphansOnDisk {
		log.Printf("  manifests:   orphan file (no applied_manifests row): %s", rel)
	}
	return mirror, nil
}

// resolveManifestDir returns the absolute path the disk mirror should write
// into. A nil config or empty `manifests:` block falls back to
// ~/.openctl/manifests. Returns "" only when the user explicitly opts out
// with `manifests: { dir: "" }` *and* the manifests block is present (we
// can't tell "absent" from "present-empty" with YAML; the convention is
// "if you wrote `manifests:` at all, you wanted the default unless you
// also set dir to something else").
func resolveManifestDir(cfg *config.Config) (string, error) {
	dir := ""
	if cfg != nil && cfg.Manifests != nil {
		dir = cfg.Manifests.Dir
	}
	if dir == "" {
		paths, err := config.GetPaths()
		if err != nil {
			return "", err
		}
		return paths.ManifestsDir, nil
	}
	return config.ExpandPath(dir)
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

	var pmp *pmprovider.Provider
	if _, ok := cfg.Providers["proxmox"]; ok {
		pcfg, err := cfg.GetProviderConfig("proxmox", "")
		if err != nil {
			return nil, nil, fmt.Errorf("load proxmox config: %w", err)
		}
		if pcfg.Endpoint != "" {
			pmp = pmprovider.New(pcfg)
			registry.Register(pmp)
			names = append(names, "proxmox")
		}
	}

	// k3s provider needs a VM provider to drive child VM ops; today that's
	// always proxmox. If proxmox isn't configured, k3s isn't usable.
	if pmp != nil {
		registry.Register(k3sprovider.New(&protocol.ProviderConfig{}, pmp))
		names = append(names, "k3s")
	}

	return registry, names, nil
}
