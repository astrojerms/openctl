# Controller Implementation Plan

This document tracks the rollout of the openctl controller — the persistent
reconciler that replaces today's stateless CLI-with-state-files model. Mark
deliverables as `[x]` as they land. Architectural decisions are pinned at the
top so reviewers and future-self can trace any line of code back to the
choice that motivated it.

## Architectural decisions (locked)

These were settled via design walkthrough with the user before any code went
in. Any change requires re-opening the discussion.

### Resource semantics

- **Trigger model:** on-demand only. Reconciler runs on user apply or on user
  query. No background polling.
- **Drift handling:** passive. Observe and surface; never auto-mutate.
- **Observation:** use whatever the provider's read API returns. No
  gap-filling.
- **Comparison:** loose. Only manifest-specified fields are tracked.
  Provider-set defaults are unmanaged.
- **Apply on existing atomic resource (e.g. `VirtualMachine`):** no-op +
  surface drift. User must delete + re-apply for changes.
- **Apply on existing composite resource (`Cluster`):** structural changes
  honored declaratively (add nodes auto, remove nodes via
  `--allow-destructive`, catastrophic ops via
  `--i-know-this-breaks-the-cluster`). Spec changes to existing children also
  gated by `--allow-destructive`.
- **Drift vs. health are distinct.** `drift` = "the resource set/spec
  differs from the manifest" (structural). `health` = "services are or
  aren't running" (runtime).
- **Ownership:** owned children carry an `ownerRef` to their parent. The
  reconciler routes mutations through whoever owns the resource.
- **Delete:** async, idempotent (delete on missing = success), blocked when
  another resource owns this one (with a clear "this VM is owned by Cluster
  X" error), hard delete (no tombstoning — audit comes from the operation
  log).

### Operation model

- **Apply is async.** Submit returns an operation ID immediately; the
  reconciler dispatches off-thread. No long-held client connections.
- **Operations are persisted.** Survive controller restart. Powers DR + the
  future web UI's history view.
- **Parent-child operations.** A composite-resource apply (Cluster) spawns
  child ops (one per VM, plus k3s install + agent install). `get
  operation <id>` shows one level of children.
- **Success criteria:** end-to-end verification. Cluster apply is
  `succeeded` only when all VMs are running, k3s is responding, and agents
  are reachable. Reuse the existing Phase 3 reachability probe.
- **Concurrency:** fail-fast on same-resource collision. Second submission
  errors with a pointer to the in-flight op.
- **Retention:** GC on apply. No background sweep. State accumulates during
  inactivity, which is exactly when size doesn't matter.
- **Restart with in-flight ops:** mark `interrupted`, surface to user. No
  auto-resume. User re-applies; the declarative model converges from
  whatever partial state the previous run left.

### Architecture

- **Providers compiled into the controller binary** (option C from the
  design discussion). proxmox + k3s become Go packages implementing a
  shared `Provider` interface. Single artifact, type-safe, no dynamic
  discovery for first-party providers. Door is open for an exec-plugin shim
  later if 3rd-party providers matter.
- **Storage:** pure-Go SQLite (`modernc.org/sqlite`). Single file. No CGO
  dependency, so cross-compiles cleanly.
- **Schemas:** CUE, embedded in both client and server. Both sides
  validate; the controller never trusts the wire blindly.
- **Transport:** gRPC over HTTP/2 with TLS. k8s-shaped resources on the
  wire (`apiVersion`, `kind`, `metadata`, `spec`, `status`). Debug with
  `grpcurl`.
- **Auth:** API token (`Authorization: Bearer <token>`). `--no-auth` flag
  required for explicit localhost-only setups.
- **CLI is a thin gRPC client.** Always requires a controller. The existing
  exec-plugin model in the CLI is removed. The per-node
  `openctl-k3s-agent` is unaffected — different boundary, different threat
  model.
- **State portability:** backup is `cp state.db state.db.bak`. Restore is
  `cp` back. The controller is stateless code on top of a stateful file.

### Out of scope for v1 (revisit later)

