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

Nothing actively in progress. Phase U8 (post-U7 polish) is complete
— U8.1 through U8.21 all shipped. UI is now genuinely usable for
authoring, editing, deleting, and operating on VMs and Clusters
end-to-end.

## Suggested next order

Post-U8 sprint delivered: templates, kubeconfig/console, auto-
remediation, provider credential UI, two-way GitOps. Remaining
candidates for the next round:

- **Full Phase 8** — K3sNode as a first-class resource, Cluster as
  composer, wire-level `ResourceRef` primitives. Biggest single
  architectural lift; unblocks refs-cache and cross-resource DAG
  work. Broken into 5 concrete pieces in the phase section
  below; first PR is the ResourceRef primitive + resolver alone.
- **Multi-user auth** — OIDC + RBAC (from "Future goals").
- **User-authored CUE templates** — extend templates from Go-only
  compiled-in to loading `~/.openctl/templates/*.cue`. Feasible
  now that the RPC + UI plumbing exists.
- **Client-side CUE WASM validation** — faster editor diagnostics
  without a server roundtrip (from "Future goals").
- **Historical diff** — diff a resource against arbitrary commits
  in the manifest repo (from "Future goals").

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
The HTML doc is the long-form design; this section tracks what's been
delivered and what remains, and notes where the original plan has
evolved.

- [x] **Arch Phase 7** — Verifying-trace cache (per-resource v1: skip
      provider.Apply when manifest hash matches last success; calls
      provider.Get to populate result and marks op with a "cached"
      label). Parent-hash-aware (children's hashes folded into the
      parent hash) deferred until composite ops are reified.
- [~] **Arch Phase 8 (scoped)** — Owner-ref / children plumbing on the
      Resource proto, Registry.ChildrenOf + OwnerRefOf helpers, k3s
      Cluster implements ChildrenLister so Get/List/Watch return its
      VM children, child resources surface their owning Cluster via
      Metadata.OwnerRefs. Unblocks UI U3.3 deferred + U6.
- [~] **Arch Phase 8 (full)** — genuinely multi-session
      architectural lift. Steps 1–5 shipped as declarative +
      cache primitives; the remaining piece is a dispatcher
      refactor that consumes Cluster.Plan output as a DAG.
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
         version + extraArgs propagation. **Dispatcher is not wired
         to consume Plan output yet** — Cluster.Apply remains the
         operative path. A future refactor swaps Apply for a DAG
         over Plan children; that swap is the biggest remaining
         piece and needs a real test cluster to validate against.
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
- [ ] **Multi-user auth** — OIDC integration, named sessions, RBAC on
      `ResourceService`. Cookie/session layer from U1 is the
      foundation.
- [x] **Provider credential editing** — new ConfigService RPCs
      (ListProviders / UpsertProvider / DeleteProvider) that read/
      write ~/.openctl/config.yaml. UI Providers page with add /
      edit / delete forms. Scope covers the common one-context/one-
      credential-per-provider case; secrets never leave the server
      (has_secret bool + edit-with-blank-preserves semantics).
      Multi-context configs still editable by hand.
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

- `b377520` — fix: shutdown hang on Ctrl-C. Root ctx now cancels
  on SIGINT/SIGTERM (signal.NotifyContext) so subsystems exit;
  server.StopWithTimeout(3s) falls back to force Stop() when
  GracefulStop would otherwise wait indefinitely for UI Watch
  streams.
- `aff8431` — two-way GitOps: fsnotify watcher on manifest mirror;
  file edits become Apply ops (source="gitops"), loop-safe via
  content compare, opt-in via `manifests.gitops.enabled`.
- `d207b9e` — provider credential editing UI: ConfigService RPCs +
  Providers page with add/edit/delete forms; secrets never leave
  the server.
- `f6b3cb2` — opt-in auto-remediation via
  `openctl.io/autoReconcile: true` annotation; exponential backoff
  on repeated failure; ops tagged source="auto-reconcile".
- `615c639` — Cluster kubeconfig download + VM console URL via
  extended Actioner (ActionResult with message/url/download shapes).
- `e821bc9` — templates MVP: TemplateService + sidebar picker +
  wizard; two starters shipped (ubuntu-server-vm, small-k3s-cluster).
- `2d629c3` — fix: optional-composite form-clobber (scrubEmpty →
  reseedFormState loop) + version pill in header.
- `ba07e5b` — build: make build-controller depend on ui.
- `b377520` — fix: Ctrl-C shutdown hang on active UI Watch streams.
- `809c3fa` — U8.21: stacked layout for map-of-objects rendering.
- `0651d6a` — U8.20: manifest-preview toggle in form view.
- `0e9b693` — U8.19: Copy/Download YAML buttons on Detail.
- `b6e5642` — U8.18: pre-fill metadata.name with a suggestion
  (`vm-a3b2`, `cluster-x9k1`).
- `d488cdf` — U8.17: inline live-progress banner on Detail.
- `a3389de` — U8.16: list filter + click-to-sort headers.
- `39a1c5b` — U8.15: per-field validation error highlighting
  (path-attributed field errors, inline row rail).
- `e466ecd` — U8.14: delete from Detail with type-the-name
  confirmation.
- `aeee483` — U8.13: discriminated-union picker via CUE
  `@oneOf(group="X")` for VM image source.
- `d2e66d3` — U8.12: runtime actions — new providers.Actioner
  interface + ListActions/InvokeAction RPCs; VM start/stop/
  shutdown/reboot buttons on Detail.
- `a39fb61` — U8.11: provider-populated dropdowns via CUE
  `@options` attribute; VM.spec.node → ProxmoxNode.
