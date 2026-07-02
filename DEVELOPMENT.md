# Development Guide

Conventions, dependencies, and recipes for working on openctl. For the
project's high-level architecture see [DESIGN.md](DESIGN.md). For a quick
end-user walkthrough see [QUICKSTART.md](QUICKSTART.md). For the controller
rollout plan see [CONTROLLER.md](CONTROLLER.md).

## Prerequisites

Required for any build:

- **Go 1.25+** (we use language features that landed in 1.25).
- **make** (the entrypoint for everything).

Required to regenerate gRPC bindings (only when `.proto` files change):

- **protoc** — `brew install protobuf` on macOS, `apt install protobuf-compiler` on Debian/Ubuntu.
- **protoc-gen-go** — `go install google.golang.org/protobuf/cmd/protoc-gen-go@latest`.
- **protoc-gen-go-grpc** — `go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest`.

Optional but useful:

- **grpcurl** — `brew install grpcurl`. Lets you call controller RPCs by
  hand for debugging.
- **golangci-lint** — `brew install golangci-lint`. Run locally before
  pushing; matches CI.

## Repo layout

```
cmd/                            # CLI / binary entry points
  openctl/                      # the user-facing CLI
  openctl-controller/           # the controller daemon (Phase 1+)
internal/                       # private code
  cli/                          # CLI command implementations
  controller/                   # controller internals
    server/                     # gRPC server, RPC handlers
    storage/                    # SQLite, schema migrations
    auth/                       # token + bearer middleware
    tls/                        # self-signed cert generation
    providers/                  # in-process Provider implementations
      proxmox/                  # (Phase 2)
      k3s/                      # (Phase 4)
  plugin/                       # exec-plugin executor (legacy, removed in Phase 6)
  state/                        # legacy file-based state (removed in Phase 6)
pkg/                            # public-ish types
  api/v1/                       # gRPC proto + generated Go bindings
  protocol/                     # legacy exec-plugin protocol (removed in Phase 6)
  compute/                      # compute provider interfaces
plugins/                        # exec'd provider binaries (legacy, removed in Phase 6)
  proxmox/
  k3s/
test/e2e/                       # end-to-end tests against mock plugins
examples/                       # sample manifests
```

The `plugins/`, `internal/plugin/`, and `internal/state/` trees are the
existing stateless-CLI architecture. They get removed at the end of the
controller rollout (see CONTROLLER.md Phase 6) once the controller fully
replaces them.

## Common commands

```sh
make build                  # build CLI + plugins + controller
make test                   # full test suite (root + each plugin module)
make lint                   # run golangci-lint everywhere
make install                # build + install to ~/.openctl/plugins/ and $GOBIN
make generate               # regenerate gRPC Go bindings from .proto
make clean                  # remove bin/
```

Plugin-specific:

```sh
make build-plugin-k3s             # build the k3s exec plugin (legacy)
make build-plugin-k3s-agent       # native build of the per-node agent
make build-plugin-k3s-agent-linux # cross-compile agent for amd64/arm64/armv7
```

## Development workflow

1. **Branch off main.** Use prefixes that match the work: `feat/`, `fix/`, `chore/`, `refactor/`.
2. **Run tests locally before commit.** `make test` covers root + both
   plugin modules. CI runs the same.
3. **Run lint.** `make lint`. New code should be lint-clean even when the
   surrounding file has pre-existing issues — fix only what you touched
   unless you intend to clean up the whole file.
4. **Commits use Conventional Commits.** `feat(scope):`, `fix(scope):`,
   `chore:`, `docs:`, `refactor(scope):`. Multi-paragraph body explaining
   the why is encouraged for non-trivial changes.
5. **Don't push to main directly.** Use a feature branch; merge via PR.

## Working on the controller

The controller lives in `internal/controller/` and `cmd/openctl-controller/`.
For local development:

```sh
# Build and run the controller in the foreground.
make build
./bin/openctl-controller serve

# In another terminal, hit it with the CLI.
./bin/openctl ping
```

State on local dev defaults to `~/.openctl/controller/`:

- `state.db` — SQLite database
- `tls.crt` / `tls.key` / `ca.crt` — self-signed TLS material generated on first start
- `token` — initial API token, written on first start

To start fresh, `rm -rf ~/.openctl/controller/`. The next `serve` will
re-bootstrap.

### macOS code signing (per-app firewalls)

