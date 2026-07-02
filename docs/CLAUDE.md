# CLAUDE.md

Context for Claude Code sessions on openctl. Kept short and current;
the source of truth for what to work on is `ROADMAP.md`.

## What openctl is

A homelab infrastructure controller. Present-day: a persistent
background daemon (`openctl-controller`) that owns a SQLite state DB,
compiles in-process providers for Proxmox and k3s, exposes a gRPC +
grpc-gateway REST API, and serves a Svelte web UI at
`https://127.0.0.1:9445/ui/` (HTTP/2 over TLS) behind a session-cookie auth layer.
Users author resources via the UI (typed form + Monaco YAML editor)
or the CLI (`openctl ctl apply -f manifest.yaml`) and the controller
routes applies through the in-process providers.

Pitch: kubectl-shape UX + Terraform-shape declarative model +
Kubernetes-shape schemas, scoped to what a home lab actually needs.
"AWS console for homelab" is the design target.

## Architecture snapshot

```
openctl/
├── cmd/
│   ├── openctl/                # CLI entry point (talks to controller via gRPC)
│   └── openctl-controller/     # The daemon: serve, install, uninstall, version
├── internal/
│   ├── controller/             # Controller-side code
│   │   ├── providers/          # In-process providers (proxmox/, k3s/)
│   │   ├── operations/         # Async op store + dispatcher
│   │   ├── manifests/          # Applied-manifests store (SQLite + disk mirror + git)
│   │   ├── reconciler/         # Periodic drift check (U8.3)
│   │   ├── server/             # gRPC + HTTP gateway + embedded UI assets
│   │   ├── auth/               # Token + session
│   │   ├── storage/            # SQLite bootstrap
│   │   └── tls/                # Self-signed material for localhost gRPC
│   ├── schema/                 # CUE embed + validate + form-schema walker
│   ├── config/                 # ~/.openctl/config.yaml types
│   ├── cli/                    # CLI subcommands
│   └── manifest/, output/, log/, errors/  # Small utilities
├── pkg/
│   ├── api/v1/                 # Proto + generated gRPC + gateway
│   ├── protocol/               # Shared plugin JSON-over-stdio types (legacy path)
│   ├── proxmox/                # Proxmox client + handler + resource converters
│   └── k3s/                    # k3s client + handler + resource converters + agent
├── plugins/
│   ├── proxmox/                # Legacy exec-plugin binary (superseded by in-process)
│   └── k3s/                    # Legacy exec-plugin + k3s-agent
├── ui/                         # Vite + Svelte 5 + TypeScript
│   └── src/{routes,components,lib}/
├── docs/
│   ├── CLAUDE.md               # (this file)
│   └── target-architecture.html # Long-form BSALC/Crossplane/BuildKit design doc
├── ROADMAP.md                  # Single index of tracked work — start here
├── CONTROLLER.md               # Controller-specific phased plan
├── UI.md                       # UI-specific phased plan (U1–U8)
├── DEVELOPMENT.md              # Toolchain setup
├── QUICKSTART.md               # User-facing getting started
├── DESIGN.md                   # Older plugin-protocol design (legacy path)
└── README.md
```

## Two paths, one goal

The codebase has **two** ways to reach a Proxmox VM or k3s cluster:

1. **Controller path** (current, preferred). The controller compiles the
   proxmox and k3s packages in-process. Users hit `openctl ctl apply`
   or use the UI; the controller dispatches to the in-process provider.
   State persists in SQLite. Drift is tracked. UI works.
2. **Exec-plugin path** (legacy, still shipped). `openctl proxmox
   apply` spawns `openctl-proxmox`, speaks JSON over stdio. No
   controller state involvement. Kept for scripting.

Both paths call into the same `pkg/proxmox/handler.Handler` (and the
k3s equivalent), so the underlying behavior is shared. Provider logic
should live under `pkg/<provider>/`; only the wrapper adapters differ.

## Where things live

- **CUE schemas:** `internal/schema/schemas/{base,proxmox,k3s}/*.cue`
  — embedded via `//go:embed all:schemas`. Adding a new resource kind
  requires the `.cue` file AND an entry in `internal/schema/embed.go`
  (`Registry()`) AND `internal/schema/validate.go` (`SchemaSelector`).
- **Form schema:** `internal/schema/form/` walks CUE → typed `Field`
  tree → served as JSON to the UI via `SchemaService.GetFormSchema`.
- **UI assets:** `internal/controller/server/uiassets/dist/` — built
  by `make ui` (which runs `npm run build`), embedded by the
  controller. `make build` now depends on `make ui` so the deployed
  UI is always fresh.
- **Configuration:** `~/.openctl/config.yaml` — providers, manifests
  sink, reconciler interval, git tracking. Loaded by
  `internal/config/config.go`.
- **State:** `~/.openctl/controller/state.db` (SQLite),
  `~/.openctl/manifests/` (disk mirror, optional),
  `~/.openctl/k3s/<cluster>/kubeconfig`.
- **k3s agent:** `pkg/k3s/agent/` — cross-compiled to
  `openctl-k3s-agent-linux-{amd64,arm64,armv7}` and installed on each
  node during cluster create.

