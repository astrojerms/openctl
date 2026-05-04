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

**Status:** complete

**Goals:** prove the provider-as-Go-interface model works. Synchronous
apply for atomic VMs; CUE schema validation on both ends.

**Deliverables:**

- [x] Define `Provider` Go interface (Apply, Get, List, Delete) +
      `Registry` for apiVersion → Provider routing.
- [x] Refactor: `plugins/proxmox/internal/{client,resources,handler}` →
      `pkg/proxmox/{client,resources,handler}` so both the legacy
      exec'd plugin and the controller's in-process provider use the
      same code (deduped before Phase 6 rather than after).
- [x] `internal/controller/providers/proxmox` — proxmox provider as a
      Go package implementing the Provider interface, adapting to
      `pkg/proxmox/handler`.
- [x] `pkg/api/v1` — `ResourceService` proto with Apply/Get/List/Delete
      RPCs using `google.protobuf.Struct` for spec/status.
- [x] CUE schema for VirtualMachine — already embedded under
      `internal/schema/schemas/proxmox/vm.cue`. Added
      `schema.Validate(*protocol.Resource)` shared by CLI and controller.
- [x] Controller routes RPCs to the right provider based on
      `apiVersion`/`kind` via the registry.
- [x] CLI: `openctl ctl apply -f`, `openctl ctl get <kind>`,
      `openctl ctl delete <kind> <name>` — all routed through the
      controller. Coexists with the legacy exec-plugin commands until
      Phase 6 cleanup.
- [x] Controller startup loads `~/.openctl/config.yaml` and
      auto-registers the proxmox provider from the default context.
- [x] Tests: provider unit tests (mock proxmox API for List/Get/Delete);
      registry routing tests; resource-service integration tests
      (in-process server with fake provider over real gRPC + TLS); CUE
      validation positive + negative tests.

**Verifiable:** `openctl ctl apply --file vm.yaml` against a running
controller submits a VirtualMachine. The controller validates against the
embedded CUE schema, routes to the proxmox provider, calls Proxmox via the
shared `pkg/proxmox/handler`, and returns the created resource.
`openctl ctl get VirtualMachine --api-version proxmox.openctl.io/v1` lists
VMs through the same path. End-to-end smoke against real Proxmox is left
for the user since it creates real infrastructure.

---

### Phase 3: Async operations + persistence

**Status:** complete

**Goals:** the operation model goes live. Apply returns an operation ID;
reconciler dispatches in the background; ops survive restart.

**Deliverables:**

- [x] SQLite schema for `operations` table (parent_id reserved for Phase 4
      child ops). Indexes for the per-resource lock query and the
      dispatcher's pending-poll query.
- [x] `OperationService` proto: `GetOperation`, `ListOperations`.
      `ApplyResponse`/`DeleteResponse` extended with `operation_id`.
- [x] `internal/controller/operations` package: Store data layer (Submit,
      Get, List, ClaimNextPending, Complete, MarkRunningInterrupted, GC)
      and Dispatcher goroutine (poll + drain + execute via Provider).
- [x] Apply/Delete RPCs enqueue an op and return `operation_id` immediately
      instead of blocking on the provider call.
- [x] Concurrency lock: Submit fails fast with `ConflictError` if another
      op for the same (apiVersion, kind, name) is pending or running. The
      gRPC layer maps this to `codes.AlreadyExists` with the in-flight
      op ID in the message.
- [x] GC on apply (and on Complete) — keeps the most recent N completed
      ops per resource. Default retention 50; configurable later.
- [x] Restart handler: on controller startup, all `running` ops are
      rewritten as `interrupted` with a marker error message. No
      auto-resume — user re-applies and the declarative model converges.
- [x] CLI: `openctl ctl apply` polls for op completion by default
      (`--no-wait` to fire-and-forget; `--wait-timeout` configurable).
      New `openctl ctl op get <id>` and `openctl ctl op list` commands
      with status/apiVersion/kind/name filters.
