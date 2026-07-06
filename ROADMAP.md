# openctl Roadmap

Single index of all tracked work across the project. Each entry links
to the detail doc that owns it; this file is the index, not the source
of truth.

- **Controller rollout:** [CONTROLLER.md](CONTROLLER.md) — Phases 1–6
  complete, 4.5 + 5.x followups pending.
- **Target architecture:** [docs/target-architecture.html](docs/target-architecture.html)
  — speculative Phase 7–10 (verifying-trace cache, K3sNode, typed task
  IR, DAG scheduler). Sketch, not committed plan.
- **UI rollout:** [UI.md](UI.md) — Phases U1–U7. Not started.
- **Strategic direction:** [docs/direction.md](docs/direction.md) — the
  wedge, audience, scope decisions, and priority tiers the next epics
  serve. [docs/plugin-architecture.md](docs/plugin-architecture.md) is
  the technical spine (plugin interface + Terraform host).

Status legend: `[x]` done, `[~]` in progress, `[ ]` not started,
`[d]` deferred / parked.

---

## In flight

- No active roadmap branch. `main` is clean. The dispatcher refactor
  shipped and was homelab-validated with the `validate-3`
  single-control-plane k3s apply path. **k3s multi-endpoint placement**
  (#64/#65) and the **composite-apply dependency DAG** (#66) shipped on
  top; cross-op dependency scheduling is the next natural lift (see
  Followups).

The whole `no route to host` / connection-resilience thread is now
closed: watch-release-on-outage (#14), macOS code signing (#15),
HTTP/2 gateway (#16), and Proxmox context threading + not-found
hardening (#17) all shipped. **Retire `applyExisting`** is now complete
too — the imperative convergence executors are deleted and the
Plan/dispatcher path is the sole existing-cluster converge path,
validated on homelab (3-CP embedded-etcd HA create + full control-plane
respec). The **UI DAG view** (Phase U9) and the **UI for controller
config** (Settings page + GetControllerConfig/UpdateControllerConfig
RPCs) have both shipped. With the feature build-out essentially
complete, direction now shifts to **breadth + reach**: the external
plugin protocol → Terraform/OpenTofu host → run-anywhere Linux daemon
(Tier 1 in [docs/direction.md](docs/direction.md)). Multi-user auth
(OIDC slice) and CUE-WASM validation are parked behind that. (Mobile
layout has since shipped.)

## Suggested next order

The UI/controller feature build-out is essentially complete (all UI
phases + arch Phase 8 shipped). The next round is driven by the
strategic direction in [docs/direction.md](docs/direction.md) — go
**wide** (any-provider ecosystem) and **run-anywhere**, while preserving
the per-resource-independence wedge. Priority tiers (full rationale in
direction.md):

**Tier 1 — the spine (roughly in sequence):**

1. **External plugin protocol** — ✅ **shipped** (PRs #42–#45). The generic,
   reusable provider interface serialized as the v2 pluginproto protocol:
   protocol + SDK, external provider adapter + registry/config wiring,
   plugin-supplied CUE schemas, and the `plugins/example` reference
   provider. Author reference: [docs/plugin-protocol.md](docs/plugin-protocol.md).
   Shaped with the Terraform host as an explicit second consumer (opaque
   state/private blobs already carried on the wire).
2. **Terraform / OpenTofu provider host** — ✅ **shipped** — a second implementer of that
   interface; the breadth multiplier that unlocks the whole provider
   registry (AWS/GKE/…). Design + honest hard-parts analysis in
   [docs/plugin-architecture.md](docs/plugin-architecture.md).
   - *Prereq shipped:* the `provider_state` opaque store (migration 0009 +
     `internal/controller/providerstate`) — the TF host reuses it directly.
   - *Phase A shipped:* the transport — `internal/controller/providers/tfhost`
     launches a real tfplugin6 provider over HashiCorp go-plugin and fetches
     its schema (vendored stubs in `pkg/tfplugin6`, tested against the in-repo
     `plugins/tf-fake` provider — no external download needed).
   - *Adapter lifecycle shipped:* `internal/controller/providers/tfhost`
     now exposes a `providers.Provider` adapter for explicit Kind →
     Terraform type mappings: Apply→Plan+Apply, Get→Read, and
     Delete→Plan+Apply(null), threading opaque `DynamicValue` + private blobs
     through `provider_state`.
   - *Schema translation shipped:* mapped Terraform resource schemas generate
     standalone CUE and register through the existing external-schema path, so
     SchemaService/validation can see hosted Terraform kinds.
   - *Config/registration shipped:* controller config can now launch
     operator-configured Terraform provider binaries, pass provider-level
     config through `ConfigureProvider`, map openctl Kinds to Terraform
     resource types, register generated schemas, and reap hosted provider
     processes at shutdown.
3. **Run-anywhere: portable Linux daemon + `install --target ssh://`** — ✅
   **shipped** (PRs #47–#48). systemd support (user unit local + system unit
   remote) behind a `serviceManager` abstraction; `make build-controller-linux`
   static cross-compile; `install --target ssh://user@host` remote deploy.

**Tier 2 — natural follow-ons:** self-hosting bootstrap
(`install --target proxmox://`); multi-user auth (OIDC/RBAC, downstream
of adoption).

**Tier 3 — parked:** client-side CUE WASM validation. (Mobile layout,
formerly Tier 3, has shipped.) Workloads/PaaS is vetoed by scope.

**Cross-cutting:** test every capability against the wedge (no global
plan/state); harden the provider contract before the ecosystem widens.

- [x] **Provider conformance suite** (`internal/controller/providers/providertest`).
      A reusable `Suite` + `Capabilities` battery encoding the baseline
      `providers.Provider` contract once — Apply identity, Get-after-Apply
      round-trip, `*providers.NotFoundError` on missing Get, idempotent Delete,
      Delete-removes, List enumeration — with capability flags for legitimate
      variations (`SupportsList`, `NoOpOnExisting`). Self-tested for teeth (a
      compliant in-memory provider passes; deliberately-broken ones fail).
      **Bound to:** the external-plugin adapter (in-process pluginproto, the
      primary ecosystem-widening path), the Terraform host (tf-fake over
      subprocess, exercising `SupportsList=false`), and the compiled-in
      **proxmox VirtualMachine** provider (stateful in-memory fake Proxmox API,
      `NoOpOnExisting=true`) — so all three provider classes (compiled-in,
      external, TF host) are covered. Binding proxmox surfaced and fixed two
      contract gaps: (1) (#71) — `Apply` returned a nil `Resource` from the
      create/apply paths; it now reads the VM back and returns observed state,
      per the `Provider` interface doc. (2) — `applyVM` mutated an existing VM
      (`ConfigureVM`/`ResizeVMDisk`) rather than the no-op CONTROLLER.md:23
      locks in; it now no-ops and returns observed state (surfacing drift via
      the manifest-vs-observed comparison), matching the documented atomic
      contract, and the binding asserts `NoOpOnExisting=true`. *If in-place VM
      update via re-apply was actually intended, revert that change and update
      CONTROLLER.md:23 instead.* **Composite k3s Cluster — resolved:** a
      composite provider is not atomic CRUD, so it doesn't fit the
      `providertest.Suite`; its contract is its `Plan()` (children carry owner
      refs, the child `$ref` graph is acyclic and self-contained so
      `operations.RunGraph` can schedule it, and Plan is deterministic). Those
      invariants are now pinned by `cluster_plan_contract_test.go`
      (`TestPlanChildGraphIsSchedulable`, `TestPlanIsDeterministic`) alongside
      the existing owner-label test. A shared composite harness is deferred
      until a second composite provider exists to justify the generality.

---

## Controller rollout — CONTROLLER.md

### Phases (complete)

- [x] **Phase 1** — Controller skeleton + auth + minimal CLI client
- [x] **Phase 2** — proxmox VirtualMachine provider compiled in
- [x] **Phase 3** — Async operations + persistence
- [x] **Phase 4** — k3s Cluster provider compiled in
- [x] **Phase 5** — Declarative reconciliation + drift surfacing
- [x] **Phase 6** — macOS LaunchAgent install/uninstall

### Followups (pending)

- [x] **Phase 4.5** — Parent-child operation rows (descriptive child
      ops: per-VM apply + k3s-install rows under the parent). True
      suspending-dispatcher orchestration deferred to arch Phase 9-10.
- [x] **Phase 4.5** — QGA-based IP discovery (polls VM provider's
      `status.ip` so `spec.network.staticIPs` is optional when the VM
      template has qemu-guest-agent).
- [x] **Phase 5.x** — Cluster apply count-up (new `Joiner` adds nodes
      to a live cluster, extending the existing CA bundle without
      rotating it).
- [x] **Phase 5.x** — In-place spec changes on existing children
      (destroy+recreate of a node whose cpu/memory drifted; one at a
      time, rejoined via the Joiner). Disk respec deferred — observed
      VM spec doesn't surface disk size.
- [x] **k3s multi-endpoint placement** (#64, #65). Spread cluster nodes
      across Proxmox hosts (per-pool `nodes` lists) and across separate
      Proxmox endpoints (per-pool `context` + a general `targets:
      [{context, node}]` list). Context routing lives inside the proxmox
      provider (`NewMulti(map[ctx]*Config, defaultCtx)`, `sync.Map`
      endpoint index); k3s just stamps `spec.context`/`spec.node` on each
      VM child. Scoped to endpoints sharing one L2 network — separate-L2
      spread (per-pool subnets, routable join URL, wireguard flannel) is a
      parked epic.
- [x] **Composite-apply dependency DAG** (#66). Ordering within a single
      composite `Apply` is now a real dependency graph
      (`operations.RunGraph`: topological execution + cycle detection),
      replacing hand-coded phase loops. Edges derive from `$ref`s between
      children (`RefChildEdges`) plus explicit barrier edges (CA bundle).
      Serial by default; `OPENCTL_APPLY_CONCURRENCY=N` runs independent
      children in parallel. See `DESIGN.md` §"Dependencies, Value-Passing
      & Ordering".
- [~] **Cross-op dependency scheduling** — implemented, **flag-gated,
      default-off**. When `OPENCTL_CROSS_OP_SCHEDULING` is set, the dispatcher
      claims the whole pending batch and runs it through `operations.RunGraph`:
      independent ops run concurrently (`OPENCTL_CROSS_OP_CONCURRENCY`, default
      4), dependent ops are ordered by their `$ref` edges (`crossOpEdges`, the
      op-level analog of `RefChildEdges`). Failure is isolated (a failed op
      doesn't stop independents; a dependent whose predecessor failed fails at
      ref resolution); a `$ref` cycle falls back to unordered scheduling so no
      op is left claimed-but-unrun. Default path is unchanged FIFO, so the
      locked single-goroutine / fail-fast-collision behavior only changes on
      opt-in. **Remaining: flip the default to on** after homelab validation —
      that is the point the reopened decisions need sign-off. Design +
      decisions: [docs/cross-op-scheduling.md](docs/cross-op-scheduling.md).

### Followups (post-Phase-6, parked)

- [x] External plugin protocol — shipped as the **v2 pluginproto**
      protocol (persistent process, id-correlated JSON-over-stdio, one-time
      configure, opaque state/private blobs) plus an external provider
      adapter, plugin-supplied CUE schemas, and the `plugins/example`
      reference provider. This is Tier 1 item 1; see
      [docs/plugin-protocol.md](docs/plugin-protocol.md) for the author
      reference and [docs/plugin-architecture.md](docs/plugin-architecture.md)
      for the design.
- [x] **Run-anywhere: Linux daemon** (Tier 1 item 3). Local install works on
      Linux (systemd user unit) via a `serviceManager` abstraction (launchd on
      macOS, systemd on Linux); `make build-controller-linux` cross-compiles a
      static ELF controller (CGO_ENABLED=0). **Remote deploy shipped:**
      `install --target ssh://user@host` cross-builds + SSHes the controller to
      a remote host and installs it as a **system** systemd service (reuses
      `pkg/k3s/ssh`; uploads binary + unit, `systemctl enable --now`). The
      orchestration is unit-tested against a fake SSH runner.
- [~] Proxmox bootstrap install (`openctl-controller install --target
      proxmox://homelab`). First slice shipped: `proxmox://context`
      target parsing, bootstrap VM manifest generation, VM create/poll
      through the existing Proxmox provider, then handoff to the SSH
      Linux installer. Still needs homelab validation before marking done.
- [x] Plugin-defined CLI subcommands (`openctl k3s logs/restart/upgrade`).
      Generic protocol + CLI registration shipped: plugins advertise typed
      subcommands in capabilities, and the CLI dispatches them with
      positional/flag values in `Request.Args`. The k3s plugin advertises and
      implements `logs` (fetch a node's k3s journal), `restart` (restart k3s
      on a node), and `upgrade` (binary-swap a node to a target k3s version:
      the agent downloads + sha256-verifies the release, atomically swaps the
      binary, and restarts). All run against the per-node agent client.
      Cluster-wide rolling upgrade (drain/cordon ordering) remains a follow-up.
- [x] Bug fix: the proxmox handler collapsed any `GetVM`/`GetNode`/
      `GetTemplate` error to NotFound — a network timeout produced a false
      "VM gone" result, and `applyVM` treated it as "doesn't exist" and
      cloned a duplicate. The client now returns a wrapped
      `client.ErrNotFound` sentinel only for a genuine miss; the handler
      branches on `errors.Is(..., ErrNotFound)` and surfaces transient
      failures as real errors so callers retry instead of recreating.

---

## Target architecture — docs/target-architecture.html

Speculative roadmap from the BSALC / Crossplane / BuildKit discussion.
The HTML doc is the long-form design; this section tracks what's been
delivered and what remains, and notes where the original plan has
evolved.

- [x] **Arch Phase 7** — Verifying-trace cache (per-resource v1: skip
      provider.Apply when manifest hash matches last success; calls
      provider.Get to populate result and marks op with a "cached"
      label). Parent-hash-aware (children's hashes folded into the
      parent hash) deferred until composite ops are reified.
- [x] **Arch Phase 8 (scoped)** — Owner-ref / children plumbing on the
      Resource proto, Registry.ChildrenOf + OwnerRefOf helpers, k3s
      Cluster implements ChildrenLister so Get/List/Watch return its
      VM children, child resources surface their owning Cluster via
      Metadata.OwnerRefs. Unblocked UI U3.3 deferred + U6.
- [x] **Arch Phase 8 (full)** — genuinely multi-session
      architectural lift. Steps 1–5 + the dispatcher refactor
      landed. Cluster.Apply's initial-create path now fans out
      through Plan → ChildDispatcher, giving each VM / K3sNode /
      AgentInstall its own resolve+cache+save pipeline. Homelab
      validation is complete: `validate-3` reached Ready after PRs
      #5–#9 fixed plan child normalization, cloud-init/k3s install
      hardening, SSH-drop recovery, nil-safe reconnect cleanup, local
      agent binary packaging, and Provisioning-stub resume. Retiring
      the imperative `applyExisting` branch remains a separate cleanup.
      Post-validation hardening: PRs #10 and #12 removed flaky
      operation-cache test submits under the race detector; PR #11
      kept UI resource watch streams alive across transient provider
      list failures — but over-corrected: it made a failing Watch
      retry *forever* server-side, so a permanently-unreachable
      provider (offline homelab Proxmox → `no route to host`) pinned
      its browser HTTP/1.1 connection + gateway gRPC stream open
      indefinitely. The UI nav opens one long-lived Watch per kind, so
      two dead Proxmox kinds exhausted the browser's ~6-per-origin
      connection pool and every other page hung. Fixed in
      `fix/watch-release-conn-on-outage`: Watch now tolerates a bounded
      burst of list failures (5 ticks ≈ 2.5s) then returns
      `Unavailable`, releasing the connection so the client's reconnect
      backoff takes over. Same PR adds a 5s TCP dial timeout to the
      Proxmox client so a black-holing host fails fast instead of
      hanging the full 60s request timeout.
      1. [x] **ResourceRef as spec-level primitive.** CUE `#Ref`
         helper in base.cue authors `{$ref: {apiVersion, kind,
         name, field?}}` markers. Server-side resolver
         (`internal/controller/refs`) walks specs pre-Apply,
         calls Registry.Get on each ref, substitutes the value
         (whole resource or dotted status/spec path). Wired into
         dispatcher.execute (before provider.Apply so providers
         see resolved values) and DryRunApply (so previews are
         accurate). Unresolvable refs → op fails with a specific
         "ref X/Y/Z: not found" message; DryRun surfaces it as a
         validation error rather than a 500.
      2. [x] **K3sNode resource + provider.** New kind that owns
         one k3s install on one node. `spec.vmRef` (whole-resource
         `#Ref` to a VM) + `spec.role` (server|agent) + `spec.joinFrom`
         + `spec.joinURLFrom` (for non-first nodes; resolve to another
         K3sNode's `status.nodeToken` / `status.vmIP`). Provider
         SSHes to the resolved VM IP, runs the appropriate k3s
         install command, captures nodeToken from the server (so
         later K3sNodes can resolve joinFrom refs against it), and
         saves the first server's kubeconfig at the standard path
         (so the existing get-kubeconfig action works for
         standalone K3sNode installs). State persisted at
         `~/.openctl/state/k3s-nodes/<name>.yaml`. Test coverage:
         parsing + install-command shape + state round-trip (7
         tests). Ships standalone-useful — users can author
         K3sNode manifests directly without going through the
         composite Cluster orchestration.
      3. [x] **AgentInstall as sibling.** One openctl-k3s-agent
         install per node as a first-class resource. `spec.vmRef`
         (`#Ref` to a VM) + `spec.clusterName` (names the existing
         k3s Cluster whose CA bundle backs this install) + `spec.ssh`.
         Provider loads the on-disk cert bundle from
         `~/.openctl/state/k3s/<cluster>/`, mints a server cert for
         the node if missing, SSH-installs the openctl-k3s-agent
         binary via the existing bootstrap.Installer, persists state
         at `~/.openctl/state/k3s-agent-installs/<name>.yaml`. Delete
         best-effort uninstalls the service + drops config. Runs
         alongside the Cluster's inline agent install today — a
         future step will wire the Cluster's Plan output through
         Apply, at which point the inline install goes away.
      4. [x] **Cluster.Plan capability** *(scoped)*. Introduces
         `providers.Planner` interface; k3s Cluster implements
         `Plan()` which returns the VirtualMachine + K3sNode +
         AgentInstall child manifests a Cluster expands to, with
         `$ref` pointers linking them (K3sNode joinFrom pointing at
         the first CP's status.nodeToken, AgentInstall vmRef pointing
         at its VM, owner labels for attribution). 9 tests cover
         single-CP, HA 3-CP, worker pools, static-IPs flow-through,
         version + extraArgs propagation. The dispatcher now consumes
         Plan output for initial Cluster create via `ChildDispatcher`;
         homelab validation covered the VM → K3sNode → AgentInstall
         flow end to end.
      5. [x] **Verifying-cache refs_hash extension.** Two-dimensional
         cache: `input_hash` (raw manifest — user intent) plus
         `refs_hash` (resolved $ref values — upstream state).
         Migration 0008 adds the column. Dispatcher now preserves
         the raw manifest through resolve/apply (fixing a latent bug
         where the stored `spec_json` held resolved values, losing
         `$ref` markers), computes both hashes, and requires BOTH to
         match for a cache hit. Otherwise the raw manifest looks
         identical while an upstream VM's IP silently changes, and
         we'd serve a stale cache. Store + DiskMirror gained
         `SaveWithRefsHash` / `LoadHashes`; old `Save` / `LoadHash`
         still work (they set/read empty refs_hash, which safely
         forces a miss). Test coverage: unchanged target → cache
         hit; changed target with same raw manifest → cache miss.

### Rescoped from Phase 9 / 10

Original Phases 9 (verifying-trace rebuilder) and 10 (continuous
reconcile) don't survive contact with what actually shipped and how
the tool ended up being used. Reasons:

- Phase 9's *per-resource* verifying cache is Phase 7, already done.
  The remaining refs_hash extension depends on Phase 7 the design
  doc (spec-level ResourceRef primitives), which is deferred behind
  the full Phase 8. Standalone Phase 9 has nothing to bite on.
- Phase 10's core mechanism — periodic drift check with per-resource
  state — is U8.3, already done. The delta is auto-remediation on
  top, which is a focused feature, not a phase.

Replaced with two smaller entries:

- [x] **Refs-cache extension** — Shipped as full-Phase-8 step 5;
      see the checklist above. Two-dimensional verifying-trace
      cache (input_hash + refs_hash), migration 0008.
- [x] **Opt-in auto-remediation** — opt-in per resource via
      `openctl.io/autoReconcile: true` annotation. When drift is
      detected on an annotated resource, the reconciler enqueues an
      Apply of the stored manifest with source="auto-reconcile" so
      the op history shows why it fired. Exponential-backoff
      throttling (30s → 1h) on repeated failure so a persistently-
      broken resource doesn't hammer the provider. Default off —
      unannotated resources continue to only log drift.

Open design questions captured in the HTML doc; revisit before
committing to any of these.

---

## UI rollout — UI.md

- [x] **Phase U1** — UI backend prerequisites complete (U1.1 Watch RPCs,
      U1.2 SchemaService, U1.3+U1.5 grpc-gateway REST + embed.FS UI
      asset hosting + session cookie middleware, U1.4 SessionService
      with sha256-stored session tokens). HTTP gateway listens on
      127.0.0.1:9445 alongside gRPC on 9444; UI placeholder page until
      Vite build lands. No frontend code yet.
- [x] **Phase U2** — Manifest store on disk + git sync.
      - [x] **U2.1** — Disk mirror (controller materializes desired state
            to `~/.openctl/manifests/<apiVersion>/<kind>/<name>.yaml`
            after every successful apply, removes on delete; atomic write
            via temp+rename; startup reconciliation re-materializes
            missing files, logs orphans without deleting; config schema
            in `manifests:` block).
      - [x] **U2.2** — Git integration. `manifests.git.enabled` opts in;
            controller runs `git init -b <branch>` on first start,
            commits each materialize/delete with `apply X/Y via CLI|UI`
            (source from gRPC metadata, stamped by HTTP gateway).
            Push modes: `onCommit` (default w/ remote), `periodic`
            (background ticker), `manual` (RPC only). `RepoService`
            RPC: GetStatus/Push/Pull. Push failures logged, never
            block apply.
- [x] **Phase U3** — UI shell + read-only views (Vite+Svelte skeleton,
      list/detail/op-history with live Watch streams, git status
      indicator).
      - [x] **U3.1** — Vite+Svelte+TS scaffold; embed pipeline (Vite →
            `internal/controller/server/uiassets/dist/` via
            `//go:embed all:uiassets/dist`); `make ui` install+build;
            login screen (root bearer → HttpOnly session cookie);
            WhoAmI confirms session; logout button + 401 → login.
      - [x] **U3.2** — Layout shell (header + left nav grouped by
            provider, main pane); hash router; kind catalogue with live
            counts (ListSchemas + parallel List fan-out); per-kind
            resource list with state + drift badges.
      - [x] **U3.3** — Resource detail (desired manifest / observed
            state / drift diff / last-applied timestamp). Proto: Get
            response gains `applied` + `applied_at`. Owner-ref +
            composite children tree shipped as a U3.3-deferred follow-up
            after arch Phase 8 (scoped) added the proto surface —
            Detail.svelte now renders an owner banner above the manifest
            panes for owned resources and a read-only children list
            below for composite parents.
      - [x] **U3.4** — Live Watch streams + ops drawer. fetch +
            ReadableStream bridge over grpc-gateway's ndjson; ResourceList
            and Detail subscribe to ResourceService.Watch; collapsible
            bottom drawer subscribes to OperationService.WatchOperations
            with the last 200 ops, in-flight count, and per-op links.
            Reconnect-with-backoff on transient errors.
      - [x] **U3.5** — Git status indicator in the header (10s
            poll of RepoService.GetStatus) + Push-now button when remote
            is configured; Watch-driven catalogue counts (one stream
            per kind, ADDED/DELETED updates); Vitest harness with unit
            tests for stream parsing, router, and status-badge format.
            Playwright headless-Chrome e2e explicitly deferred (~200MB
            of browsers + non-trivial CI is wrong tradeoff for a
            homelab project).
- [x] **Phase U4** — CUE/manifest editor (Monaco-based, server-side
      validation, diff view, `DryRunApply` RPC, destructive gates as
      checkboxes).
      - [x] **U4.1** — `ResourceService.DryRunApply` RPC server-side +
            optional `providers.DryRunner` interface for composite
            providers (k3s `Cluster` wired up; reuses the existing
            change-plan + catastrophic-check chain).
      - [x] **U4.2** — Monaco editor wired into `/edit/...` route,
            lazy-loaded so list/detail bundles stay light. 350 ms
            debounce on edits → SchemaService.Validate → Monaco markers
            + diagnostics card. Detail pane gets an "Edit" button.
      - [x] **U4.3** — Apply panel inline in the edit pane: one
            debounce fires Validate + DryRunApply in parallel; preview
            shows diff + child verbs + summary; required gates render
            as labelled checkboxes; Apply submits with gate flags and
            tails the resulting op via the existing ops store.
      - [x] **U4.4** — Monaco diff view. Tab toggle in the edit pane
            ("Editor" / "Diff vs applied"); read-only; shares the
            lazy Monaco bundle. Closes Phase U4.
- [x] **Phase U5** — Typed form editor (CUE-AST → form-schema bridge,
      AWS-console stepped sections, live manifest preview, view
      toggle).
      - [x] **U5.1** — `internal/schema/form` walks CUE → typed Field
            tree; `SchemaService.GetFormSchema` RPC ships it as
            JSON-in-string. Handles primitives, objects, arrays,
            optional+required, defaults, number bounds, const literals.
      - [x] **U5.2** — Svelte form renderer (`FormField.svelte`,
            recursive); three-way view toggle (Form / Editor / Diff);
            live YAML preview alongside the form; form edits drive
            the same `text` state as the editor so Validate +
            DryRunApply preview + Apply keep working unchanged.
      - [x] **U5.3** — Advanced field types: string-literal disjunctions
            → enums (rendered as select), regex constraints → pattern
            (rendered with HTML pattern attr + invalid styling), maps
            (`{[string]: T}`) → FieldMap (rendered as key/value
            add-row editor). Non-literal disjunctions still emit
            `unsupported`; stepped sections deferred until a real
            schema demands them.
      - [x] **U5.4** — `extraKeys` walks the form schema vs the parsed
            YAML to find non-roundtrippable paths; Form tab disabled
            with offending-keys tooltip when the editor carries
            anything the form would drop. View auto-snaps to Editor
            when an unknown key appears while on Form. Closes Phase U5.
- [x] **Phase U6** — Composite resource UX. Detail.svelte fans out one
      Get per child to render per-row status + drift pills; an
      aggregated "N children · M drifted · K unhealthy" pill rides next
      to the parent state in the header. Edit.svelte detects ownerRefs
      and blocks Apply with a banner pointing to the owner ("Edit X
      instead →"). Apply preview's child verbs link to each child's
      detail page (except for `create` verbs whose target doesn't exist
      yet). DAG view broken out as its own phase (Phase U9) now
      that arch Phase 8 has landed the plumbing.
- [x] **Phase U7** — Op orchestration polish. CancelOperation RPC +
      new StatusCancelled status (pending-only first pass; running ops
      still need cooperative cancellation in providers, parked).
      ListOperationsRequest gains source + since/until filters;
      GetOperation now returns manifest_json so the UI's retry button
      can pre-fill the editor with the exact failed payload (via
      sessionStorage handoff between the ops drawer and Edit.svelte).
      Ops drawer rewritten: status/source/text filter controls,
      per-row Cancel button on pending ops, Retry on
      failed/interrupted/cancelled, expandable parent rows with a
      substep checklist driven by include_children on
      WatchOperations.

### Phase U8 — Post-U7 polish (in flight)

Not a pre-planned phase; the punch list that emerged from actually
using the UI to author resources. Focus: turn "the editor works" into
"authoring a VM/Cluster is genuinely pleasant."

Shipped this session:

- [x] **U8.1** — ProxmoxNode as a first-class observed-only kind
      (`e7b8605` filter + `2f59e2c` node kind). Providers can now
      declare `ObservedOnlyKinds` so infrastructure discovered from the
      provider API (never applied) shows up in Get/List/Watch alongside
      user-managed resources.
- [x] **U8.2** — Managed-only filter on Get/List/Watch. Resources not
      in `applied_manifests` (unless observed-only or owned by an
      applied parent) are hidden from the API surface, matching the
      "openctl ignores out-of-band resources" direction.
- [x] **U8.3** — Periodic drift reconciler (`35820d3`). Background loop
      re-checks every managed resource on a configurable interval,
      logging drift transitions. Manual "Reconcile" button on the
      Detail page re-applies the stored manifest on demand.
- [x] **U8.4** — VirtualMachine schema expansion (`cb61619`). Docs,
      defaults, enums (osType/bios/machine/network model), bounded
      numbers (vlan 1..4094), and new fields wired through Go:
      `networks[].vlan/firewall/macAddress`, `cloudInit.searchDomain/
      nameservers`.
- [x] **U8.5** — Per-disk Proxmox flag knobs (`527c13b`). Schema +
      Go wiring for `disks[].ssd/discard/iothread/backup/cache` via
      new `SetDiskOptions` client helper that merges flags into the
      existing disk config string.
- [x] **U8.6** — k3s Cluster schema expansion (`2cc8a18`). Same
      docs/defaults/enums treatment. Introduces `#NodeSize` so size
      overrides render as structured number inputs instead of
      FieldAny freeform boxes.
- [x] **U8.7** — Create flow (`86bf57e`). New `/new` route reuses
      Edit.svelte in create mode; schema-driven seeded manifest;
      "+ New &lt;Kind&gt;" button on ResourceList.
- [x] **U8.8** — Create polish (`4db4927`). Optional composite fields
      collapse to `+ &lt;name&gt;` buttons until clicked; inline name-
      collision check against existing resource list.
- [x] **U8.9** — Form layout fix (`c64a09a`). Vertical row layout
      (label above input) so deep nested paths aren't crushed by the
      10rem-label-per-level grid; form pane widened to 2fr/1fr vs the
      manifest preview.
- [x] **U8.10** — Fix optional-composite expand (`5da3e02`). Empty
      `{}` and `[]` no longer count as "unset" for collapse purposes —
      previously fields with no required children (cloudInit,
      sshKeys, etc.) were stuck permanently collapsed.

Punch list (unstarted, prioritized):

- [x] **U8.11** — Provider-populated dropdowns (first slice).
      CUE `@options(kind="X" [, apiVersion="Y"])` attribute; form
      walker emits Field.OptionsSource; new `ui/src/lib/options.ts`
      lazy-caches resource-name lists keyed by (apiVersion, kind);
      FormField renders a select when a resolved options list is
      available. Wired for `VirtualMachine.spec.node` →
      ProxmoxNode. Storage / bridge / dependent dropdowns (e.g.
      storages on the selected node) still pending — needs a
      field-to-field dependency convention that this MVP doesn't
      model.
- [x] **U8.12** — Runtime actions on resources (VM lifecycle first
      slice). New optional `providers.Actioner` interface: providers
      declare per-kind action lists and handle DoAction. Two new
      RPCs on ResourceService: `ListActions` (used by Detail to
      build the button bar) + `InvokeAction`. Proxmox VM supports
      start / shutdown / stop / reboot; destructive actions (stop /
      shutdown / reboot) prompt for confirmation. Detail auto-
      refetches 800ms after invocation so status catches up
      before Watch does. Cluster kubeconfig download + VM console
      access still parked — different modality (file / websocket)
      than the fire-and-forget action RPC covers.
- [x] **U8.13** — Discriminated-union picker for VM image source.
      CUE convention `@oneOf(group="X")` — sibling fields sharing a
      group name render as a single picker in the form editor:
      radio-style buttons at the top, only the chosen alternative's
      sub-form appears below, switching alternatives clears the
      previous one. Wired for VirtualMachine.spec.{template,
      cloudImage, image}.
- [x] **U8.14** — Direct delete from Detail with a type-the-name
      confirmation (kubectl / AWS-console style). Success navigates
      to the list; the resource disappears on the next Watch tick.
      Not surfaced on List rows yet — Detail is the primary
      delete-from-UI path.
- [x] **U8.15** — Per-field validation error highlighting. New
      `schema.ValidateStructured` returns path-attributed errors
      via cueerrors.Errors; `DryRunApplyResponse.field_errors`
      ships them to the UI; Edit.svelte publishes a per-path map
      on a Svelte context; FormField adds a red left-border rail
      and inline message to the offending row. Bottom-panel error
      list stays as a fallback for path-less errors.
- [x] **U8.16** — List sort/filter/search. Free-text filter box +
      click-to-sort column headers (name / state / drift). Applied
      client-side over the live Watch snapshot so the stream keeps
      populating.
- [x] **U8.17** — Live progress on the detail page. Subscribes to
      the shared ops store and shows an inline banner for any
      pending/running op matching this resource; on terminal
      transition (op moves out of pending/running) auto-refetches
      so the observed state catches up promptly.
- [x] **U8.18** — Better create defaults. The seed manifest now
      pre-fills `metadata.name` with a kind-derived suggestion
      (`vm-a3b2`, `cluster-x9k1`, etc). Users can accept or type
      over. The suggestion is stable per-Edit-instance so the
      schema-upgrade path can still equality-check the stub.
- [x] **U8.19** — Copy/download YAML on detail. Two small buttons
      in the Desired manifest card head: Copy YAML (clipboard) and
      Download (as `<kind>-<name>.yaml`). Falls back to the observed
      resource for observed-only kinds that have no applied
      manifest.
- [x] **U8.20** — Manifest-preview toggle in the form view. Hide
      button in the preview head collapses the preview pane; a
      "Show manifest" affordance replaces it. Preference persists
      via localStorage.
- [x] **U8.21** — Map-of-objects rendering polish. When the map's
      value type is composite (object/array/map), the row switches
      to a stacked layout: key + remove on top; the nested sub-form
      spans the full width underneath, indented with a subtle
      left-border rail. Fixes the awkward alignment on things like
      `cloudInit.ipConfig`.

### Phase U9 — Composite DAG visualization (shipped)

Now that arch Phase 8 has landed the plumbing (Plan output, child
owner labels, per-child ops rows via the dispatcher refactor),
Detail for composite resources renders a real graph instead of just
the flat children list.

- [x] **U9.1** — Server-side DAG endpoint. `GetChildrenGraph` on
      `ResourceService` (`/v1/resources:childrenGraph`) synthesizes a
      `{nodes, edges}` graph. Structural source is the provider's
      `Planner` output (k3s Cluster → VMs + K3sNodes + AgentInstalls,
      each carrying `$ref` pointers), falling back to
      `registry.ChildrenOf` for non-Planner composites. Edges: `owns`
      (root → child) + `ref` (child → sibling), the latter parsed via
      the new `refs.Collect` walker so the UI never re-implements ref
      parsing. Node status is a coarse pill (`applied` / `pending` /
      `observed` / `missing`) derived from a live provider `Get`;
      planned children are always `managed`, observed-only children
      come back `managed=false`.
- [x] **U9.2** — Svelte graph renderer (`DagView.svelte`). Hand-rolled
      longest-path layered SVG layout (no new dep — graphs are tiny
      and the strict CSP disfavors CDN libs). Node = kind + name +
      status pill; `owns` edges dashed-gray, `ref` edges accent-blue
      with the source field on hover. Click a node to open its Detail.
- [x] **U9.3** — Live operation overlay. `DagView` joins graph nodes
      against the live operations store by `apiVersion/kind/name`,
      flattens parent + child op rows, and shows the latest
      pending/running/failed/interrupted/canceled op as a compact node
      pill. Clicking a graph node now opens the ops drawer focused on
      that resource, auto-expanding parent rows when the matching op is
      a child step. Operation status tone mapping is centralized in
      `format.ts`, including the backend's `canceled` spelling.
- [x] **U9.4** — Observed-only / unmanaged children (no applied
      manifest, not Planner-authored) render dim with a "read-only"
      badge and no `ref` edges, since no `$ref` metadata exists.

---

## Future goals (parked)

Cross-cutting items that don't belong to a single track. Promote into a
phase plan when ready to commit.

- [x] **Templates (MVP)** — parameterized starters. Go-defined
      templates compiled in (deferred a CUE-templating engine for
      user-authored templates as a future extension). New
      `TemplateService` with ListTemplates / GetTemplate /
      RenderTemplate RPCs. UI sidebar "Templates" link → picker
      grid → wizard form with live rendered-manifest preview +
      DryRunApply, submits through the normal Apply pipeline and
      navigates to the new resource's detail page.
      Two starters shipped: `ubuntu-server-vm` (Ubuntu 22.04 on
      Proxmox with cloud-init, QEMU agent, cloud image URL baked
      in) and `small-k3s-cluster` (k3s with static-IP networking).
      Each created resource is stamped with the
      `openctl.io/template: <name>` annotation for provenance.
- [x] **Two-way GitOps** — fsnotify watcher on the manifest mirror
      dir. On file change, parse + compare against applied_manifests
      + Apply if different (comparison guarantees loop-safety: our
      own DiskMirror writes trigger fsnotify events, but the content
      matches the store so we skip). File removals optionally submit
      Delete ops (opt-in via `deleteOnRemove: true`). Ops are tagged
      source="gitops" so the audit trail is honest. Opt-in via
      `manifests.gitops.enabled: true`. Debounces rapid successive
      writes (500ms) to handle editor truncate+write patterns.
- [~] **Multi-user auth** — OIDC integration, named sessions, RBAC on
      `ResourceService`. Cookie/session layer from U1 is the
      foundation.
      - *RBAC spine shipped:* the auth interceptors now resolve an
        `auth.Principal{UserID, Role}` (roles: viewer ⊂ editor ⊂ admin) and
        inject it into the request context; `ResourceService` mutations
        (Apply/Delete/InvokeAction) require editor+, reads (Get/List/DryRun/
        ListActions/GetChildrenGraph) require viewer+. The root token and all
        current sessions resolve to admin, so enforcement is a no-op in
        production until a non-admin identity source exists; `--no-auth`
        skips the interceptor entirely (every caller trusted).
      - *Identity source shipped — named API tokens:* `<state-dir>/users.yaml`
        defines named users with a role and a `tokenFile`; the auth interceptor
        resolves a user's bearer token to its `{UserID, role}` principal, so
        RBAC is now **live** for token/CLI callers (a viewer token is genuinely
        read-only). Chosen for lowest lock-in (no external IdP; OIDC can layer
        on later).
      - *Session role inheritance shipped:* the `sessions.role` column
        (migration 0010, defaulting admin) + `SessionService.Login` reads the
        caller's principal and mints the session with their user+role, so a
        viewer-token holder who logs in gets a viewer-scoped cookie.
        `--no-auth` still mints admin. Browser logins are now role-scoped.
      - *WhoAmI surfaces role:* `WhoAmIResponse.role` now reports the caller's
        RBAC level (session → its role, named-user/root → the principal's), and
        `openctl whoami` prints your user + role from the CLI. UI display is a
        small frontend follow-up.
      - *Next — OIDC:* external IdP → claims → role (the last big auth slice).
        **Design proposal:** [docs/oidc-design.md](docs/oidc-design.md) — OIDC
        as a new session-minting front door (Authorization Code + PKCE,
        discovery, claims→role deny-by-default) that reuses the shipped
        session/cookie/RBAC machinery; testable against a fake IdP without an
        external provider. Awaiting sign-off.
- [x] **Terraform / OpenTofu provider host** *(Tier 1 — see
      [docs/direction.md](docs/direction.md))* — consume the existing
      Terraform provider ecosystem (AWS, GCP, Azure, Cloudflare, …)
      instead of hand-writing every provider, by adding a *second
      implementer* of the openctl provider interface that delegates to
      any `terraform-provider-*` binary over tfplugin6 gRPC. One adapter
      → the whole registry (the breadth multiplier for the north-star
      demo). Full design — the layering, RPC mapping, the "wrap providers
      not the orchestrator" subtlety, and the three hard parts honestly
      assessed (schema overlays; the new `provider_state` opaque-blob
      store as openctl's fifth persistence store, distinct from
      `applied_manifests`; and unknown/"known after apply" support
      contained to `refs` + `DryRun`) — is in
      [docs/plugin-architecture.md](docs/plugin-architecture.md).
      Sequence: ship the native external plugin protocol first with the
      TF host as an explicit second consumer, so the interface is shaped
      right. Precedent: Crossplane Upjet, Pulumi TF Bridge, Flux
      tf-controller. Target OpenTofu for the license story.
- [x] **Provider credential editing** — new ConfigService RPCs
      (ListProviders / UpsertProvider / DeleteProvider) that read/
      write ~/.openctl/config.yaml. UI Providers page with add /
      edit / delete forms. Scope covers the common one-context/one-
      credential-per-provider case; secrets never leave the server
      (has_secret bool + edit-with-blank-preserves semantics).
      Multi-context configs still editable by hand.
- [x] **Cancel of `running` ops** — the dispatcher wraps each op in a
      per-op cancelable context and registers a cancel func; CancelOperation
      aborts a running op's context (CancelRunning), which the op completes
      as `canceled` (Store.Complete now accepts the canceled terminal
      status; the completion write is detached from the canceled context so
      it still lands). Cancellation is cooperative — the op stops once its
      provider yields (proxmox threads ctx through its HTTP client; k3s
      checks ctx between install steps). The ops drawer's Cancel button now
      shows on running rows too.
- [ ] **Client-side CUE WASM validation** — faster editor diagnostics
      without a server roundtrip.
- [x] **Historical diff** — RepoService.GetResourceHistory +
      GetResourceAtCommit; Detail.svelte History card with a commit picker
      diffing a revision against the current desired manifest.
- [x] **UI for controller config** — new ConfigService
      GetControllerConfig / UpdateControllerConfig RPCs
      (`/v1/config/controller`) read/write the controller-behavior
      blocks of `config.yaml`: reconciler (enabled + interval) and a
      new `operations.retainPerResource` field (wired into
      `operations.New` via `resolveRetainPerResource`). UI "Settings"
      page (Providers-page-style form) with a persistent "restart
      required to apply" banner — every tunable is read once at
      startup, so there's no hot-reload. *Dispatcher concurrency* was
      intentionally NOT exposed: the dispatcher is a single-loop,
      one-op-per-resource design with no worker-count knob to tune.
      Follow-up if ever wanted: a config-watch/SIGHUP reload path so
      changes apply without a restart.
- [x] **Mobile-friendly layout** — app shell is now responsive: below 48rem
      the fixed 18rem sidebar collapses into a hamburger-toggled off-canvas
      drawer (backdrop + Esc to close; closes on navigation), the header
      wraps, and `main` goes full-width. Layout tokens (`--sidebar-width`,
      `--bp-mobile`) added to app.css. The side-by-side editor/preview panes
      (Edit form-view + TemplateWizard) now stack into a single column below
      48rem (preview un-stickied). The content tables (ResourceList, Providers,
      Detail) are wrapped in a `.table-scroll` container (app.css) so wide
      tables scroll within their region instead of the page; OpsDrawer is left
      as-is (its own vertical-scroll + sticky-header + ellipsized cells already
      contain it). The mobile-friendly layout is now functionally complete.
- [x] **Plugin-defined CLI subcommands** — generic protocol + CLI
      registration landed, and the k3s plugin ships `logs`/`restart`/`upgrade`
      handlers backed by the per-node agent client (`upgrade` is a
      sha256-verified binary swap).
      See DESIGN.md "Plugin-defined CLI subcommands."
- [x] **Default-timeout problem** — verified. The controller's
      submit-returns-immediately model means the global `--timeout` (300s,
      used for the fast gRPC submit + exec'd-plugin executors) is fine; the
      only mismatch was `ctl apply/delete`'s `--wait-timeout`, whose 5m
      default was shorter than a real cluster create (~10-15m) so the CLI
      reported "did not finish" while the op ran on fine server-side. Bumped
      the wait default to 30m (the op keeps running if it fires; poll with
      `openctl ctl op get`).

---

## Recently completed (housekeeping)

When phases or followups land, move them up out of "pending" into their
detail doc's marked-complete section, then leave a one-line entry here
with the commit hash for at-a-glance history. Trim to the last 10.

- feat: **cross-op dependency scheduling** (#74, design #70) — hoists the
  composite-apply DAG up to the dispatcher, **flag-gated default-off**
  (`OPENCTL_CROSS_OP_SCHEDULING`). On opt-in, `drainScheduled` claims the whole
  pending batch and runs it through `operations.RunGraph`: independent ops
  concurrent (`OPENCTL_CROSS_OP_CONCURRENCY`, default 4), dependent ops ordered
  by `$ref` edges (`crossOpEdges`, the op-level analog of `RefChildEdges`).
  Failure isolated (a failed op doesn't stop independents); `$ref` cycle falls
  back to unordered so nothing is left claimed-but-unrun. Default path is
  unchanged FIFO. Remaining: flip the default after homelab validation (where
  the reopened locked decisions need sign-off).
- test/docs: **provider contract hardening** (#67–#69, #71–#73) — a reusable
  `providertest.Suite` conformance battery encodes the `providers.Provider`
  contract once (Apply identity, Get-after-Apply round-trip,
  `*providers.NotFoundError` on missing Get, idempotent Delete, Delete-removes,
  List), with `Capabilities` flags for legitimate variations (`SupportsList`,
  `NoOpOnExisting`) and self-tests proving it fails violators. Bound to all
  three provider classes: external-plugin adapter (#68), Terraform host (#68),
  and compiled-in proxmox VM (#72). Binding proxmox surfaced and fixed two
  contract gaps: `Apply` returned a nil `Resource` (#71, now reads observed
  state back) and `applyVM` mutated an existing VM instead of the CONTROLLER.md
  no-op (#73, now no-ops + surfaces drift). The `$ref`/DAG model (DESIGN.md,
  #67) and provider contract (docs/plugin-protocol.md, #69) are documented.
  Follow-up: k3s Cluster is composite, out of the atomic battery's scope.
- feat: **dependency-DAG apply ordering** — composite Apply now orders its
  children with a real dependency graph + topological sort instead of
  hand-coded kind phases. New generic scheduler `operations.RunGraph`
  (topo order, cycle detection, bounded concurrency) driven by
  `operations.RefChildEdges`, which derives edges from the children's `$ref`s
  (a K3sNode depends on its VM and — for joiners — the first control plane).
  Two non-`$ref` constraints are added as explicit barrier edges: the interim
  state stub (after all VMs) and the CA-bundle aggregation (after all
  K3sNodes, gating agents). `applyClusterViaPlan` reduces to building tasks;
  ordering falls out of the graph. Serial by default (preserves SSH-install
  semantics + the existing dispatch-order test); `OPENCTL_APPLY_CONCURRENCY=N`
  opts into parallel provisioning of independent nodes. Graph is generic and
  reusable by any composite provider. Adds cycle detection the phase loops
  lacked. Tests: topo/cycle/unknown-dep/dup/error-propagation/parallelism +
  k3s first-CP-before-joiners.
- feat: **k3s node placement across Proxmox _endpoints_** — a k3s `Cluster`
  can now spread its VMs across separate Proxmox endpoints, not just hosts
  within one. Mechanism (Proxmox provider): the controller loads every
  configured proxmox context and `pmprovider.NewMulti` holds a client per
  endpoint; a VM's `spec.context` selects its endpoint, and reads by name
  resolve the owning endpoint via an index/full-scan. Policy (k3s): pools gain
  `context` and a general `targets: [{context, node}]` list; `PlacementTargets`
  stamps `spec.context`+`spec.node` onto each VM child, which rides the
  ChildDispatcher to the provider unchanged (routing spine untouched).
  Spreading the control plane over per-endpoint targets keeps etcd quorum when
  a whole Proxmox server dies. Scoped to endpoints sharing one L2 (single
  bridge / IP range / mutual reachability); separate-L2 spread (per-endpoint
  subnets, routable join) is the documented follow-on. CUE schema + README +
  tests; single-endpoint configs behave exactly as before.
- feat: **k3s node placement across Proxmox hosts** — a k3s `Cluster` can
  now spread its VMs across multiple provider hosts instead of piling every
  node onto one. `spec.compute.nodes` sets a cluster-wide host pool and
  `spec.nodes.controlPlane.nodes` / `spec.nodes.workers[].nodes` override it
  per pool; VMs are assigned round-robin within each pool (three CP replicas
  over three hosts land one each, keeping etcd quorum across failure domains).
  `resources.PlacementHosts` threads the chosen host onto each VirtualMachine's
  `spec.node` in both the `Cluster.Apply` and `Plan`/dispatcher paths; empty
  lists leave `spec.node` unset for the provider default (fully backward
  compatible). Covers only different physical nodes within one Proxmox
  endpoint — spanning separate Proxmox *endpoints* still needs per-pool
  context selection. CUE schema + README + tests.
- feat: **`provider_state` opaque store** — migration 0009 +
  `internal/controller/providerstate` (per-resource state/private/schema_version
  blobs, keyed like applied_manifests). The external adapter now round-trips
  state for plugins advertising `CapabilityState` (load-before/save-after each
  Apply/Get/Delete), so stateful external providers are fully supported. This
  is the controller-side prerequisite the Terraform host (Tier 1 item 2)
  reuses. Stateless plugins are unaffected.
- feat: **TF host provider adapter lifecycle** — explicit Kind → Terraform
  resource type mappings now satisfy the openctl `providers.Provider` contract:
  Apply runs `PlanResourceChange` + `ApplyResourceChange`, Get runs
  `ReadResource`, and Delete applies a null planned state while persisting
  opaque `DynamicValue` + private blobs in `provider_state`. Tests exercise the
  path against the in-repo `tf6server` fake provider and SQLite state store.
- feat: **TF host schema-to-CUE translation** — mapped Terraform resource
  schemas now generate standalone external CUE schemas and register during
  TF-host provider construction. Generated schemas validate desired `spec`
  fields, omit computed-only provider outputs from desired input, and surface
  through the same registry/SchemaService path as plugin-supplied schemas.
- feat: **Proxmox bootstrap install plumbing** — `openctl-controller install
  --target proxmox://context?...` now has a tested target parser/defaulting
  contract, creates an openctl controller VM through the existing Proxmox
  provider, waits for a static or guest-agent-reported IP, then reuses the
  SSH Linux installer for the controller deployment. Homelab validation still
  gates marking the roadmap item complete.
- fix: **Proxmox bootstrap install hardening** — template-based VM clones now
  pass `disks[0].storage` through to the Proxmox clone `storage` parameter, so
  `disk-storage=` actually controls the target storage for self-hosting
  installs. `--ssh-key ~/...` is also expanded consistently for SSH installs
  and the Proxmox handoff path.
- (#42–#45) — feat: **external plugin protocol (Tier 1 item 1)**, shipped
  in four phases. #42 `pkg/pluginproto` (persistent-process, id-correlated
  JSON-over-stdio protocol + Client + Handler SDK). #43 external provider
  adapter + registry/config `command:` wiring (capability-gated optional
  interfaces; only Planner needs a wrapper). #44 plugin-supplied CUE
  schemas threaded through validation + SchemaService. #45 the
  `plugins/example` reference provider (file-backed Note), a real-subprocess
  e2e test, and `docs/plugin-protocol.md`. Opaque state/private blobs are
  carried on the wire now so the Terraform host (item 2) needs no protocol
  change; their persistence store lands with item 2.
- `aa7b2a0` (#17) — fix: harden the proxmox provider: thread
  `context.Context` through the whole client (cancelable HTTP; polling
  loops honor `ctx.Done()`) and stop collapsing every lookup error to
  NotFound (new `client.ErrNotFound` sentinel; `applyVM` no longer clones
  a duplicate on a transient blip). Tests cover the sentinel split,
  context cancellation, and the apply not-found/transient branches.
- `e2af31a` (#16) — feat: serve the gateway over HTTP/2 (TLS, reusing the
  controller's self-signed cert) so browsers multiplex ~100 streams over
  one connection, ending the HTTP/1.1 ~6-conns/origin starvation class.
- `0cb047b` (#15) — build: sign macOS binaries with a stable self-signed
  `openctl-dev` identity so per-app firewalls (LuLu / Little Snitch) stop
  re-blocking every `make build`. No-op off macOS.
- `df967ea` (#14) — fix: bound consecutive Watch list-error retries and
  release the stream on a sustained provider outage (so a dead Proxmox
  no longer pins a browser connection + gateway stream open forever);
  adds a 5s TCP dial timeout to the Proxmox client.
- `30a14ab` — fix: resource Watch streams now tolerate transient
  provider List failures (for example Proxmox route flaps), log the
  outage, preserve the previous snapshot, and retry on the next poll
  instead of surfacing a fatal UI HTTP 500.
- `195fca4` — test: operations cache tests no longer rely on flaky
  submit races under the CI race detector; coverage still exercises
  verifying-trace cache disabled and refs-hash cache-miss behavior
  (follow-up to `cefc7b0`).
- `218f44a` — fix: packaged k3s agent binaries into controller
  install and taught Cluster apply to resume from a persisted
  Provisioning stub after a controller rebuild/restart.
- `975aee9` — fix: nil-safe K3sNode client close after SSH-drop
  reconnect failures.
- `02add4c` — fix: K3sNode install tolerates SSH disconnects caused
  by k3s.service startup reconfiguring networking; reconnects and
  verifies service health + node-token before succeeding.
- `db0d4b3` — fix: k3s install waits for cloud-init and runs
  curl-piped shell with pipefail so curl failures cannot be hidden.
