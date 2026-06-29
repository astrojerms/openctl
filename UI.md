# UI Implementation Plan

This document tracks the rollout of the openctl browser UI — the typed,
form-driven console that sits on top of the controller's gRPC API. Mark
deliverables as `[x]` as they land. Architectural decisions are pinned at
the top so future-self can trace any line of code back to the choice that
motivated it.

The UI is an independent track from the controller's architectural Phase 7+
work (verifying-trace cache, typed task IR, DAG scheduler). They can
interleave; the UI consumes whatever the controller reports and benefits
from the architectural work but doesn't block on it.

## Architectural decisions (locked)

These were settled via design walkthrough with the user before any code
went in. Any change requires re-opening the discussion.

### Source of truth

- **Controller SQLite is canonical.** `applied_manifests` and the
  resource state are the truth. The UI is a typed editor over that
  truth, not a separate source.
- **Manifests on disk are a materialized output, not an input.** The
  controller writes the desired manifest to a configured directory on
  every successful apply/delete. The UI and CLI both read this directory
  for "show me my config in git" purposes.
- **Git is one-way for v1.** controller → disk → git. Manual file edits
  don't trigger reapply. Two-way GitOps (file change → reconciler picks
  up) is a future goal.

### Editing model

- **Two affordances over one manifest.** Form view and CUE/YAML view.
  Toggle between them; the form view is disabled if hand-edits in the
  CUE pane can't be round-tripped through the form schema.
- **Same destructive gates as CLI.** `--allow-destructive` and
  `--i-know-this-breaks-the-cluster` surface as explicit checkboxes;
  Apply blocks until they're checked when the change requires them.
- **Composite resources:** the parent is editable; children are
  read-only, rendered as a tree underneath. Per-child drift/health
  badges aggregate into the parent badge.
- **Apply preview:** before submit, the UI shows what's about to happen
  (add VM-x, remove VM-y, leave others). Mirrors `terraform plan`.

### Stack

- **Frontend:** Vite + Svelte. Compact, reactive, manageable build.
- **Transport:** gRPC over the existing 9444 port for live streams
  (Watch), plus a `grpc-gateway`-generated REST/JSON gateway for
  request/response RPCs. Same protos, two surfaces.
- **Asset hosting:** built UI is embedded as `embed.FS` in
  `openctl-controller` and served from `/ui/*`. Single binary, single
  install command. `make ui` runs the Vite build into the embed dir.
- **Auth:** bearer token exchanged for an `HttpOnly`, `Secure`,
  `SameSite=Strict` session cookie. Cookie has the same authority as
  the bearer token. Single-user for v1; multi-user (named sessions,
  per-user tokens, RBAC) is future work — but the cookie/session layer
  is designed up front to leave room.

### Schema and form generation

- **CUE AST → form schema bridge.** A direct walk of the CUE AST
  produces the form schema. Faithful: disjunctions, constraints,
  defaults, and conditional fields all round-trip. Lossy intermediates
  (CUE → OpenAPI → form) are explicitly rejected.
- **Server-side validation for v1.** The UI calls `SchemaService.Validate`
  on debounced edits and renders errors as Monaco markers. The
  validation path is the same code the controller uses at Apply time.
- **Client-side WASM validation is a future enhancement.** Ships once
  edit latency becomes a real complaint; not before.

### Git sync

- **Materialize on apply success.** Dispatcher writes
  `<manifest-dir>/<apiVersion>/<kind>/<name>.yaml` after `applied_manifests`
  is updated. Removes the file on delete success.
- **Default location:** `~/.openctl/manifests/`. Configurable in
  `~/.openctl/config.yaml`.
- **Git is opt-in.** When enabled, the controller initializes the dir as
  a git repo on first start (or no-ops if already one), commits after
  every materialize/delete with a message like
  `apply VirtualMachine/foo via UI` or `... via CLI`.
- **Remote push is optional.** Config carries an optional remote URL and
  push cadence (e.g. `onCommit`, `every:5m`, `manual`). Push failures
  are logged but never block the apply.
- **UI surface:** a "Git status" indicator (clean/dirty/ahead-of-remote)
  and a manual "Push now" button.

### Out of scope for v1 (revisit later)