- Auto-resume of interrupted operations.
- Tombstoning / soft-delete with retention.
- Background drift detection.
- External plugin protocol (3rd-party providers).
- Multi-tenant auth (OIDC, RBAC).
- Web UI (informs API design but isn't a v1 deliverable).

---

## Phases

### Phase 1: Controller skeleton + auth + minimal CLI client

**Status:** complete

**Goals:** prove the end-to-end wire works. Controller binary serves gRPC
behind TLS + token auth; CLI binary authenticates and hits a trivial RPC.
No providers, no operations, no persistence beyond auth material.

**Deliverables:**

- [x] Add deps: `google.golang.org/grpc`, `google.golang.org/protobuf`,
      `modernc.org/sqlite`.
- [x] `pkg/api/v1/api.proto` — Ping service definition (foundation for
      later additions).
- [x] Generated Go bindings committed; `make generate` regenerates.
- [x] `internal/controller/storage` — SQLite open + schema migration
      runner.
- [x] `internal/controller/auth` — token generation, bearer-token
      middleware.
- [x] `internal/controller/tls` — self-signed cert generation on first
      start.
- [x] `internal/controller/server` — gRPC server with TLS + auth +
      `Ping` handler.
- [x] `cmd/openctl-controller/main.go` — entry point: `serve` subcommand.
- [x] `internal/cli` — `openctl ping` command using the gRPC client.
- [x] Config: extend `~/.openctl/config.yaml` with `controller:` section
      (`url`, `tokenFile`, `caFile`).
- [x] Tests: unit tests for token gen, TLS cert gen, auth middleware;
      integration test that runs the controller in-process and Pings it
      from a real client.
- [x] Lint clean on new code.
- [x] `make build` and `make test` both pass.

**Verifiable:** all four checks confirmed during smoke test —
1. `openctl-controller serve` starts, prints token path + listen address.
2. `openctl ping` round-trips: `ok: echo="ping" server-version=0.1.0-controller`.
3. `grpcurl` without bearer header → `Unauthenticated: missing authorization header`.
4. Token + CA + server cert persist across restart; second `serve` reuses
   the existing material with no regeneration.

---

### Phase 2: First provider compiled in (proxmox VirtualMachine)

**Status:** pending

**Goals:** prove the provider-as-Go-interface model works. Synchronous
apply for atomic VMs; CUE schema validation on both ends.

**Deliverables:**

- [ ] Define `Provider` Go interface (Apply, Get, List, Delete).
- [ ] `internal/controller/providers/proxmox` — proxmox provider as a
      Go package, reusing existing client from `plugins/proxmox/internal/client`.
- [ ] `pkg/api/v1` — `ResourceService` proto with Apply/Get/List/Delete RPCs.
- [ ] CUE schema for VirtualMachine, embedded in both controller and CLI;
      both validate.
- [ ] Controller routes RPCs to the right provider based on
      `apiVersion`/`kind`.
- [ ] CLI: `openctl proxmox apply -f vm.yaml`, `openctl proxmox get vms`,
      `openctl proxmox delete vms <name>`.
- [ ] Tests: mock proxmox API; verify apply round-trips; CUE validation
      catches a bad manifest before submission.

**Verifiable:** `openctl proxmox apply -f preflight-vm.yaml` creates a real
VM via the controller; `openctl proxmox get vms` lists it from SQLite-backed
state.

---

### Phase 3: Async operations + persistence

**Status:** pending

**Goals:** the operation model goes live. Apply returns an operation ID;
reconciler dispatches in the background; ops survive restart.

**Deliverables:**

- [ ] SQLite schema for `operations` and `operation_steps` tables.
- [ ] `OperationService` proto: `GetOperation`, `ListOperations`.
- [ ] Reconciler dispatcher: pulls pending ops from DB, runs them, updates
      status.
- [ ] Apply returns operation ID immediately (no longer blocks).
- [ ] Concurrency lock: fail-fast on same-resource in-flight op.
- [ ] GC on apply: prune ops older than N or beyond per-resource limit.
- [ ] Restart handler: marks `running` ops as `interrupted`.
- [ ] Tests: long-running op doesn't block client; concurrent apply on
      same resource fails fast; restart mid-op marks as interrupted; GC
      removes old ops.

**Verifiable:** submit an apply, get back an op ID; poll `openctl get
operation <id>` until done; kill the controller mid-op, restart, see the
op marked `interrupted`.

---

### Phase 4: Cluster provider

**Status:** pending

**Goals:** k3s plugin moves from exec'd binary to compiled-in provider.
Composite operations work end-to-end. The agent build/install machinery
stays unchanged — it's called from in-process Go now instead of via exec.

**Deliverables:**

- [ ] `internal/controller/providers/k3s` — k3s provider as a Go package,
      reusing existing `plugins/k3s/internal/{cluster,agent}` packages.
- [ ] Parent-child operations: Cluster apply spawns child VM ops + k3s
      install step + agent install step.
- [ ] Ownership: child VMs carry `ownerRef` to their parent Cluster.
- [ ] Delete on owned VM: blocked with clear error.
- [ ] CLI: `openctl k3s apply -f cluster.yaml` works end-to-end.
- [ ] Tests: end-to-end Cluster apply using mocked proxmox; ownership
      block-on-delete enforced.

**Verifiable:** `openctl k3s apply -f cluster.yaml` against a real Proxmox
creates the cluster; both VMs Ready, agents reachable, kubectl works.

---

### Phase 5: Declarative reconciliation + drift surfacing

**Status:** pending

**Goals:** the reconciler graduates from "create the missing things" to
"converge to spec." Drift detection on read; structural changes honored;
destructive guardrails enforced.

**Deliverables:**

- [ ] Loose comparison + drift detection on `Get` for VirtualMachine.
- [ ] Drift detection on `Get` for Cluster (structural — children diff
      against manifest).
- [ ] Cluster apply: add new children on count-up.
- [ ] Cluster apply: remove children on count-down with
      `--allow-destructive`.
- [ ] Cluster apply: spec changes to existing children with
      `--allow-destructive`.
- [ ] Catastrophic-op detection (single-CP recreate, quorum loss, last
      worker removal) requires `--i-know-this-breaks-the-cluster`.
- [ ] Tests: apply with new manifest detects drift; flagged apply
      converges; catastrophic op blocked without flag.

**Verifiable:** change `workers.count` and re-apply: get-cluster shows
drift; apply with `--allow-destructive` removes the extra worker. Try to
recreate the only CP without `--i-know-this-breaks-the-cluster`: blocked.

---

### Phase 6: Mac launchd install

**Status:** pending

**Goals:** zero-friction local install. Fresh Mac → one command → openctl
works.

**Deliverables:**

- [ ] `openctl-controller install --local` command.
- [ ] Generates launchd plist at `~/Library/LaunchAgents/io.openctl.controller.plist`.
- [ ] Places binary at standard location.
- [ ] Calls `launchctl load`; verifies controller responds.
- [ ] Writes initial CLI config to `~/.openctl/config.yaml` (controller URL
      + token path + CA path).
- [ ] Prints next-steps including how to uninstall.
- [ ] `openctl-controller uninstall` removes plist + binary (leaves state
      and config alone unless `--purge`).
- [ ] Documentation: README/QUICKSTART updates pointing at the install
      command.

**Verifiable:** fresh test directory, run `openctl-controller install
--local`, then `openctl proxmox get vms` works without further setup.

---

### Followup work (post-Phase-6)

Tracked here so they don't get lost; not blocking the core rollout.

- **External plugin protocol** (option A from the design): allow 3rd-party
  providers to ship as separate exec'd binaries the controller talks to over
  the existing JSON-over-stdio plugin protocol. Wraps to the same `Provider`
  Go interface internally.
- **Linux install via SSH** (`openctl-controller install --target
  ssh://user@host`): reuses the agent's bootstrap pattern.
- **Proxmox bootstrap install** (`openctl-controller install --target
  proxmox://homelab`): composes "create VM" + "install on Linux."
- **Web UI** — separate frontend repo or subdir; consumes the controller's
  gRPC API (via gRPC-Gateway or gRPC-Web).
- **Plugin-defined CLI subcommands** (deferred from agent work, see
  `DESIGN.md`): becomes more relevant once the controller is in place.
- **Default-timeout problem** (also deferred from agent work): controller
  changes the operational shape — long applies don't need long client
  timeouts because submit returns immediately.
