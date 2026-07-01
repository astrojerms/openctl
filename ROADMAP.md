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

Post-U7 UI polish track (**Phase U8**, see below) actively shipping —
schema depth, form ergonomics, and a Create flow. Managed-only filter
+ periodic reconciler landed on the controller side. Everything the
official phase plan calls "done" is still done; U8 is the punch list
that surfaced from actually using the UI to author resources.

## Suggested next order

U8 punch list (see "UI post-U7 polish"), then any of: full arch Phase
8 (K3sNode as first-class resource + Cluster.Plan refactor + wire-
level refs), arch Phase 9 (verifying-trace rebuilder), arch Phase 10
(continuous reconcile), or a runtime-actions track (start/stop/console
buttons on VM detail, kubeconfig download on Cluster detail) which
would close the biggest "AWS-console-for-homelab" gap identified in
U8.

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
- [~] **Arch Phase 8 (scoped)** — Owner-ref / children plumbing on the
      Resource proto, Registry.ChildrenOf + OwnerRefOf helpers, k3s
      Cluster implements ChildrenLister so Get/List/Watch return its
      VM children, child resources surface their owning Cluster via
      Metadata.OwnerRefs. Unblocks UI U3.3 deferred + U6. The full
      Phase 8 (K3sNode as first-class resource + AgentInstall +
      Cluster.Plan refactor + wire-level refs) remains deferred — a
      separate architectural lift to be planned when the UI flushes
      out demand for it.
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
      yet). DAG view remains gated on a future arch Phase 8 lift
      (target-architecture HTML).
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
- [ ] **U8.17** — Live progress on the detail page. Ops drawer tails
      globally; detail doesn't inline the current op for that
      resource.
- [ ] **U8.18** — Better create defaults. `metadata.name` placeholder
      like `vm-&lt;random&gt;` beats an empty required field.
- [ ] **U8.19** — Copy/download YAML on detail.
- [ ] **U8.20** — Manifest-preview toggle in the form view (some
      users won't want it eating a third of the pane).
- [ ] **U8.21** — Map-of-objects rendering (`cloudInit.ipConfig`).
      Works but the key input aligns awkwardly with the middle of
      the nested object subtree.

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

- `a39fb61` — U8.11: provider-populated dropdowns via CUE
  `@options` attribute; VM.spec.node → ProxmoxNode.
- `5da3e02` — U8.10 fix: optional composites now open properly
  (empty `{}`/`[]` no longer collapse back to `+` button).
- `c64a09a` — U8.9: vertical form-row layout + wider form pane.
- `4db4927` — U8.8: create polish — collapsed optional sections +
  name-collision check.
- `86bf57e` — U8.7: Create flow via /new route reusing Edit.svelte.
- `2cc8a18` — U8.6: k3s Cluster schema expansion + `#NodeSize`.
- `527c13b` — U8.5: per-disk Proxmox flag knobs (ssd/discard/
  iothread/backup/cache).
- `cb61619` — U8.4: VirtualMachine schema expansion (docs, defaults,
  enums, network/cloudInit extras).
- `35820d3` — U8.3: periodic drift reconciler + manual Reconcile.
- `e7b8605` — U8.2: managed-only filter on Get/List/Watch.
- `2f59e2c` — U8.1: ProxmoxNode as first-class observed-only kind.
