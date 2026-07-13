// openctl-controller is the persistent reconciler that backs the openctl
// CLI. See CONTROLLER.md for the architecture and DEVELOPMENT.md for the
// dev workflow.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/openctl/openctl/internal/config"
	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/internal/controller/providers"
	externalprovider "github.com/openctl/openctl/internal/controller/providers/external"
	k3sprovider "github.com/openctl/openctl/internal/controller/providers/k3s"
	pmprovider "github.com/openctl/openctl/internal/controller/providers/proxmox"
	tfhostprovider "github.com/openctl/openctl/internal/controller/providers/tfhost"
	"github.com/openctl/openctl/internal/controller/providerstate"
	"github.com/openctl/openctl/internal/controller/reconciler"
	"github.com/openctl/openctl/internal/controller/secrets"
	"github.com/openctl/openctl/internal/controller/server"
	"github.com/openctl/openctl/internal/controller/storage"
	tlspkg "github.com/openctl/openctl/internal/controller/tls"
	"github.com/openctl/openctl/internal/schema"
	"github.com/openctl/openctl/internal/templates"
	"github.com/openctl/openctl/pkg/pluginproto"
	"github.com/openctl/openctl/pkg/protocol"
)

// resolveRetainPerResource reads operations.retainPerResource from config,
// falling back to config.DefaultRetainPerResource. Startup-only: the value is
// baked into the operations store at construction, so editing it via the
// ConfigService takes effect on the next controller start.
func resolveRetainPerResource() int {
	cfg, err := config.Load()
	if err != nil || cfg == nil || cfg.Operations == nil || cfg.Operations.RetainPerResource <= 0 {
		return config.DefaultRetainPerResource
	}
	return cfg.Operations.RetainPerResource
}

// registerConfiguredSecretProviders builds and registers each configured
// secret backend (Tier 2), returning display names for the startup log. An
// unknown type or a missing required field is a hard error — a misconfigured
// backend must surface loudly, not silently leave a $secret marker unresolved.
func registerConfiguredSecretProviders(reg *secrets.Registry, providers []config.SecretProviderConfig) ([]string, error) {
	var names []string
	for _, pc := range providers {
		if pc.Name == "" {
			return nil, fmt.Errorf("a secret provider is missing its name")
		}
		switch pc.Type {
		case "vault":
			if pc.Address == "" {
				return nil, fmt.Errorf("secret provider %q (vault) requires an address", pc.Name)
			}
			token, err := pc.ResolveToken()
			if err != nil {
				return nil, fmt.Errorf("secret provider %q token: %w", pc.Name, err)
			}
			reg.Register(secrets.NewVaultProvider(pc.Name, pc.Address, token, pc.Namespace))
			names = append(names, pc.Name+" (vault)")
		default:
			return nil, fmt.Errorf("secret provider %q: unknown type %q", pc.Name, pc.Type)
		}
	}
	return names, nil
}