- Two-way GitOps (file edits don't trigger reapply).
- Multi-user auth (OIDC, RBAC, named sessions).
- Provider credential editing via UI (read-only; operator edits file +
  restarts controller).
- Cancel of `running` ops (only `pending` ops cancelable in U7).
- Client-side CUE WASM validation.
- UI for controller config (UI reads provider config from disk silently;
  surfaces deferred).
- Resource diff against arbitrary historical commits (only against
  currently-applied manifest).

---

## Phases

### Phase U1: UI backend prerequisites

**Status:** not started

**Goal:** make the existing gRPC API browser-reachable and add the
streaming/schema/asset surfaces the UI needs. Verifiable entirely via
`curl`/`grpcurl` — no frontend in this phase.

**Deliverables:**

- [ ] `Watch(stream WatchEvent)` RPC on `ResourceService`: streams
      add/modify/delete events plus drift updates. First-pass
      implementation polls Get/List internally; replace with notification
      hooks from the dispatcher later.
- [ ] `WatchOperations(stream OperationEvent)` RPC: streams op state
      transitions and substep updates. Filter by operation id, resource
      ref, or status.
- [ ] `SchemaService` proto with `ListSchemas`, `GetSchema(kind)`, and
      `Validate(manifest)` RPCs. Returns the embedded CUE schema text
      plus runs the same validation path the controller uses at Apply.
- [ ] `grpc-gateway` annotations on existing protos + a REST/JSON gateway
      mounted alongside gRPC. Same port via cmux, or sibling port —
      decide during implementation. Generates OpenAPI for free.
- [ ] `SessionService`: `Login(bearer_token)` → `Set-Cookie:
      openctl_session=...; HttpOnly; Secure; SameSite=Strict`. `Logout`
      revokes. Session storage in SQLite (`sessions` table) so it
      survives restart. Sessions carry an internal user id even though
      v1 only has one user — leaves room for multi-user without a schema
      migration.
- [ ] `embed.FS` of UI assets in `cmd/openctl-controller`, served from
      `/ui/*`. Returns a friendly "UI not built — run `make ui`" page
      when the embedded dir is empty.
- [ ] Tests: Watch streams emit when a sibling client applies; session
      cookie round-trips; SchemaService returns embedded CUE; REST
      gateway reaches all CRUD RPCs.

**Verifiable:** `grpcurl -plaintext localhost:9444 openctl.v1.ResourceService/Watch`
emits an event when another terminal does `openctl ctl apply -f vm.yaml`;
`curl --cookie-jar c.txt --data '{"token":"..."}' http://localhost:9444/v1/session`
returns a cookie that subsequent `curl --cookie c.txt /v1/resources`
requests use without re-auth.

---

### Phase U2: Manifest store on disk + git sync

**Status:** complete.

**Goal:** materialize the controller's desired state to disk so it's
visible to users outside the UI, with optional git tracking.

**Deliverables:**

- [x] `internal/controller/manifests/disk.go`: wraps the existing
      manifests Store with a "write to disk on commit" side-effect.
      Files go to `<manifest-dir>/<apiVersion>/<kind>/<name>.yaml`
      (apiVersion's `/` becomes a nested directory; names path-scrubbed
      so hostile inputs can't escape the root). Atomic write via temp +
      rename. Hook-point lets the git layer plug in without disk.go
      knowing about git.
- [x] Dispatcher integration: on apply success, write the manifest;
      on delete success, remove the file. On startup, reconcile disk
      against `applied_manifests` — missing-on-disk rows get
      re-materialized; orphan files (no applied_manifests row) are
      logged but never deleted (user may have committed them).
- [x] Config schema: `manifests: { dir, git: { enabled, branch,
      remote, pushMode, pushInterval } }` in `~/.openctl/config.yaml`.
- [x] Git integration (`internal/controller/manifests/git.go`):
      `git init -b <branch>` on first start when enabled (idempotent
      if already a repo), `git add -A` + `git commit -m "..."` after
      every materialize/delete with the structured message
      `apply <kind>/<name> via <source>`. Source ("CLI"/"UI") comes
      from gRPC metadata: the HTTP gateway middleware injects
      `x-openctl-source: ui` on every browser-proxied request; direct
      gRPC calls default to "CLI".
- [x] Optional remote push: `manifests.git.remote` plus `pushMode`
      (`onCommit` default when remote is set, `periodic`, `manual`).
      Periodic uses `pushInterval` parsed as a `time.Duration`. Push
      failures are logged but never bubble back into the dispatcher.
- [x] `RepoService` proto: `GetStatus` (enabled, dir, branch, head_sha,
      clean, dirty_paths, ahead/behind, push_mode), `Push`, `Pull`
      (advisory — does NOT trigger reapply in v1). Wired into both
      gRPC and HTTP gateway; UI consumes in U3.
- [x] Op source persisted on the `operations` row (migration
      `0007_op_source.sql`); dispatcher attaches it to context via
      `manifests.WithSource(ctx, op.Source)` so the git hook can use it
      after Save/Delete.
- [x] Tests: round-trip materialize/delete, atomic overwrite (no
      leftover .tmp files), path-traversal scrubbing, startup
      reconciliation behavior, commit-message formatting, source
      propagation via metadata, nothing-to-commit swallowed cleanly,
      RepoService.Push/Pull preconditions, ahead/behind reporting.

**Verifiable:** apply a VM via CLI, see `~/.openctl/manifests/proxmox.openctl.io/v1/VirtualMachine/foo.yaml`
appear with a matching git commit. Delete the VM, see the file gone and
a deletion commit. With remote configured, see the commit reach origin.

---

### Phase U3: UI shell + read-only views

**Status:** complete.

**Sub-phases:**

- [x] **U3.1** — Vite + Svelte + TypeScript project under `ui/`; Vite
      writes to `internal/controller/server/uiassets/dist/` (embedded by
      `//go:embed all:uiassets/dist`); `make ui` orchestrates install +
      build + `.gitkeep` restore; login screen exchanges the root bearer
      token for an HttpOnly session cookie via `POST /v1/session/login`;
      `WhoAmI` confirms the session on first load and post-login;
      Logout button + 401 handling routes back to login. Hand-rolled
      REST client over the grpc-gateway surface (60 LoC, no protoc dep);
      generated client deferred until U3.4 (streaming Watch RPCs) when
      typed schemas pay for themselves.
- [x] **U3.2** — Layout shell with header (logout) + left nav grouped
      by provider + main pane; tiny hash-based router (`#/k/<apiVersion>/
      <kind>[/<name>]`, no SPA framework); kind catalogue derived from
      `SchemaService.ListSchemas` with per-kind live resource counts
      (parallel `List` fan-out); per-kind resource list table with
      state/drift badges. `state` adapter centralises the
      proxmox-vs-k3s status-key inconsistency (`status.state` vs
      `status.phase`). Bottom ops drawer deferred to U3.4 alongside
      Watch wiring; detail pane deferred to U3.3.
- [x] **U3.3** — Resource detail pane: desired manifest (from
      applied_manifests), observed state (provider Get), drift diff
      table, last-applied timestamp. Proto extension: GetResponse now
      carries `applied` (Resource) + `applied_at` (RFC3339). Server
      loads from manifests.Store via new `LoadWithTime` helper that
      preserves the existing Load signature. Owner-ref + composite
      children tree deferred — needs proto-level relationship surface
      (planned alongside arch Phase 8 K3sNode work).
- [x] **U3.4** — Ops drawer + live Watch streams. Streaming bridge
      (`lib/stream.ts`) reads grpc-gateway's newline-delimited
      `{"result": <event>}` chunks over `fetch` + `ReadableStream`,
      cancellable via AbortSignal. Typed wrappers
      (`watchResources`, `watchOperations`) feed:
      ResourceList (initial `List` snapshot + Watch deltas merged in by
      name), Detail (single-resource Watch triggers re-Get so applied/
      drift refresh, not just observed), and a shell-wide ops store
      (`lib/ops.ts`) that drives a collapsible bottom drawer with the
      last 200 ops, in-flight count, and per-op status/error/resource
      links. Reconnect-with-backoff on transient errors; AbortError on
      route change is silenced.
- [x] **U3.5** — Git status indicator in the chrome
      (`components/GitStatus.svelte`): polls `RepoService.GetStatus`
      every 10s and renders a colour-coded pill (clean/dirty/behind);
      Push-now button shows when a remote is configured and there's
      something to push (or push_mode=manual). Catalogue counts now
      live-update via one Watch stream per kind — ADDED grows the
      count, DELETED shrinks it, MODIFIED is a no-op. Vitest harness
      (happy-dom env) with unit tests for the streaming bridge (chunk
      reassembly, error envelope, auth), the router (apiVersion
      splitting, encoding round-trips), and the status-badge format
      adapter. Playwright headless-Chrome e2e is explicitly deferred —
      ~200MB of browsers + non-trivial CI is too much weight for a
      homelab project; revisit if/when collaboration warrants it.

**Deliverables:**

- [x] Vite + Svelte project under `ui/` with embed pipeline. TypeScript
      on. Hand-rolled REST client for now; generated client revisited in
      U3.4.
- [x] Login screen: bearer token in → session cookie out. Logout button.
      Token never persisted in JS (cookie is HttpOnly); "signed in as
      &lt;userId&gt;" with session-id-on-hover replaces the planned
      last-4-chars hint since the JS layer can't see the cookie value.
- [x] Layout shell: left nav grouped by provider/kind with resource
      counts; main pane. Bottom drawer for op history deferred to U3.4
      (lands with Watch streams).
- [x] Resource list per kind: name, state badge, drift badge. Click →
      detail. Last-applied timestamp surfaces on the detail pane (not
      the list) via GetResponse.applied_at; adding it to List would
      require N joins on applied_manifests per row.
- [x] Resource detail (read-only): desired (applied) manifest, observed
      state, drift diff. Owner-ref + children tree deferred — see U3.3
      sub-phase note above.
- [x] Operations drawer: collapsible bottom pane fed by
      WatchOperations. Each row links to its resource detail. Substep
      checklist comes with U7.
- [x] Live updates throughout — ResourceList, Detail, ops drawer, AND
      catalogue counts all subscribe to Watch streams. The shell needs
      no refresh button.
- [x] Git status indicator + manual Push-now button (U3.5).
- [x] Tests: unit-level Vitest covers stream/router/format. Playwright
      headless-Chrome e2e deferred — see U3.5 sub-phase note for
      reasoning.

**Verifiable:** `make ui && openctl-controller serve` →
`http://localhost:9444/ui/` shows the console. Apply a VM via CLI; see
it appear without refresh. Take it down in Proxmox; see drift surface
in <2s.

---

### Phase U4: CUE / manifest editor

**Status:** complete.

**Goal:** "kubectl edit" in a browser, with live validation against the
real schema.

**Sub-phases:**

- [x] **U4.1** — `ResourceService.DryRunApply` RPC server-side. Optional
      `providers.DryRunner` interface lets composite providers expose
      per-child verbs (`create`/`destroy`/`respec`/`no-op`) and required
      destructive gates (`allow_destructive`, `i_know_this_breaks_the_cluster`).
      Atomic providers don't implement it; the handler still returns the
      spec-level diff against the persisted applied manifest. Schema
      validation errors surface inline in `validation_errors` (not as
      RPC errors) so the editor can mark them without a second roundtrip.
      k3s `Cluster` provider wired up — reuses `computeChangePlan` +
      `computeSpecRespecs` + `catastrophicReason` so the gates fire on
      the same conditions Apply would.
- [x] **U4.4** — Monaco diff view. Tab toggle in the edit pane
      ("Editor" / "Diff vs applied"). Diff is read-only and uses
      Monaco's `createDiffEditor` with the applied manifest on the left
      and the current edited text on the right. Tab is disabled when
      there are no unsaved changes and auto-snaps back to Editor view
      if the user reverts to baseline while on Diff. Shares the lazy
      Monaco bundle with U4.2 (no extra download cost). Closes UI
      Phase U4.
- [x] **U4.3** — Apply panel inline in the edit pane. One debounce
      fires Validate + DryRunApply in parallel; the preview pane lists
      the spec diff (current → will become), per-child verbs
      (create/destroy/respec/no-op with colour-coded pills), and the
      provider's summary. Required gates render as labelled checkboxes
      in an amber-bordered "destructive change" card; Apply stays
      disabled until every required gate is checked and there's a real
      change to make. On Apply, the request includes the gate flags
      and returns an op_id; we tail the existing shell-wide ops store
      (no double WatchOperations) and surface a coloured op card —
      pending/running/succeeded/failed/interrupted. On succeeded we
      navigate back to detail after a 600 ms green flash; on failed
      the apply card surfaces the error.
- [x] **U4.2** — Monaco editor wired into a new `/edit/...` route.
      Lazy-loaded — the editor + YAML language ship in their own chunks
      that only download on first /edit visit; list/detail/home stay
      light (~180 KB index). Pre-fills from `GetResponse.applied`
      (falls back to a skeleton built from observed state when no
      manifest is on file). 350 ms debounce on edits → YAML parse → if
      shaped right, `SchemaService.Validate` → errors surfaced as Monaco
      markers and listed in a diagnostics card below the editor.
      "Discard" reverts to the applied baseline; "Apply" is wired into
      the UI but disabled until U4.3 (with a tooltip pointing to it).
      Hash route gains `#/k/.../<name>/edit`. Detail pane gets an
      "Edit" button.

**Deliverables:**

- [x] Monaco editor integration. YAML mode for the editor surface
      (CUE doesn't ship a Monaco grammar; if/when manifests graduate to
      raw CUE, add a custom grammar).
- [x] Debounced server-side validation via `SchemaService.Validate`.
      Errors render as Monaco markers with hover detail.
- [x] Side-by-side diff vs. currently-applied manifest (Monaco's diff
      editor).
- [x] Apply panel: destructive/i-know checkboxes that surface
      conditionally based on what the change requires (UI calls a new
      `ResourceService.DryRunApply` RPC to learn which gates apply);
      submit → live op progress inline.
- [x] Cancel/discard reverts the editor to the applied manifest.
- [x] New `DryRunApply` RPC on the controller: runs the same change-plan
      logic as Apply (`computeChangePlan`, `catastrophicReason`)
      without actually applying. Returns the diff and the list of
      required gates. Used by both the editor and the form view.
- [ ] Tests: validation roundtrip latency budget, gate surfacing for
      no-op/scale-down/catastrophic cases, op progress streams through
      to the apply panel.

**Verifiable:** edit a VM's memory in the UI → apply → controller
errors with the same drift-on-atomic message the CLI surfaces.
Delete + re-create via the editor works end-to-end. Editing a Cluster
to scale workers down surfaces the `--allow-destructive` checkbox.

---

### Phase U5: Typed form editor

**Status:** in progress.

**Goal:** form-driven creation/editing for users who don't want to write
CUE. AWS-console-shape.

**Sub-phases:**

- [x] **U5.2** — Svelte form renderer with live YAML preview. The
      edit pane gets a three-way view toggle: Form / Editor / Diff.
      Form view loads the U5.1 Field tree via `schemas.getForm(av, kind)`,
      seeds state from the currently-applied manifest via
      `fromManifest`, and renders a recursive `FormField.svelte` that
      dispatches on `field.type`: text input (string), number input
      with min/max/step (int/number), checkbox (bool), recursive
      object/array, freeform textarea (any), greyed-out tile
      (unsupported). Array fields get add/remove row controls; const
      fields render read-only. Edits in the form drive the same `text`
      state the editor uses, so Validate + DryRunApply preview + Apply
      keep working unchanged. The right pane shows the live-generated
      YAML manifest. `scrubEmpty` drops empty optional fields from the
      generated manifest so the preview stays clean.
- [x] **U5.1** — Form schema Go package + `SchemaService.GetFormSchema`
      RPC. `internal/schema/form` walks a CUE value into a typed
      `Field` tree (strings/ints/numbers/bools/objects/arrays/any/
      unsupported) carrying optional+required, defaults, number bounds
      (>=, <=, >, <), const literals (pinned values like `apiVersion`),
      and CUE doc comments. RPC returns the tree as JSON-in-a-string
      so the proto stays non-recursive; the browser parses and renders.
      Same SchemaSelector the validator uses → form + Validate always
      agree on which CUE def to consult. 10 unit tests cover each
      construct, including a round-trip against the real shipped
      k3s `Cluster` schema. Unsupported constructs (free disjunctions,
      regex patterns, key-value maps) deferred to U5.3 — they emit a
      `{type:"unsupported", reason:"..."}` leaf the renderer can grey
      out.

**Deliverables:**

- [x] `internal/schema/form` package: walks the CUE AST and
      produces a typed form schema. Handles scalars, strings, numbers
      (with range constraints), bools, arrays (homogeneous), nested
      structs, optional fields, defaults, and const literals. Regex
      patterns + enums + free disjunctions deferred to U5.3 (emit
      `unsupported` leaves for now).
- [x] `SchemaService.GetFormSchema(kind)` RPC returns the form schema.
- [x] Form renderer in Svelte: recursive `FormField.svelte` with
      sensible defaults from CUE defaults. Stepped sections deferred
      to U5.3; current renderer is a single scrollable form.
- [x] Live preview pane: the manifest the form is currently generating,
      rendered as YAML, updates as you type.
- [x] Toggle between form view and CUE editor view (same underlying
      manifest, three-way: Form / Editor / Diff). Switching Editor →
      Form re-seeds state from the parsed YAML. Detecting
      non-roundtrippable hand-edits and disabling the toggle ships in
      U5.4.
- [ ] Review-before-apply screen showing the diff (via `DryRunApply`)
      and the same destructive gates as U4.
- [ ] Tests: form-schema bridge unit tests covering every CUE construct
      we use; round-trip tests (manifest → form state → manifest) for
      every shipped kind; e2e create-via-form for VirtualMachine and
      Cluster.

**Verifiable:** create a new VM entirely through the form, see it
apply, see it in the list. Edit a Cluster's worker count via the form,
hit apply with `--allow-destructive` checked, see workers drop.

---

### Phase U6: Composite resource UX

**Status:** not started

**Goal:** clusters feel like a first-class object, not "a parent and
some opaque children."

**Deliverables:**

- [ ] Cluster detail page: parent manifest (form or CUE) + children
      tree underneath, with per-child drift/health badges.
- [ ] Children are read-only, clickable to drill into their own
      (read-only) detail page. A "delete the owner instead" banner
      explains why edits aren't allowed.
- [ ] Aggregated badges: parent shows the sum of child drift; clicking
      expands which children are drifted.
- [ ] Apply preview shows per-child verbs: `+ create VM worker-3`,
      `- destroy VM worker-2`, `~ no-op on VM control-plane-1`.
- [ ] Tree → DAG view (later, gated on architectural Phase 7+): the
      same tree, rendered as a graph with dep edges. Becomes useful
      once `K3sNode` and per-step ops exist.
- [ ] Tests: parent edit doesn't accidentally write to children;
      aggregation logic for mixed drift states; preview matches the
      actual apply behavior.

**Verifiable:** edit a Cluster to scale workers 1→2 — preview shows
"add 1 worker VM"; apply; children tree updates live as the new VM
moves through `pending → running`.

---

### Phase U7: Op orchestration polish

**Status:** not started

**Goal:** apply-progress UX that doesn't make you guess.

**Deliverables:**

- [ ] Per-substep progress for composite operations. Requires
      **Phase 4.5** (parent-child op rows) — the UI renders the child
      op list as a checklist with live status per row.
- [ ] `CancelOperation` RPC on the controller. First-pass: only
      cancelable while `pending`. Running ops require cooperative
      cancellation in providers — design hook in, implement opportunistically.
- [ ] Retry / re-apply for `failed` and `interrupted` ops, with the
      original manifest pre-filled in the editor.
- [ ] Op history filtering by resource, status, time range, source
      (CLI vs UI).
- [ ] Tests: cancel-pending works and is idempotent; retry surfaces
      the right manifest; substep checklist renders correctly for a
      3-node cluster apply.

**Verifiable:** apply a 3-node cluster, see VM creates progress
one-by-one in the UI checklist. Kill the controller mid-apply, restart,
see op marked `interrupted` with a "retry" button. Cancel a pending op
before the dispatcher picks it up.

---

## Dependencies summary

| Phase | Hard prereq | Strong nice-to-have |
|-------|-------------|---------------------|
| U1 | — | — |
| U2 | U1 | — |
| U3 | U1, U2 | — |
| U4 | U3 | — |
| U5 | U3 | 5.x (count-up + spec changes) |
| U6 | U5 | 5.x, 4.5 |
| U7 | U3 | 4.5 (parent-child ops) |

## Suggested order (fastest to shippable UI)

`4.5 → 5.x → U1 → U2 → U3 → U4 → U5 → U6 → U7`, interleaving
architectural Phase 7+ between U3 and U4 if you want the IR/DAG
groundwork before the form/composite work.

## Followup work (post-U7)

Tracked here so they don't get lost; not blocking the core UI rollout.

- **Two-way GitOps**: file edits trigger reconciler reapply. Requires
  conflict resolution (UI edit vs file edit), watch on the manifest
  dir, and a "GitOps mode" toggle.
- **Multi-user auth**: OIDC integration, named sessions in the UI,
  RBAC on `ResourceService`. Cookie/session layer already in place.
- **Provider credential editing**: surface `~/.openctl/config.yaml`
  providers in the UI with secret-write affordances.
- **Cancel of running ops**: cooperative cancellation hooks in
  proxmox and k3s providers.
- **Client-side CUE WASM validation**: faster editor diagnostics.
- **Historical diff**: diff a resource against arbitrary commits in
  the manifest repo.
- **UI for controller config**: tunable retention, dispatcher
  concurrency, etc.
- **Mobile-friendly layout**: not a v1 concern but worth flagging.
