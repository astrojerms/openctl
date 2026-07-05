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
- **Child ordering is a dependency DAG.** Within a single composite apply, the
  children are ordered by a real dependency graph (`operations.RunGraph`:
  topological execution + cycle detection), not hand-coded phase loops. Edges
  are derived from `$ref`s between children (`RefChildEdges`) plus explicit
  barrier edges for non-ref constraints (e.g. every AgentInstall waits on the
  CA bundle, which aggregates all K3sNode states). Serial by default (preserves
  SSH-install semantics); `OPENCTL_APPLY_CONCURRENCY=N` applies independent
  children in parallel. Ordering *across* separate top-level operations is still
  FIFO — cross-op scheduling is future work. See `DESIGN.md` §"Dependencies,
  Value-Passing & Ordering" for the `$ref`/resolver model.
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
  required for explicit localhost-only setups. The install-time root token
  (`<state-dir>/token`) is admin. **Named users with roles** can be added in
  `<state-dir>/users.yaml`; each authenticates with its own bearer token and
  is subject to RBAC (roles: `viewer` ⊂ `editor` ⊂ `admin`):

  ```yaml
  users:
    - name: alice
      role: editor
      tokenFile: alice.token   # relative → resolved under the state dir; minted 0600 if absent
    - name: bob
      role: viewer
      tokenFile: bob.token
  ```

  `ResourceService` enforces the role: mutations (Apply/Delete/InvokeAction)
  need `editor`+, reads (Get/List/DryRun/…) need `viewer`+. Browser sessions
  minted via `SessionService.Login` inherit the caller's role (a viewer-token
  holder who logs in gets a viewer-scoped cookie); `--no-auth` mints admin.
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
- [x] Phase 4.5 followup: split cluster apply into parent + child ops.
      Each per-VM apply + the InstallK3s call now write a row to the
      operations table with `parent_id` set to the cluster op. `op get
      <id> --include-children` (default on for the CLI) returns the
      child rows. Implemented as **descriptive child ops** — the rows
      are real persisted operations but the parent's provider executes
      them in-process rather than each child being independently
      claimed/dispatched. That keeps the suspending-scheduler complexity
      out of v1 while still unlocking the substep visibility Phase U7
      needs. A "true" parent-child orchestration (each child claimable,
      retryable in isolation, parent suspends until children complete)
      is the architectural Phase 9-10 typed-task-IR work.
- [x] Phase 4.5 followup: QGA-based IP discovery in the controller
      path. When `spec.network.staticIPs` is unset, the k3s provider
      polls the VM provider's Get response for `status.ip` (populated
      by the QEMU guest agent) until every node reports its IP, then
      proceeds with the k3s install. Surfaced as a `discover-ips`
      child op so the wait is visible. Static path unchanged.
      Requires `qemu-guest-agent` in the VM template; without it the
      poll times out with a clear "set spec.network.staticIPs to
      bypass" pointer. Used by both the fresh-create and count-up
      paths.

**Verifiable:** confirmed via tests — `TestDeleteCascadesToChildVMs`
exercises cluster-delete → child VM cascade, `TestOwnerOfFindsClusterChild`
proves the OwnerOf walk, and `TestResourceServiceDeleteBlockedWhenOwned`
proves the gRPC-level FailedPrecondition path. End-to-end smoke against
real Proxmox is left for the user since it creates real infrastructure.

---

### Phase 5: Declarative reconciliation + drift surfacing

**Status:** complete (count-up + child-spec changes deferred to Phase 5.x)

**Goals:** the reconciler graduates from "create the missing things" to
"converge to spec." Drift detection on read; structural changes honored;
destructive guardrails enforced.

**Deliverables:**