## How to build, test, run

```bash
# Everything (UI + all Go binaries)
make build

# UI only
make ui

# Go-only iteration (skip UI rebuild)
go build ./cmd/openctl-controller

# Full test suite
go test ./...

# UI tests
cd ui && npm test -- --run

# TypeScript + Svelte check
cd ui && npm run check

# Regenerate gRPC + gateway bindings after editing pkg/api/v1/api.proto
make generate
```

**Reload the running controller after a code change:**

```bash
make build && ./bin/openctl-controller install --local
```

Do NOT use `launchctl unload/load` for a rebuild reload — the plist
points at `~/Library/Application Support/openctl/bin/openctl-controller`,
NOT `./bin/openctl-controller`, so `launchctl load` reruns the OLD
installed binary. `install --local` copies the fresh binary into place
and reloads.

For the CLI directly:

```bash
./bin/openctl-controller serve --dir /tmp/scratch --no-auth   # Foreground
./bin/openctl ping                                            # Sanity check
./bin/openctl ctl get vm                                      # List VMs
./bin/openctl ctl apply -f vm.yaml                            # Apply a manifest
./bin/openctl validate -f vm.yaml                             # CUE-validate only
```

## Common tasks

### Add a new resource kind

1. Write the CUE schema in `internal/schema/schemas/<provider>/<kind>.cue`.
   Use `@options(kind="X")` for dropdown references,
   `@oneOf(group="X")` for mutually-exclusive fields.
2. Register in `internal/schema/embed.go` (`Registry()`) and
   `internal/schema/validate.go` (`SchemaSelector`).
3. Extend the provider under `internal/controller/providers/<provider>/`:
   handle the new kind in `Kinds()`, `Apply`, `Get`, `List`, `Delete`.
   Optional capabilities: `ObservedOnlyKinds`, `Actioner`,
   `ChildrenLister`, `OwnershipChecker`, `DryRunner`.
4. If the kind needs custom behavior (start/stop, kubeconfig download,
   etc.), plumb via the appropriate optional interface.
5. Add unit tests.

### Change the wire protocol

Edit `pkg/api/v1/api.proto`, run `make generate`, rebuild. The
gateway rewires REST paths from proto annotations automatically.

### UI changes

1. Edit under `ui/src/`. `npm run dev` from the `ui/` dir gives a
   live-reload dev server against a running controller (see
   `ui/vite.config.ts` for proxy config).
2. When done: `make build && ./bin/openctl-controller install --local`
   to bake the new bundle into the controller and reload.
3. The build pill in the UI header shows the running git SHA — use
   this to confirm your change is actually deployed before debugging
   further.

### Investigate a bug

- Check `ROADMAP.md`'s "Recently completed" section — recent merges
  often explain surprising behavior.
- The op history in the UI drawer (bottom of every page) shows every
  Apply/Delete with timestamps + errors.
- Controller logs: `~/Library/Logs/openctl-controller.log` (macOS
  LaunchAgent) or foreground stderr for `openctl-controller serve`.
- SQLite state: `sqlite3 ~/.openctl/controller/state.db`.

## Conventions

- **Commits:** short imperative subject line under 72 chars; body
  wraps at ~72 explaining the *why*. Recent examples in `git log`
  are the style guide.
- **Branch names:** `feat/<slug>`, `fix/<slug>`, `docs/<slug>`.
  PRs merged with `--no-ff` for a clean history.
- **ROADMAP.md** gets updated on every ship — "Recently completed"
  section carries the last ~10 merges by hash; phase sections mark
  items shipped as they land.
- **Go style:** Google style guide + `make fmt` + `make lint`.
- **Tests:** unit tests colocated with source; integration in
  `test/e2e/`. Regression tests when fixing bugs.
- **Comments:** the *why*, not the *what*. Field-level doc comments
  on CUE schemas surface as field descriptions in the UI form —
  worth investing in.
- **No secrets in commits:** use `tokenSecretFile` in config, never
  inline `tokenSecret`.

## Notable current state (as of Phase U8 complete)

- **UI is genuinely usable for authoring, editing, deleting, and
  operating on VMs and Clusters.** Form editor with typed fields,
  discriminated-union pickers, provider-populated dropdowns
  (ProxmoxNode names), inline validation errors, per-resource
  action buttons (start/stop/reboot), live op progress, YAML
  copy/download, name suggestions, sort/filter on lists.
- **Version pill** in the UI header shows the running controller's
  git SHA. `openctl-controller version` prints it too. Ping response
  carries it.
- **`make build` rebuilds the UI** as a prerequisite. Skip via
  `go build ./cmd/openctl-controller` for Go-only iteration.
- **Shutdown is graceful.** SIGINT/SIGTERM cancels the root ctx and
  falls back to force-stop after 3s if Watch streams hang around.
- **Periodic drift reconciler** re-checks every managed resource on
  a configurable interval (default 5m). Logs drift transitions.
  Does NOT auto-remediate — users click Reconcile in the UI or CLI.