// oidcConfig reads config.auth.oidc, resolving the client secret from its file.
// Returns (nil, nil) when OIDC is absent or disabled, (nil, err) when it's
// enabled but the secret can't be read.
func oidcConfig() (*auth.OIDCConfig, error) {
	cfg, err := config.Load()
	if err != nil || cfg == nil || cfg.Auth == nil || cfg.Auth.OIDC == nil || !cfg.Auth.OIDC.Enabled {
		return nil, nil //nolint:nilerr // a missing/unreadable config just means "OIDC off"
	}
	o := cfg.Auth.OIDC
	secret, err := o.ResolveClientSecret()
	if err != nil {
		return nil, fmt.Errorf("read client secret: %w", err)
	}
	return &auth.OIDCConfig{
		Issuer:        o.Issuer,
		ClientID:      o.ClientID,
		ClientSecret:  secret,
		RedirectURL:   o.RedirectURL,
		RoleClaim:     o.RoleClaim,
		RoleMapping:   o.RoleMapping,
		DefaultRole:   o.DefaultRole,
		UsernameClaim: o.UsernameClaim,
	}, nil
}

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
		fmt.Printf("%s (commit=%s built=%s)\n",
			server.ServerVersion, server.GitCommit, server.BuildTime)
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

	users, err := auth.LoadUsers(*dir)
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
	// Root context canceled on SIGINT/SIGTERM. Every subsystem below
	// (dispatcher, reconciler, HTTP gateway, periodic git push) takes
	// this ctx and stops when it fires — without cancellation the
	// gRPC GracefulStop below waits forever for UI Watch streams that
	// have no reason to disconnect.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	db, err := storage.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	registry, registered, closePlugins, err := buildRegistry(ctx, providerstate.New(db))
	if err != nil {
		return err
	}
	defer closePlugins()

	// Operations store + dispatcher. On startup, mark any ops that were
	// running when the previous controller died as `interrupted` — this is
	// the "no auto-resume" half of the operation-model decision.
	opStore := operations.New(db, resolveRetainPerResource())
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

	// Secret resolution ($secret markers). v1 registers the built-in `file`
	// (secrets read from <state-dir>/secrets, mode 0600) and `env` providers.
	// The resolved value reaches provider.Apply only; the persisted manifest
	// keeps the marker. Configured backends (Vault, cloud secret managers) and
	// external secret-provider plugins register here later without touching the
	// resolver or the redaction guarantee.
	secretsDir := filepath.Join(*dir, "secrets")
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		return fmt.Errorf("create secrets dir: %w", err)
	}
	secretsReg := secrets.NewRegistry()
	secrets.RegisterBuiltins(secretsReg, secretsDir)
	// action provider: resolve a $secret by running a resource action and
	// returning its output (e.g. a Cloudflare Tunnel's run token from get-token),
	// so an action's result wires into a manifest without manual copying.
	secretsReg.Register(secrets.NewActionProvider(func(ctx context.Context, av, kind, name, action string) (*secrets.ActionOutput, error) {
		res, err := registry.DoAction(ctx, av, kind, name, action, nil)
		if err != nil {
			return nil, err
		}
		return &secrets.ActionOutput{DownloadContent: res.DownloadContent, Message: res.Message, URL: res.URL}, nil
	}))
	secretProviderNames := []string{"file", "env", secrets.ActionProviderName}
	// Tier 2: register configured backends (Vault, ...) from config.secrets.
	if cfg, err := config.Load(); err == nil && cfg != nil && cfg.Secrets != nil {
		names, err := registerConfiguredSecretProviders(secretsReg, cfg.Secrets.Providers)
		if err != nil {
			return fmt.Errorf("configure secret providers: %w", err)
		}
		secretProviderNames = append(secretProviderNames, names...)
	}
	dispatcher.SetSecrets(secretsReg)
	log.Printf("  secrets:     %s (providers: %s)", secretsDir, strings.Join(secretProviderNames, ", "))

	dispatcher.Start(ctx)
	defer dispatcher.Stop()

	// gitOpsWebhook, when the push-triggered reconcile is configured, is built
	// in the GitOps block below and mounted on the HTTP gateway further down.
	var gitOpsWebhook *server.GitOpsWebhook

	// Two-way GitOps: fsnotify watcher on the mirror dir. File edits
	// become Apply ops tagged source="gitops". Opt-in (config
	// manifests.gitops.enabled: true) — default remains one-way
	// mirror. Requires the disk mirror to be configured; without
	// it there's no directory to watch.
	if diskMirror != nil {
		if cfg2, err := config.Load(); err == nil && cfg2 != nil &&
			cfg2.Manifests != nil && cfg2.Manifests.GitOps != nil &&
			cfg2.Manifests.GitOps.Enabled {
			applyFn := func(ctx context.Context, r *protocol.Resource) error {
				mJSON, err := json.Marshal(r)
				if err != nil {
					return fmt.Errorf("encode manifest: %w", err)
				}
				_, err = opStore.Submit(ctx, &operations.Operation{
					Type:         operations.TypeApply,
					APIVersion:   r.APIVersion,
					Kind:         r.Kind,
					ResourceName: r.Metadata.Name,
					ManifestJSON: string(mJSON),
					Source:       manifests.SourceGitOps,
				})
				if err != nil {
					return err
				}
				dispatcher.Notify()
				return nil
			}
			var deleteFn manifests.GitOpsDeleteFunc
			if cfg2.Manifests.GitOps.DeleteOnRemove {
				deleteFn = func(ctx context.Context, av, kind, name string) error {
					_, err := opStore.Submit(ctx, &operations.Operation{
						Type:         operations.TypeDelete,
						APIVersion:   av,
						Kind:         kind,
						ResourceName: name,
						Source:       manifests.SourceGitOps,
					})
					if err != nil {
						return err
					}
					dispatcher.Notify()
					return nil
				}
			}
			gitOpsWatcher := manifests.NewWatcher(diskMirror.Root(), manifestStore, applyFn, deleteFn)
			if err := gitOpsWatcher.Start(ctx); err != nil {
				log.Printf("  gitops:      failed to start watcher: %v", err)
			} else {
				defer gitOpsWatcher.Stop()
				log.Printf("  gitops:      enabled (deleteOnRemove=%v)", cfg2.Manifests.GitOps.DeleteOnRemove)

				// Git-as-source: periodically pull the remote into the mirror
				// and reconcile via the watcher's Sync, so committing to the
				// remote brings the infra up. Requires a remote (from git
				// tracking) and pull.enabled.
				if pull := cfg2.Manifests.GitOps.Pull; pull != nil && pull.Enabled {
					if gitRepo == nil || gitRepo.Remote() == "" {
						log.Printf("  gitops:      pull enabled but no git remote configured — skipping")
					} else {
						interval := parseDurationDefault(pull.Interval, time.Minute)
						onChange := gitOpsWatcher.Sync
						if pull.Prune {
							// Repo-wide prune: after each pull, delete managed
							// resources whose file left the repo. Provenance
							// (latest successful apply's source) comes from the
							// ops table; deletes go through the same gitops-sourced
							// Delete-op path as deleteOnRemove.
							sourceOf := func(ctx context.Context, av, kind, name string) (string, bool) {
								ops, err := opStore.List(ctx, operations.ListFilter{
									Status: operations.StatusSucceeded, APIVersion: av, Kind: kind, ResourceName: name, Limit: 20,
								})
								if err != nil {
									return "", false
								}
								for _, op := range ops { // newest-first
									if op.Type == operations.TypeApply {
										return op.Source, true
									}
								}
								return "", false
							}
							pruneDelete := func(ctx context.Context, av, kind, name string) error {
								_, err := opStore.Submit(ctx, &operations.Operation{
									Type: operations.TypeDelete, APIVersion: av, Kind: kind, ResourceName: name,
									Source: manifests.SourceGitOps,
								})
								if err != nil {
									return err
								}
								dispatcher.Notify()
								return nil
							}
							pruner := manifests.NewPruner(manifestStore, diskMirror.Root(), sourceOf, pruneDelete)
							sync := gitOpsWatcher.Sync
							onChange = func(ctx context.Context) error {
								if err := sync(ctx); err != nil {
									return err
								}
								_, err := pruner.Prune(ctx)
								return err
							}
						}
						gitRepo.StartPeriodicPull(ctx, interval, onChange, log.Printf)
						log.Printf("  gitops:      git-as-source pull enabled (interval=%s, prune=%v)", interval, pull.Prune)

						// Push-triggered reconcile: mount a webhook that pulls +
						// reconciles on demand (same path as the ticker), so a
						// git push converges immediately instead of within one
						// interval. The handler is served by the HTTP gateway.
						if wh := pull.Webhook; wh != nil && wh.Enabled {
							repo := gitRepo
							trigger := func(ctx context.Context) error {
								_, err := repo.PullAndReconcile(ctx, onChange, log.Printf)
								return err
							}
							gitOpsWebhook = server.NewGitOpsWebhook(wh.Path, wh.Secret, trigger)
							whPath := wh.Path
							if whPath == "" {
								whPath = "/gitops/webhook"
							}
							log.Printf("  gitops:      push webhook enabled (path=%s, signed=%v)", whPath, wh.Secret != "")
						}
					}
				}
			}
		}
	}

	// Periodic drift reconciler. Disabled only when the config explicitly
	// sets `reconciler.enabled: false`. Default behavior: logs drift
	// transitions; auto-remediate only fires on resources annotated with
	// openctl.io/autoReconcile=true (opt-in per resource).
	rec, recStarted := buildReconciler(registry, manifestStore)
	if rec != nil {
		// Auto-apply hook: submit an Apply op the same way the
		// resource handler does, tagged with source="auto-reconcile"
		// so the op history shows why it fired.
		rec.WithAutoApply(func(ctx context.Context, desired *protocol.Resource) error {
			mJSON, err := json.Marshal(desired)
			if err != nil {
				return fmt.Errorf("encode manifest: %w", err)
			}
			_, err = opStore.Submit(ctx, &operations.Operation{
				Type:         operations.TypeApply,
				APIVersion:   desired.APIVersion,
				Kind:         desired.Kind,
				ResourceName: desired.Metadata.Name,
				ManifestJSON: string(mJSON),
				Source:       manifests.SourceAutoReconcile,
			})
			if err != nil {
				return err
			}
			dispatcher.Notify()
			return nil
		})
		rec.Start(ctx)
		defer rec.Stop()
		log.Printf("  reconciler:  enabled (interval=%s)", recStarted)
	} else {
		log.Printf("  reconciler:  disabled (config: reconciler.enabled=false)")
	}

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
		Templates:  buildTemplateRegistry(),
	}
	if !*noAuth {
		opts.Token = token
		opts.Users = users
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
		log.Printf("  token:       %s (root/admin)", tokenPath)
		for _, u := range users {
			log.Printf("  user:        %s (%s)", u.UserID, u.Role)
		}
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
		// OIDC login front door (browser SSO), when configured. The gateway is
		// TLS, so cookies are Secure. A discovery/config failure is fatal — an
		// operator who enabled OIDC shouldn't silently get a controller with no
		// SSO.
		var oidcHandler *server.OIDCHandler
		oc, err := oidcConfig()
		if err != nil {
			return fmt.Errorf("oidc config: %w", err)
		}
		if oc != nil {
			authn, err := auth.NewOIDCAuthenticator(ctx, *oc)
			if err != nil {
				return fmt.Errorf("oidc: %w", err)
			}
			oidcHandler = server.NewOIDCHandler(authn, sessionStore, true)
			log.Printf("  oidc:        enabled (issuer discovered)")
		}
		go func() {
			log.Printf("openctl-controller HTTP gateway listening on %s (HTTP/2 over TLS)", *httpListen)
			log.Printf("  UI:          https://%s/ui/", *httpListen)
			if err := server.ServeHTTPGateway(ctx, *httpListen, *listen, caBytes, host, mat.ServerCertPath, mat.ServerKeyPath, oidcHandler, gitOpsWebhook); err != nil {
				errCh <- fmt.Errorf("http gateway: %w", err)
			}
		}()
	}

	select {
	case err := <-errCh:
		// Server exited on its own — cancel ctx so background
		// subsystems (dispatcher, reconciler, HTTP gateway) stop
		// waiting on it before their deferred Stop() calls block.
		cancel()
		srv.StopWithTimeout(3 * time.Second)
		return err
	case <-ctx.Done():
		log.Printf("received interrupt, shutting down")
		// Give in-flight RPCs 3s to finish gracefully; force-close
		// streams after that so Ctrl-C actually exits even when the
		// UI has long-running Watch streams open. HTTP gateway,
		// dispatcher, reconciler, and periodic git push all took a
		// child of ctx and stop when it cancels — signal.NotifyContext
		// already canceled it before this branch runs.
		srv.StopWithTimeout(3 * time.Second)
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

// buildReconciler returns a started-or-skipped reconciler based on
// config.Reconciler. Defaults to enabled with the package default
// interval when the block is omitted. Returns (nil, "") when the user
// explicitly set `enabled: false` so main.go can log that state.
func buildReconciler(reg *providers.Registry, mstore *manifests.Store) (*reconciler.Reconciler, time.Duration) {
	cfg, err := config.Load()
	if err != nil || cfg == nil || cfg.Reconciler == nil {
		// No config block: defaults (enabled, DefaultInterval).
		return reconciler.New(reg, mstore, 0), reconciler.DefaultInterval
	}
	r := cfg.Reconciler
	if r.Enabled != nil && !*r.Enabled {
		return nil, 0
	}
	interval := reconciler.DefaultInterval
	if r.Interval != "" {
		if parsed, perr := time.ParseDuration(r.Interval); perr == nil {
			interval = parsed
		} else {
			log.Printf("config: reconciler.interval %q invalid, using default %s", r.Interval, interval)
		}
	}
	return reconciler.New(reg, mstore, interval), interval
}

// parseDurationDefault parses a duration string, falling back to def when the
// string is empty or invalid (a bad value logs and uses the default rather
// than failing startup).
func parseDurationDefault(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		log.Printf("config: invalid duration %q, using default %s", s, def)
		return def
	}
	return d
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

// resolveTemplatesDir mirrors resolveManifestDir for the user-template scan
// directory: honor cfg.Templates.Dir, else ~/.openctl/templates.
func resolveTemplatesDir(cfg *config.Config) (string, error) {
	dir := ""
	if cfg != nil && cfg.Templates != nil {
		dir = cfg.Templates.Dir
	}
	if dir == "" {
		paths, err := config.GetPaths()
		if err != nil {
			return "", err
		}
		return paths.TemplatesDir, nil
	}
	return config.ExpandPath(dir)
}

// buildTemplateRegistry returns the compiled-in starters merged with any
// user-authored CUE templates under the templates dir. A missing dir or a
// scan error degrades to just the built-ins (logged) — never fatal.
func buildTemplateRegistry() *templates.Registry {
	base := templates.Default()
	cfg, _ := config.Load() // best-effort; nil cfg falls back to the default dir
	dir, err := resolveTemplatesDir(cfg)
	if err != nil {
		log.Printf("  templates:   cannot resolve dir, serving built-ins only: %v", err)
		return base
	}
	user, err := templates.LoadFromDir(dir)
	if err != nil {
		log.Printf("  templates:   cannot scan %s, serving built-ins only: %v", dir, err)
		return base
	}
	if len(user) > 0 {
		log.Printf("  templates:   loaded %d user template(s) from %s", len(user), dir)
	}
	return base.With(user...)
}

// buildRegistry constructs the Provider registry from ~/.openctl/config.yaml.
// Registers the built-in proxmox/k3s providers, then loads any config entries
// that declare either a native external plugin `command:` (see internal/
// controller/providers/external) or a Terraform host `terraform:` block.
// Returns the registry, the registered provider names (for logging), and a
// cleanup func the caller must defer to reap spawned plugin processes on
// shutdown.
//
// If the config file is missing, the registry is left empty — the controller
// still starts; resource RPCs will return errors pointing the user at the
// missing config.
func buildRegistry(ctx context.Context, stateStore externalprovider.StateStore) (*providers.Registry, []string, func(), error) {
	registry := providers.NewRegistry()
	var names []string
	var clients []*pluginproto.Client
	var tfClients []*tfhostprovider.Client
	cleanup := func() {
		for _, c := range clients {
			// Best-effort reap; use a fresh short context since the root ctx
			// is already canceled by the time cleanup runs on shutdown.
			cc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = c.Close(cc)
			cancel()
		}
		for _, c := range tfClients {
			c.Close()
		}
	}

	cfg, err := config.Load()
	if err != nil {
		// Missing config file is acceptable — controller starts empty.
		return registry, nil, cleanup, nil
	}

	var pmp *pmprovider.Provider
	if _, ok := cfg.Providers["proxmox"]; ok {
		// Load every configured Proxmox context, not just the default, so a
		// single k3s cluster can spread its VMs across endpoints. Each VM's
		// spec.context selects its endpoint; the provider routes accordingly.
		ctxConfigs, defaultCtx, err := cfg.ProviderContextConfigs("proxmox")
		if err != nil {
			return nil, nil, cleanup, fmt.Errorf("load proxmox config: %w", err)
		}
		valid := make(map[string]*protocol.ProviderConfig, len(ctxConfigs))
		for name, pc := range ctxConfigs {
			if pc.Endpoint != "" {
				valid[name] = pc
			}
		}
		if len(valid) > 0 {
			pmp = pmprovider.NewMulti(valid, defaultCtx)
			registry.Register(pmp)
			names = append(names, "proxmox")
			if len(valid) > 1 {
				log.Printf("  provider \"proxmox\": %d endpoints configured (default context %q)", len(valid), defaultCtx)
			}
		}
	}

	// k3s provider needs a VM provider to drive child VM ops; today that's
	// always proxmox. If proxmox isn't configured, k3s isn't usable.
	if pmp != nil {
		registry.Register(k3sprovider.New(&protocol.ProviderConfig{}, pmp))
		names = append(names, "k3s")
	}

	// External providers: any provider entry with either a native plugin
	// `command:` or a Terraform host `terraform:` block whose name isn't
	// already registered (built-ins win). A provider that fails to load is
	// logged and skipped — one bad provider must not stop the controller.
	registered := map[string]bool{}
	for _, n := range names {
		registered[n] = true
	}
	for name, pc := range cfg.Providers {
		if registered[name] {
			continue
		}
		if pc.Command != "" && pc.Terraform != nil {
			log.Printf("  provider %q: both command and terraform configured; skipping ambiguous provider", name)
			continue
		}
		if pc.Terraform != nil {
			prov, client, err := loadTerraformProvider(ctx, name, pc, stateStore)
			if err != nil {
				log.Printf("  terraform provider %q: load failed, skipping: %v", name, err)
				continue
			}
			registry.Register(prov)
			tfClients = append(tfClients, client)
			registered[prov.Name()] = true
			names = append(names, prov.Name())
			log.Printf("  terraform provider %q: registered %d kind(s)", prov.Name(), len(prov.Kinds()))
			continue
		}
		if pc.Command == "" {
			continue
		}
		prov, hs, client, err := loadExternalProvider(ctx, cfg, name, pc, stateStore)
		if err != nil {
			log.Printf("  plugin %q: load failed, skipping: %v", name, err)
			continue
		}
		if prov.Name() != name {
			log.Printf("  plugin %q: handshake reported name %q; registering under %q",
				name, prov.Name(), prov.Name())
		}
		registry.Register(prov)
		clients = append(clients, client)
		registered[prov.Name()] = true
		names = append(names, prov.Name())

		// Register any plugin-supplied CUE schemas so external kinds validate
		// and surface through the SchemaService like built-in kinds.
		nSchemas := 0
		for _, k := range hs.Kinds {
			if k.Schema == "" {
				continue
			}
			schema.RegisterExternal(providerAPIVersion(prov.Name()), k.Kind, k.Schema)
			nSchemas++
		}
		if nSchemas > 0 {
			log.Printf("  plugin %q: registered %d schema(s)", prov.Name(), nSchemas)
		}
	}

	return registry, names, cleanup, nil
}

// providerAPIVersion returns the canonical apiVersion for a provider name,
// matching the `<name>.openctl.io/v1` convention the registry uses to route
// apiVersion → provider. External kinds are keyed by this apiVersion.
func providerAPIVersion(name string) string {
	return name + ".openctl.io/v1"
}

// loadExternalProvider spawns and handshakes one external plugin, passing the
// provider's default-context config (endpoint/token/defaults) as the opaque
// configure bag.
func loadExternalProvider(ctx context.Context, cfg *config.Config, name string, pc *config.Provider, stateStore externalprovider.StateStore) (providers.Provider, *pluginproto.HandshakeResult, *pluginproto.Client, error) {
	pcfg, err := cfg.GetProviderConfig(name, "")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve config: %w", err)
	}
	cmd := exec.CommandContext(ctx, pc.Command, pc.Args...) //nolint:gosec // G204: plugin command is operator-configured in config.yaml
	return externalprovider.Load(ctx, cmd, pcfg, stateStore)
}

func loadTerraformProvider(ctx context.Context, name string, pc *config.Provider, stateStore tfhostprovider.StateStore) (providers.Provider, *tfhostprovider.Client, error) {
	tf := pc.Terraform
	if tf == nil {
		return nil, nil, fmt.Errorf("terraform config is required")
	}
	if tf.Command == "" {
		return nil, nil, fmt.Errorf("terraform.command is required")
	}
	mappings := make([]tfhostprovider.ResourceMapping, 0, len(tf.Resources))
	for _, r := range tf.Resources {
		if r.Kind == "" || r.Type == "" {
			return nil, nil, fmt.Errorf("terraform resource mappings require kind and type")
		}
		mappings = append(mappings, tfhostprovider.ResourceMapping{
			Kind:     r.Kind,
			TypeName: r.Type,
		})
	}
	client, err := tfhostprovider.Launch(tf.Command, tf.Args...)
	if err != nil {
		return nil, nil, err
	}
	prov, err := tfhostprovider.NewProvider(ctx, name, client, stateStore, mappings, tfhostprovider.WithProviderConfig(tf.Config))
	if err != nil {
		client.Close()
		return nil, nil, err
	}
	return prov, client, nil
}