- [x] `applied_manifests` SQLite table + `internal/controller/manifests`
      store: persisted "desired state", written by the dispatcher on
      apply success and removed on delete success. Stable source of truth
      for drift comparison (the operations table can't serve this — its
      rows get GC'd).
- [x] `Resource.drift` field on the proto, populated on Get/List by the
      resource handler. Loose comparison helper in
      `internal/controller/server/drift.go` walks the desired spec and
      surfaces only the keys where desired ≠ observed; provider-set
      defaults are unmanaged.
- [x] VirtualMachine drift on Get: works generically via the loose
      comparison (proxmox `VMToResource` returns spec keys that overlap
      with the manifest, e.g. `cpu.cores`, `memory.size`).
- [x] Cluster structural drift on Get: the k3s provider synthesizes
      `spec.nodes.controlPlane.count` and each `spec.nodes.workers[*]
      .count` from the *actual* children list, so post-apply Gets reveal
      out-of-band VM deletion as drift.
- [x] Cluster apply: remove children on count-down with
      `--allow-destructive`. Workers go first, then CPs, so we drop
      schedulable capacity before touching apiserver replicas.
      State file is rewritten to reflect the surviving child set.
- [x] Catastrophic-op detection (single-CP recreate, quorum loss, last
      worker removal) requires `--i-know-this-breaks-the-cluster`.
      Quorum threshold is the standard Raft majority `ceil((n+1)/2)`.
- [x] CLI: `--allow-destructive` and `--i-know-this-breaks-the-cluster`
      flags on `openctl ctl apply`. Plumbed through `ApplyRequest` proto
      fields → manifest annotations the k3s provider reads at apply time.
      `openctl ctl get` now renders the `drift` block when present.
- [x] Tests: drift helper unit tests (identical specs, scalar mismatch,
      unmanaged-field tolerance, nested maps, slice length, missing
      observed key, stable ordering); applied-manifests store
      round-trip/overwrite/delete; Cluster Get count synthesis;
      apply-existing tests for no-op, scale-down-needs-flag, scale-down-
      with-flag, catastrophic-needs-i-know-flag, scale-up-not-supported.
- [x] Phase 5.x followup: Cluster apply count-up (adding nodes to a live
      cluster). New `Joiner` in `pkg/k3s/cluster` mirrors `Creator`:
      reads the join token from a surviving CP via SSH, runs the k3s
      install on each new node (server-join for new CPs, agent install
      for new workers), lays down the openctl-k3s-agent using
      newly-minted server certs against the existing per-cluster CA
      bundle. `MintServerCerts` on the certs Bundle extends an existing
      bundle without rotating the CA so existing agents keep trusting
      it. State file's `status.outputs.agent.endpoints` map is updated
      in-place. Pure adds are non-destructive (no flag needed); mixed
      apply (down + up) still requires `--allow-destructive` for the
      remove side.
- [x] Phase 5.x followup: spec changes to existing children with
      `--allow-destructive` (destroy + recreate of a node whose
      cpu/memory changed; runs one at a time so the cluster keeps
      majority CPs throughout). Re-uses the count-up Joiner per node
      after the recreate. Catastrophic gate fires when the only CP or
      the only worker would respec (no apiserver / no scheduling
      target during the gap). Disk respec deferred — the proxmox
      VMToResourceWithIP doesn't surface disk size yet, so we have
      nothing to diff against.

**Verifiable:** confirmed via tests in
`internal/controller/providers/k3s/provider_test.go` —
`TestApplyExistingScaleDownWithFlag` removes a worker when
`--allow-destructive` is set, `TestApplyExistingCatastrophicRequiresIKnowFlag`
blocks a last-worker removal until both flags are passed,
`TestGetSynthesizesObservedCountsFromChildren` proves out-of-band VM
deletion shows up as count drift on Get.

---

### Phase 6: Mac launchd install

**Status:** complete

**Goals:** zero-friction local install. Fresh Mac → one command → openctl
works.

**Deliverables:**

- [x] `openctl-controller install --local` command (per-user LaunchAgent;
      no sudo, no `/usr/local/bin/` write).
- [x] Generates launchd plist at
      `~/Library/LaunchAgents/io.openctl.controller.plist` with RunAtLoad
      + KeepAlive (start now, restart on crash). Logs go to
      `~/Library/Logs/openctl/controller.{out,err}.log`.
- [x] Places binary at `~/Library/Application Support/openctl/bin/openctl-controller`
      via atomic temp-file + rename, so reinstalling over a running
      binary is safe.
- [x] Calls `launchctl load -w`; verifies the controller responds by
      dialing `127.0.0.1:9444` with a 10s budget.
- [x] Seeds `~/.openctl/config.yaml` with a `controller:` stub when the
      file doesn't exist. Existing files are left untouched (they
      typically carry provider credentials).
- [x] Prints next-steps including how to uninstall.
- [x] `openctl-controller uninstall` removes plist + binary; leaves state
      and config alone unless `--purge` is passed (in which case
      `~/.openctl/controller` is removed). Config is *never* rewritten —
      provider credentials aren't ours to delete.
- [x] Documentation: QUICKSTART updated to point at the install command
      first; foreground `serve` retained for development.
- [x] Tests: path resolution, plist rendering (key Label/Args/RunAtLoad/
      log-path fields), config-stub-on-missing, config-leave-on-existing,
      atomic-copy with executable mode, idempotent remove.

**Verifiable:** confirmed via `cmd/openctl-controller/install_test.go` —
the pure-function pieces (paths, plist rendering, config stub, atomic
copy) all unit-tested. End-to-end smoke (`install --local` → `openctl
ping` round-trip) is left for the user since it touches the real
LaunchAgent on the running machine.

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