- [x] SQLite tuning: pure-Go modernc.org/sqlite opened with
      `busy_timeout=5000`, `journal_mode=WAL`, `foreign_keys=on` so the
      dispatcher and the gRPC handlers can share writes without contention.
- [x] Tests: data-layer (insert + lookup, fail-fast conflict, no-conflict
      across different resources, claim marks running, complete writes
      terminal status, MarkRunningInterrupted rewrites all running, GC
      keeps last N, list filters); dispatcher (apply + delete process,
      provider error → failed op, missing provider → failed op, Stop
      blocks until done); gRPC integration (apply enqueues + dispatches
      to terminal status, conflict returns AlreadyExists, delete enqueues
      + dispatches).

**Verifiable:** confirmed via integration tests — submit an apply, get back
an op ID; OperationService returns the op; concurrent submission for the
same resource gets `AlreadyExists` with the in-flight op ID; provider
errors propagate to op.error.

---

### Phase 4: Cluster provider

**Status:** complete (parent-child ops deferred to Phase 4.5)

**Goals:** k3s plugin moves from exec'd binary to compiled-in provider.
Composite operations work end-to-end. The agent build/install machinery
stays unchanged — it's called from in-process Go now instead of via exec.

**Deliverables:**

- [x] Refactor: `plugins/k3s/internal/{cluster,agent,resources,handler,ssh}` →
      `pkg/k3s/{cluster,agent,resources,handler,ssh}` so both the legacy
      exec'd plugin and the controller's in-process provider share the
      same code (mirror of the proxmox refactor in Phase 2).
- [x] `internal/controller/providers/k3s` — k3s provider as a Go package,
      depending on a narrow `VMApplier` interface so the in-process
      proxmox provider satisfies the child-VM dependency naturally.
- [x] Cluster apply runs as a single op in the dispatcher (synchronous
      step sequence: child VM applies via the in-process VM provider,
      then `cluster.InstallK3s` for k3s + agent install).
- [x] Ownership: cluster state file lists `children: [{provider, kind,
      name}]`; the k3s provider implements `OwnershipChecker` so the
      registry's `OwnerOf` walk finds the owning cluster on a VM delete
      attempt.
- [x] Delete on owned VM: blocked with clear error
      (`FailedPrecondition: VirtualMachine "x" is owned by Cluster "y";
      delete the owner instead`). Cluster delete cascades to child VMs.
- [x] CLI: `openctl ctl apply -f cluster.yaml` routes through the
      controller and dispatches to the in-process k3s provider. (Legacy
      `openctl k3s apply` exec-plugin command coexists until Phase 6
      cleanup.)
- [x] Controller startup auto-registers the k3s provider when proxmox
      is configured (k3s needs a VM provider to drive child VMs).
- [x] Tests: provider unit tests (OwnerOf hits/misses, Get returns
      NotFound vs. existing state, Delete cascades to fake VM provider,
      Delete on missing is idempotent); resource-handler integration
      test for the FailedPrecondition path on owned-resource delete.
- [ ] Phase 4.5 followup: split cluster apply into parent + child ops
      (one row per VM apply + one for k3s install + one for agent
      install). Today everything runs as one op; surfacing the steps
      individually needs the operations layer to model parent_id +
      child progress aggregation.
- [ ] Phase 4.5 followup: QGA-based IP discovery in the controller
      path (current path requires `spec.network.staticIPs.{startIP,
      gateway,netmask}`).

**Verifiable:** confirmed via tests — `TestDeleteCascadesToChildVMs`
exercises cluster-delete → child VM cascade, `TestOwnerOfFindsClusterChild`
proves the OwnerOf walk, and `TestResourceServiceDeleteBlockedWhenOwned`
proves the gRPC-level FailedPrecondition path. End-to-end smoke against
real Proxmox is left for the user since it creates real infrastructure.

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
