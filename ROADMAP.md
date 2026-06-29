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

Status legend: `[x]` done, `[~]` in progress, `[ ]` not started,
`[d]` deferred / parked.

---

## In flight

UI Phase U5 (typed form editor) underway. U5.1–U5.3 shipped — CUE →
form-schema bridge, Svelte form renderer + live YAML preview + 3-way
toggle, advanced field types (enums, regex patterns, key/value maps).
Next sub-phase is U5.4 (form ↔ CUE round-trip detection; disable Form
view when manifest has hand-edits the form can't represent).

## Suggested next order

`4.5 → 5.x → arch Phase 7 → U1 → U2 → U3 → U4 → U5 → U6 → U7`

Tracks can interleave; the architectural Phase 7+ doesn't gate the UI
phases, and 4.5 / 5.x meaningfully improve the UI's UX. See
[UI.md](UI.md) § Dependencies summary for the hard prereqs.

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

### Followups (post-Phase-6, parked)

- [ ] External plugin protocol (3rd-party providers as exec'd binaries
      over the existing JSON-over-stdio protocol).
- [ ] Linux install via SSH (`openctl-controller install --target
      ssh://user@host`).
- [ ] Proxmox bootstrap install (`openctl-controller install --target
      proxmox://homelab`).
- [ ] Plugin-defined CLI subcommands (`openctl k3s logs/restart/upgrade`).
- [ ] Bug fix: `pkg/proxmox/handler/handler.go:114` collapses any
      `GetVM` error to NotFound — network timeouts produce false "VM
      gone" results.

---

## Target architecture — docs/target-architecture.html

Speculative roadmap from the BSALC / Crossplane / BuildKit discussion.
Sketch only; no committed deliverables yet. Phases as written in the
HTML doc:

- [x] **Arch Phase 7** — Verifying-trace cache (per-resource v1: skip
      provider.Apply when manifest hash matches last success; calls
      provider.Get to populate result and marks op with a "cached"
      label). Parent-hash-aware (children's hashes folded into the
      parent hash) deferred until composite ops are reified in arch
      Phase 9-10.
- [ ] **Arch Phase 8** — K3sNode as first-class resource; Cluster
      becomes a composer that emits typed children.
- [ ] **Arch Phase 9** — Typed task IR (frontend/IR/executor split,
      BuildKit-LLB-shape). Frontends: YAML, CUE, programmatic Go.
- [ ] **Arch Phase 10** — Full DAG scheduler with parallel execution
      of independent nodes, content-addressed cache for pure
      sub-operations (cert bundles, cloud-init, IP allocation).

Open design questions captured in the HTML doc; revisit before
committing to a phase.

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
            composite children tree deferred (needs proto relationship
            surface, lands with arch Phase 8).
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
      - [ ] **U3.4** — ops drawer + live Watch streams.
      - [ ] **U3.5** — git status indicator + Push now + e2e tests.
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
      - [ ] **U4.4** — Side-by-side diff view (Monaco diff editor).
- [~] **Phase U5** — Typed form editor (CUE-AST → form-schema bridge,
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
      - [ ] **U5.4** — Form ↔ CUE round-trip + disable form when manifest
            has hand-edits the form can't represent.
- [ ] **Phase U6** — Composite resource UX (Cluster parent + read-only
      children tree, aggregated badges, per-child verbs in apply
      preview, DAG view gated on arch Phase 8).
- [ ] **Phase U7** — Op orchestration polish (live substep checklist,
      `CancelOperation` RPC, retry/re-apply, history filtering).

---

## Future goals (parked)

Cross-cutting items that don't belong to a single track. Promote into a
phase plan when ready to commit.

- [ ] **Two-way GitOps** — file edits in the manifest dir trigger
      reconciler reapply. Needs conflict resolution between UI edits
      and file edits, `inotify`/`fsnotify` watch, and a "GitOps mode"
      toggle. Big enough to be its own track.
- [ ] **Multi-user auth** — OIDC integration, named sessions, RBAC on
      `ResourceService`. Cookie/session layer from U1 is the
      foundation.
- [ ] **Provider credential editing** — surface
      `~/.openctl/config.yaml` providers in the UI with secret-write
      affordances. Currently read-only.
- [ ] **Cancel of `running` ops** — cooperative cancellation hooks in
      proxmox and k3s providers. U7 only does `pending` cancel.
- [ ] **Client-side CUE WASM validation** — faster editor diagnostics
      without a server roundtrip.
- [ ] **Historical diff** — diff a resource against arbitrary commits
      in the manifest repo.
- [ ] **UI for controller config** — tunable retention, dispatcher
      concurrency, etc.
- [ ] **Mobile-friendly layout** — not v1 but worth flagging.
- [ ] **Plugin-defined CLI subcommands** — deferred from agent work,
      see DESIGN.md "TODO: Plugin-defined CLI subcommands."
- [ ] **Default-timeout problem** — controller's submit-returns-immediately
      model mostly fixes this, but verify the CLI's defaults match the
      new shape.

---

## Recently completed (housekeeping)

When phases or followups land, move them up out of "pending" into their
detail doc's marked-complete section, then leave a one-line entry here
with the commit hash for at-a-glance history. Trim to the last 10.

- `30c8036` — Phase 6: macOS LaunchAgent install/uninstall.
- `98636a7` — Phase 5: drift surfacing + declarative scale-down.
- `9db1144` — Phase 4: k3s Cluster provider compiled in.
- `bc24de0` — Phase 3: async operations with persistence.
- `2101107` — Phase 2: proxmox VirtualMachine provider.
- `383cae5` — Phase 1: scaffold persistent reconciler.
- `f19aefd` — k3s per-node agent for out-of-band cluster operations.
- `aa0f235` — CUE configuration support (Phase 1 of CUE rollout).