If you run a per-app outbound firewall (LuLu, Little Snitch), you may hit
a baffling failure: the controller can't reach Proxmox — `connect: no
route to host` to your PVE host — even though `curl` from the same shell
works fine. The cause isn't the network. `go build` produces an ad-hoc
Mach-O whose cdhash changes on **every** build, and these firewalls
identify unsigned apps by that cdhash. So each `make build` looks like a
brand-new app: the rule you approved yesterday no longer matches, the
firewall silently blocks the new binary, and openctl sees an instant
`EHOSTUNREACH` (which prints as "no route to host").

The fix is to sign every build with one persistent self-signed identity.
Run the one-time setup, then rebuild:

```sh
make codesign-setup     # creates the "openctl-dev" identity in your login keychain
make build              # every build is now signed with it
```

`make build` signs the CLI, controller, and native plugins automatically
(the k3s *agent* Linux binaries are ELF, not Mach-O, so they're skipped —
they run on remote nodes, not this Mac). Because codesign's designated
requirement for a self-signed leaf is `identifier "…" and certificate
leaf = H"<cert hash>"` — no cdhash — every rebuild satisfies the identical
requirement, so a firewall rule you approve **once** keeps applying across
rebuilds. Confirm with:

```sh
codesign -d --requirements - bin/openctl-controller   # stable across rebuilds
```

Signing is a no-op off macOS and for anyone who hasn't run the setup
(CI and other contributors build exactly as before). The cert is
untrusted-but-stable on purpose: the firewall wants a consistent identity,
not an Apple-issued one. To undo: `security delete-identity -c openctl-dev
~/Library/Keychains/login.keychain-db`. Details in
`scripts/macos-codesign-setup.sh`.

### HTTPS gateway (HTTP/2)

The HTTP gateway + UI is served over **HTTPS** on `127.0.0.1:9445`
(`https://127.0.0.1:9445/ui/`). It's TLS specifically to get HTTP/2:
browsers only negotiate h2 over TLS, and h2 multiplexes ~100 streams over
one connection. That's what removed the old "Loading..." hang — under
HTTP/1.1 (~6 connections per origin) a handful of long-lived Watch streams
would exhaust the pool and starve ordinary page fetches. The gateway
reuses the same self-signed cert/key the gRPC server uses (SANs:
`localhost`, `127.0.0.1`, `::1`), signed by the controller's own CA.

Because that CA isn't in your trust store, the browser shows a one-time
"Your connection is not private" interstitial — click through
(Advanced → Proceed). This does **not** downgrade the protocol; the h2
connection is still established. To silence the warning, trust the CA:

```sh
# macOS (login keychain; prompts for your password once):
security add-trusted-cert -r trustRoot \
  -k ~/Library/Keychains/login.keychain-db \
  ~/.openctl/controller/tls/ca.crt
```

For `curl` against the gateway, use `-k` (skip verification) or
`--cacert ~/.openctl/controller/tls/ca.crt`. The **CLI** talks gRPC
directly on `:9444` (not the gateway) and already trusts this CA via
`openctl` config, so it's unaffected.

### Modifying the gRPC API

The wire contract lives in `pkg/api/v1/api.proto`. After editing:

```sh
make generate
```

That regenerates the Go bindings under `pkg/api/v1/`. Commit the regenerated
files alongside the proto change so anyone can build without protoc
installed.

### Hand-debugging RPCs

```sh
# List services (server reflection is enabled in dev builds)
grpcurl -insecure -H "Authorization: Bearer $(cat ~/.openctl/controller/token)" \
  localhost:9444 list

# Call Ping
grpcurl -insecure -H "Authorization: Bearer $(cat ~/.openctl/controller/token)" \
  localhost:9444 openctl.v1.PingService/Ping
```

`-insecure` skips TLS verification — fine for local-Mac dev where the cert
is self-signed and the CA isn't in your system trust store. Production
clients should use `-cacert ~/.openctl/controller/ca.crt`.

## Working on providers

Providers are Go packages implementing the `Provider` interface (Phase 2+).
They live in `internal/controller/providers/<name>/`.

For now (during the controller rollout), providers also exist as exec'd
binaries under `plugins/<name>/`. This is the architecture being phased out;
see CONTROLLER.md.

## Working on the per-node k3s agent

Lives at `plugins/k3s/cmd/openctl-k3s-agent/` (binary) and
`plugins/k3s/internal/agent/` (library). Independent of the controller —
gets installed on cluster nodes via SSH bootstrap during cluster create.

To rebuild and push to a running cluster's nodes for testing:

```sh
make build-plugin-k3s-agent-linux
DIR=~/.openctl/state/k3s/<cluster>
for ip in $(yq '.status.outputs.agent.endpoints[]' ~/.openctl/state/k3s/<cluster>.yaml); do
  scp bin/openctl-k3s-agent-linux-amd64 ubuntu@$ip:/tmp/agent
  ssh ubuntu@$ip "sudo install -m 0755 /tmp/agent /usr/local/bin/openctl-k3s-agent && \
                  sudo systemctl restart openctl-k3s-agent"
done
```

For details on the agent's architecture (mTLS model, OS heterogeneity,
endpoint surface), see [DESIGN.md](DESIGN.md) "K3s Plugin Agent".

## Testing conventions

- **Unit tests** live next to the code (`*_test.go` in the same package).
- **Integration tests** that need real services or wire-level setup
  (mTLS handshakes, gRPC clients against real servers) live in the same
  package, conventionally with `Integration` in the test name.
- **End-to-end tests** for the CLI live in `test/e2e/`. They use mock
  plugins via the test harness; no real Proxmox needed.

Tests should not require network access by default. Anything that touches a
real provider (real Proxmox, real network) is gated by an env var
(`OPENCTL_E2E_REAL=1` or similar) so CI doesn't surprise itself.

## Common pitfalls

- **Cross-compilation and CGO.** We cross-compile agent binaries to three
  Linux architectures. Anything depending on CGO breaks this. Pure-Go
  alternatives matter — e.g., we use `modernc.org/sqlite` instead of
  `mattn/go-sqlite3` for exactly this reason.
- **Go module versions.** Three modules in this repo (root + two plugins)
  must agree on the Go version directive. The chore commit `chore: bump
  plugin go.mod to go 1.25.0` exists because they drifted.
- **Plugin discovery.** The CLI scans `~/.openctl/plugins/` for
  `openctl-*` files. Anything matching that pattern in there gets
  registered as a provider. The agent binaries live in a `k3s-agents/`
  subdir specifically to avoid this pollution.
