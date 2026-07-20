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

## Homelab completion (COMPLETE — 2026-07-12)

Backend-vs-vision gap audit for running the full homelab through openctl
(GPU box for a local model / Ollama, two k3s clusters, self-hosted sites,
a DriftlessAF-style GitOps loop, internal services like media / Home
Assistant, Synology storage). The workload/deploy mechanism (HelmRelease /
Manifest / Platform / Argo aggregation), two-cluster support, DNS + Traefik
ingress, the unified cross-layer graph, and 5-min drift detection **already
exist**. These are the remaining gaps, tracked as targets and checked off as
they ship.

### A. GPU / local-model (Ollama) enablement
- [x] **A1 — Proxmox VM PCI/GPU passthrough.** `VMSpec.hostPCI[]` (device or
      resource-mapping id, `pcie`, `primaryGPU`/x-vga, `rombar`, `mdev`,
      `romfile`) → `hostpciN`; `cpu.type` (`host`) → `cpu`. Lets openctl hand a
      GPU to a VM it creates. CUE schema + unit tests (emit + round-trip).
- [x] **A2 — OVMF/UEFI completeness.** `VMSpec.efiDisk` (storage, `4m`/`2m`,
      preEnrolledKeys) → allocates `efidisk0`, so a `q35` + `ovmf` passthrough
      VM actually boots. Schema + tests.
- [x] **A3 — k3s node GPU request.** Per-pool `gpu` on control-plane / worker
      specs (`devices[]`, `efiStorage`, `cpuType`). A shared
      `resources.GPUForNode` + `ApplyGPUToVMSpec` stamps q35 + OVMF + efidisk +
      cpu host + hostpci onto that pool's node VMs — wired into BOTH the
      operative `create.go` path and the Plan/graph mirror. CUE `#GPU` +
      unit tests (parse, resolve, stamp, per-pool isolation).
- [x] **A4 — GPU workload enablement.** `nvidiaDevicePlugin` added to the
      opt-in Platform components (NVIDIA k8s-device-plugin chart) so
      `nvidia.com/gpu` is advertised on GPU nodes and workloads (e.g. Ollama,
      as a plain HelmRelease) can request one. Schema + unit test.

**Area A COMPLETE (A1–A4):** openctl can now build a GPU box end-to-end —
passthrough VM hardware (A1/A2) → k3s GPU worker pool (A3) → GPU scheduling
via the device plugin (A4). Real-hardware validation on the homelab GPU host
is a follow-up (config layers are unit-tested).

### B. GitOps-from-git (the DriftlessAF loop)
- [x] **B1 — Git-as-source pull loop.** `Repo.StartPeriodicPull` pulls the
      remote (`--ff-only`) on an interval and, when HEAD advances, calls
      `Watcher.Sync` (new full-dir reconcile that applies every changed
      manifest, reusing the fsnotify apply path). Closes the "commit to git →
      openctl brings it up" loop. Config: `manifests.gitops.pull.{enabled,
      interval}` (requires a remote + the watcher). Apply-only — repo-wide
      prune is B2. Tests: `Watcher.Sync` (changed-only) + a real-git pull loop
      (bare remote → new commit → reconcile fires).
- [x] **B2 — Declarative desired-set + repo-wide prune.** `manifests.Pruner`
      (opt-in `manifests.gitops.pull.prune`): after a pull, deletes managed
      resources whose file left the repo — the repo becomes the desired SET.
      Heavily guarded (deletes real infra): skips composite children (owner
      labels + operative `k3s.openctl.io/cluster` label — deleting the parent
      cascades), skips resources whose latest apply was cli/ui-sourced
      (provenance from the ops table), only considers resources with no file on
      disk. Wired after `Watcher.Sync` in the pull loop. Unit tests cover every
      guard branch. Default OFF.
- [x] **B3 — GitHub push webhook.** `server.GitOpsWebhook` — a POST-only,
      HMAC-SHA256-verified (`X-Hub-Signature-256`) endpoint on the HTTP gateway
      that triggers an immediate pull+reconcile (shared `Repo.PullAndReconcile`
      path with the ticker), so a git push converges without waiting for the
      interval. Config: `manifests.gitops.pull.webhook.{enabled, secret, path}`.
      Threaded through the gateway like the OIDC handler. Unit tests: valid/bad/
      missing signature, non-POST, unsigned mode, route mounting. (PR-based
      *change proposal* left to Argo per the design; openctl reconciles the
      infra layer it owns.)

**Area B COMPLETE (B1–B3):** git is now a first-class *source*, not just a
sink — remote commits pull → reconcile (B1), removed files prune with strong
guards (B2), and a push webhook converges immediately (B3). The DriftlessAF
loop for the infra layer is in place. All opt-in; defaults unchanged.

### C. Storage
- [x] **C1 — NFS-backed storage.** `nfsProvisioner` added to the opt-in
      Platform components (nfs-subdir-external-provisioner chart): a dynamic,
      NFS-backed StorageClass so stateful workloads (media, Home Assistant) get
      persistent volumes on request from a Synology share
      (`values.nfs.{server,path}`). Schema + unit test. (Chose the dynamic
      provisioner over a static PV kind — more reusable, matches the A4 shape.)

### D. Loose ends
- [x] **D1 — action-output → secret bridge.** New `action` `$secret` provider
      (`secrets.ActionProvider`): resolves a secret by running a resource
      action and returning its output — e.g. the Cloudflare Tunnel run token
      via `{$secret: {provider: action, key: ".../Tunnel/<n>#get-token"}}` →
      cloudflared wires up with no manual token copying. General (any action,
      field selector download|message|url), rides the dispatcher's git-safety
      discipline (only the raw marker persists). Unit-tested with a fake
      invoker (no Cloudflare account needed). k8s cloudflared docs updated.
      (Live tunnel metal-validation is still a separate user task.)

**Area D COMPLETE. ENTIRE "Homelab completion" roadmap done (A1–A4, B1–B3,
C1, D1 — PRs #131–#138).** openctl can now: build a GPU box for a local model,
reconcile the homelab from a git repo (pull/prune/webhook), provision
Synology-backed storage, and auto-wire a Cloudflare Tunnel token. Remaining is
metal validation on real hardware (GPU passthrough, live tunnel) — a hands-on
homelab task, not code.

## Homelab workloads (ACTIVE — 2026-07-13)

Gap analysis for deploying the target workload set — a few blogs / public
sites, the Driftless GitOps architecture, MinIO, Longhorn, Infisical,
Authentik, Jellyfin, Open WebUI, Ollama. **Most are stock Helm charts and
deploy TODAY** on the primitives already shipped (k3s cluster, HelmRelease /
Manifest, NFS provisioner + k3s built-in local-path storage, GPU passthrough +
device plugin, Traefik + Cloudflare Tunnel/DNS with the `$ref` cnameTarget
wiring). These targets close the remaining gaps. Only **E1** is a genuine
prerequisite (Longhorn); the rest are convenience + robustness.

### E. Node customization
- [x] **E1 — cloud-init `packages` + `runcmd`.** The Proxmox VM `cloudInit`
      spec now takes `packages: [...string]` (installed on first boot with a
      preceding index refresh — `package_update: true`) and `runcmd: [...string]`
      (first-boot commands, after the qemu-guest-agent enablement). Rendered
      into a **per-VM cloud-init vendor snippet** (`client.RenderVendorData` via
      `yaml.Marshal` so arbitrary commands escape safely) uploaded to snippets
      storage and attached via `cicustom vendor=` — Proxmox has no native `ci*`
      option for arbitrary packages, and vendor-data is additive (doesn't clobber
      the generated ciuser/sshkeys user-data that a `user=` snippet would). The
      no-packages path keeps the shared static qemu-agent snippet **byte-
      identical** (zero risk to the metal-validated QGA flow); the combined path
      folds the agent runcmd in so IP discovery still works. Also added the
      previously-undocumented `packageUpgrade` to the CUE schema. Unblocks F1
      (Longhorn needs `open-iscsi`). Tests: render (packages/runcmd ordering,
      omitempty, special-char round-trip), spec parse, and a handler wiring test
      asserting the per-VM upload + cicustom. *Metal validation (does open-iscsi
      actually install) is a homelab follow-up, like A1–A4.*
- [x] **E2 — k3s node-pool node prep.** The k3s Cluster spec now takes a
      per-pool `nodePrep: {packages, runcmd}` block (on control-plane and each
      worker pool), stamped onto that pool's node VMs' `cloudInit` (which the
      Proxmox provider renders via E1). Mirrors the GPU-per-pool shape from A3
      exactly: `NodePrepSpec` + `parseNodePrepSpec` + `NodePrepForNode(i,cpCount)`
      + `ApplyNodePrepToVMSpec` (writes into the existing `cloudInit` map,
      preserving user/sshKeys/ipConfig), wired into **both** VM-build paths
      (operative `create.go` + Plan mirror `cluster_plan.go`) via the shared
      helper. `#NodePrep` CUE def on both pools. So a worker pool installs its
      own host deps — e.g. `open-iscsi` for a Longhorn storage pool. Tests:
      parse+resolve across the flat node ordering, apply-into-cloudInit
      (nil/empty no-op, preserves siblings), and an operative-path
      `GenerateDispatchRequests` test asserting only the target pool's VMs get
      the prereqs. **Makes F1 (Longhorn) usable.**

### F. Storage
- [ ] **F1 — Longhorn Platform component.** Add `longhorn` to the opt-in
      Platform set (replicated block storage, the tier NFS/local-path don't
      cover). Depends on E1 (open-iscsi on nodes); document the
      iscsi-installer DaemonSet as the interim fallback.

### G. Public exposure
- [ ] **G1 — expose-app convenience.** A composite (or documented pattern)
      that wires an in-cluster service to a public hostname in ONE place: the
      Cloudflare Tunnel ingress rule + the CNAME `DNSRecord` (`$ref`
      cnameTarget — already shipped #139). Removes the per-app two-resource
      dance for blogs, sites, Jellyfin, Open WebUI, Authentik, etc.

### H. Integrations (optional — deploying the app works without these)
- [ ] **H1 — Infisical `$secret` provider.** Register a self-hosted Infisical
      as a Tier-2 secrets backend (like Vault) so openctl *reads* secrets from
      it. Deploying Infisical itself is a plain HelmRelease.
- [x] **H2 — Authentik as openctl SSO.** Already works, no new code: Authentik
      is an OIDC IdP; point `auth.oidc` at it (OIDC backend shipped #110).

### I. GitOps
- [ ] **I1 — plan-on-PR (optional).** Surface a dry-run diff on a pull request
      before merge, for a PR-gated Driftless workflow (openctl reconciles on
      merge/webhook today — B1–B3; PR-preview was deferred to Argo). Enhancement.

### J. App catalog / examples
- [ ] **J1 — example manifests** for the target apps under `examples/`
      (Ollama + Open WebUI on GPU, Jellyfin on NFS, MinIO, Authentik-as-SSO, a
      static blog behind the tunnel) — the deploy-today path, documented
      end-to-end. Not a code gap; lowers the activation energy.

## Architecture & consolidation (2026-07-13)

Health, debt, and direction items from an architecture review — worth banking
**before** piling on more workload features (E–J). The code is cohesive today
(features slot into stable seams — one `protocol.Resource` behind a uniform
`Provider`, `$ref`/`$secret` resolution centralized in the dispatcher), but
these are the corners where drift would start if it starts anywhere.

- [ ] **K1 — Unify the two k3s VM-build paths.** `pkg/k3s/cluster/create.go`
      `GenerateDispatchRequests` (the operative create path) and
      `internal/controller/providers/k3s/cluster_plan.go` `buildVMManifest`
      (the Plan/graph mirror) produce structurally-identical VM manifests but
      must be kept in sync **by hand** — a documented "keep these identical"
      that already bit the GPU-pool work (A3 edited both + factored a shared
      helper). Switch the operative create onto the Plan path (the long-intended
      unification) so there's one source of VM shape. **Known unpaid debt.**

- [ ] **K2 — One GitOps source-of-truth model.** git-as-source (B1–B3) was
      layered on top of git-as-sink (disk mirror + push), leaving overlapping
      machinery — disk mirror, push, pull, watcher, prune, drift reconciler,
      webhook — with subtle interactions (startup reconcile *re-materializes*
      files that prune wants gone; `--ff-only` pull fights auto-commit-on-apply).
      Collapse into **two explicit, non-overlapping modes**: **mirror** (SQLite
      is truth, git is a read-only audit mirror — no pull/prune) and **gitops**
      (the repo is truth — pull/apply/prune; the controller does NOT auto-commit
      desired specs, and startup does NOT revive missing files). One config
      switch; only the machinery for the chosen mode runs. Write the invariants
      down first (short design doc), then reconcile the code to them. Pairs with
      K3 (gitops mode needs reliable `source=git`).

- [x] **K3 — Make provenance explicit.** `applied_manifests` now carries a
      first-class `source` column (migration 0011). The dispatcher already
      attaches the op's source to the context before the manifest sink runs
      (`manifests.WithSource`); the store persists that value on every apply
      (`SaveWithRefsHash` reads `SourceFromContext`), and `ListAll` surfaces it
      as `Ref.Source`. The B2 Pruner's hand-managed guard reads the column
      directly — the fragile ops-table `SourceLookup` reconstruction (and its
      main.go closure) is **retired**, so provenance survives op GC. Empty
      source ("unknown", e.g. pre-migration rows) is not protected, preserving
      the prior behavior. Tests: source round-trip + in-place update
      (`TestSaveRecordsSourceFromContext`), all prune guards driven by the
      column (`TestPrunerGuards`), child guard independent of source. **Unblocks
      K2** (gitops mode needs reliable `source=git`).

- [ ] **K4 — Generic Platform + deployment methods as providers.** The intent
      for `Platform` is NOT a curated grab-bag of preferred charts but **generic
      support**, Terraform-provider style: (a) make Platform components
      data-declared (any chart via `{name, chart, namespace, values}`), with
      traefik/cloudflared/nvidia/nfs/… as optional named **presets** or examples
      — not baked-in opinion; (b) treat the deployment **method** itself as a
      provider — Helm/Manifest/Argo are already kinds; add new methods
      (Kustomize, Flux, …) as new kinds/providers rather than bespoke code.
      openctl already has the provider bones (it's TF-like for infra); extend
      the same shape to workload deployment so breadth comes from providers, not
      special cases.

- [ ] **K5 — Re-baseline the scope wedge (`direction.md`).** The doc originally
      vetoed workloads/PaaS ("stop at cluster-Ready"); the deployment model +
      homelab workloads crossed that line deliberately (infra-IaC → homelab
      PaaS). Decide + document on record which openctl is — infra IaC
      (Terraform-like) with workloads as a **bounded** convenience, or a homelab
      PaaS — so the next N features check against a rule instead of drifting
      feature-by-feature. Frames K4.

- [x] **K6 — Providers declare their status/outputs.** `status` was fully open
      (`status?: _`), so `$ref` targets like `status.outputs.kubeconfigPath`
      weren't discoverable or validatable. Kinds now declare an open, all-
      optional `status`/`outputs` block in their CUE schema (k3s Cluster,
      proxmox VM, cloudflare Tunnel), `schema.OutputsFor(apiVersion, kind)`
      enumerates them structured (path/type/doc), and `openctl explain
      <apiVersion> <kind>` prints "what you can `$ref`." Descriptive-only
      (never rejects a manifest or the provider's output). Flows through
      `GetSchema`/`GetFormSchema` to the UI for free. Follow-on: author-time
      `$ref` field-path validation against the declared status (reuse the same
      declaration). Think "typed status for a controller," not "Terraform
      outputs."

- [ ] **K7 — Expose the scheduling DAG / plan-preview.** Today only a *rooted*
      composition graph is exposed (`GetChildrenGraph` → UI DagView: a resource's
      `owns`/`ref` edges outward). The **operation-scheduling DAG** the dispatcher
      builds from `$ref` edges — the actual apply *order* and *why* — is internal
      and never surfaced. And `DryRunApply` previews one resource's diff, not a
      whole submission's ordering. Add a batch **plan-preview**: given a set of
      manifests (or a repo dir), return the dependency DAG + the topological
      apply order (+ per-resource dry-run diff), so an operator can see "what
      will happen, in what order, and what waits on what" before applying —
      including for a GitOps sync. Pairs with I1 (plan-on-PR: the same preview,
      rendered on a PR).

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
      per the `Provider` interface doc. (2) — `applyVM`'s update behavior on an
      existing VM was first narrowed to a no-op (#73) and then, by product
      decision, reinstated as a **scoped in-place resize**: re-applying updates
      memory, CPU (cores/sockets), and disk **growth** in place (no recreate),
      rejects disk shrink, and leaves non-resizable fields (template, networks,
      cloud-init) to surface as drift. CONTROLLER.md updated accordingly; the
      conformance binding is now `NoOpOnExisting=false` (an identical re-apply is
      still effectively a no-op since Proxmox no-ops on unchanged config). **Composite k3s Cluster — resolved:** a
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
      parked epic, scoped in
      [docs/k3s-separate-l2-spread.md](docs/k3s-separate-l2-spread.md)
      (routed-VLAN slice first; the networking gap, not orchestration).
      **Per-context networking landed and reachable** (#91 + follow-on):
      `network.perContext` (per-endpoint bridge + static-IP range) validates
      (CUE), parses (`ParseClusterSpec`), allocates each node's IP from its
      placement context's range (`AllocateIPs`, fail-fast on a missing block),
      and stamps the per-context bridge on each VM (`BridgeForContext` in
      `buildVMManifest`). Single-L2 path byte-identical. So a routed-VLAN
      cluster's VMs now land on the right subnet+bridge. **Remaining:** the
      pod-overlay/join layer for cross-subnet — `--node-external-ip` +
      (if needed) `wireguard-native` flannel via K3sNode `extraArgs`, and
      `routableIP` for non-routed separate sites; these are k3s-semantic details
      that need two-VLAN homelab validation to get right (server-vs-agent flag
      placement, vxlan-over-routed vs wireguard).
- [x] **Composite-apply dependency DAG** (#66). Ordering within a single
      composite `Apply` is now a real dependency graph
      (`operations.RunGraph`: topological execution + cycle detection),
      replacing hand-coded phase loops. Edges derive from `$ref`s between
      children (`RefChildEdges`) plus explicit barrier edges (CA bundle).
      Serial by default; `OPENCTL_APPLY_CONCURRENCY=N` runs independent
      children in parallel. See `DESIGN.md` §"Dependencies, Value-Passing
      & Ordering".
- [x] **Cross-op dependency scheduling** — implemented and **on by default**
      (2026-07-06). The dispatcher claims the whole pending batch and runs it
      through `operations.RunGraph`: independent ops run concurrently
      (`OPENCTL_CROSS_OP_CONCURRENCY`, default 4), dependent ops are ordered by
      their `$ref` edges (`crossOpEdges`, the op-level analog of
      `RefChildEdges`). Failure is isolated (a failed op doesn't stop
      independents; a dependent whose predecessor failed fails at ref
      resolution); a `$ref` cycle falls back to unordered scheduling so no op is
      left claimed-but-unrun. The two reopened locked decisions were verified
      *preserved, not loosened* before the flip: same-resource fail-fast is
      enforced at `Store.Submit` (a batch never holds two ops for one resource),
      and concurrent provider `Apply` on distinct resources was already the
      default path via the intra-composite DAG. `OPENCTL_CROSS_OP_SCHEDULING=0`
      is the escape hatch back to FIFO. Verified under `-race`. Design +
      verification: [docs/cross-op-scheduling.md](docs/cross-op-scheduling.md).

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
- [x] Proxmox bootstrap install (`openctl-controller install --target
      proxmox://homelab`). Target parsing, bootstrap VM manifest generation,
      VM create/poll through the Proxmox provider, then handoff to the SSH
      Linux installer. **Metal-validated on the homelab (2026-07-07):** with a
      static IP, `install --target proxmox://homelab?...&ip=…` created the VM,
      applied the static IP, waited for SSH, then deployed the cross-built
      Linux controller — the systemd `openctl-controller.service` came up
      **active** with :9444 (gRPC) + :9445 (UI) listening. Prereq:
      `make build-controller-linux` (the installer expects the pre-built
      `bin/openctl-controller-linux-amd64`, it doesn't cross-compile on the
      fly). **Known limitation found during validation:** DHCP mode
      (`ip` unset) times out on IP discovery because it relies on the
      qemu-guest-agent, and the QGA-install `cicustom` snippet silently
      no-ops when the disk storage is `local-lvm` (LVM storages can't hold
      `snippets` content). Use a static `ip=…` or a snippet-capable storage
      until the QGA-install path is hardened (follow-up below).
- [~] **Bootstrap QGA robustness (follow-up).** `EnsureQemuAgentSnippet`
      needs a `snippets`-capable storage; on a `local-lvm`-only node it
      no-ops, so DHCP-mode bootstrap can't discover the VM's IP. *Done:* the
      DHCP IP-wait timeout now fails with an **actionable** error naming the
      likely cause (qemu-guest-agent not running) and the static-IP
      workaround, instead of a bare timeout. *Remaining (the real fix):*
      install qemu-guest-agent without depending on the disk storage — pick a
      snippets-capable storage automatically (the proxmox client now has
      `ListNodeStorages`), or fail fast at create time when no such storage
      exists rather than after a 10-minute IP-wait.
- [x] **Bug: Cluster apply doesn't detect an out-of-band child-VM
      deletion — FIXED.** Surfaced 2026-07-07 recovering a k3s worker whose
      VM had been deleted outside openctl: Cluster `Get` reported `Ready`
      off its stale `children` list, and the verifying-trace cache further
      short-circuited re-applies. Two coordinated fixes: (1) `applyObserved
      Counts` now verifies each child VM actually exists on the provider
      (`vmChildExists` via the VM provider's Get — a definitive `NotFound`
      drops the child from the count so drift surfaces; a transient error is
      treated as "exists" to avoid fabricating drift); (2) the dispatcher
      **skips the verifying-trace cache for composite (`Planner`) providers**
      — a composite's convergence depends on children that drift
      independently of the manifest hash, so an input-hash cache can't
      capture it; a Cluster re-apply now always reconciles (its apply-existing
      path is idempotent, so a converged cluster is a fast no-op). Tests:
      `TestGetDetectsDeletedChildVM`, `TestVerifyingCacheSkippedForComposite`.
- [x] **Bug: Plan-based count-up breaks on legacy (pre-`K3sNode`)
      clusters — FIXED.** Existing-cluster converge goes through the Plan
      path, which pointed each new worker's join at `$ref(K3sNode/<cp>)`. A
      cluster created via the old inline path has no `K3sNode` resource for
      its CPs, so ref resolution failed (`K3sNode "dev-cp-0" not found`) and
      the worker VM was created but never joined. *Fix:* `setJoin` now
      checks whether the surviving CP has a `K3sNode` resource — if so it
      keeps the `$ref` (Plan/K3sNode clusters); if not (legacy), it resolves
      the join token + IP directly (CP IP from cluster-state endpoints, token
      via SSH `cat .../node-token` behind a testable `readCPNodeToken` seam)
      and sets **concrete** `joinFrom`/`joinURLFrom` values, which
      `applyK3sNode` already accepts as bare strings. Both the count-up and
      respec Plan paths use it. Tests: `TestSetJoin_UsesRefWhenK3sNodeExists`,
      `TestSetJoin_ConcreteForLegacyCP`.
- [x] Plugin-defined CLI subcommands (`openctl k3s logs/restart/upgrade`).
      Generic protocol + CLI registration shipped: plugins advertise typed
      subcommands in capabilities, and the CLI dispatches them with
      positional/flag values in `Request.Args`. The k3s plugin advertises and
      implements `logs` (fetch a node's k3s journal), `restart` (restart k3s
      on a node), and `upgrade` (binary-swap a node to a target k3s version:
      the agent downloads + sha256-verifies the release, atomically swaps the
      binary, and restarts). All run against the per-node agent client.
      Cluster-wide rolling upgrade (drain/cordon ordering) — **implemented and
      reachable** as the k3s Cluster `upgrade` action (no-drain, per
      [docs/k3s-rolling-upgrade.md](docs/k3s-rolling-upgrade.md)). Landed in
      four slices: orchestration core (#87, `cluster_upgrade.go` —
      CPs-serial-then-workers, idempotent-skip, health-gated halt); parameterized
      actions plumbing (#88, `InvokeAction` gains `parameters` +
      `ParameterizedActioner`); production agent upgrader (#89,
      `agentNodeUpgrader` over mTLS, unit-tested vs a fake agent); and the wiring
      (enumerate nodes from cluster state + cert bundle → `DoActionWithParams
      ("upgrade", {version})`). Invoke: `InvokeAction Cluster/<name> upgrade
      version=v1.30.5+k3s1`. Idempotent-skip is active — a version pre-pass
      queries each node so a re-run after a partial/halted upgrade skips nodes
      already at the target. **Remaining:** multi-node homelab validation (real
      k3s download + pod continuity — the one hardware gate); `--drain` stays an
      opt-in follow-on (needs a k8s client); UI action-param input is a small
      frontend follow-up.
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

### Phase U10 — UI exposure gaps (backend features not reachable from the UI)

Punch list from a backend-RPC-vs-UI-usage audit (2026-07-07). The UI
covers nearly every RPC, so these are *capability*- and *feature*-level
gaps — shipped backend behavior a browser user can't reach — not missing
screens. Ordered by impact.

- [x] **U10.1 — Parameterized actions.** `ResourceService.InvokeAction`
      already carries a `parameters` map, but `ListActions` returned bare
      action-name strings with no parameter schema, so the Detail action
      bar could only fire zero-arg actions — leaving the **shipped k3s
      Cluster rolling upgrade** (`upgrade version=…`) unreachable from the
      UI. Fixed: `ListActionsResponse` now carries `action_specs`
      (`ActionSpec` + `ActionParameterSpec` — name/type/required/
      description/default) via a new optional `providers.ActionDescriber`
      interface (providers implementing only `Actioner` get name-only
      specs synthesized, so proxmox etc. are unchanged). The k3s provider
      declares `upgrade`'s required `version` param; the Detail action bar
      renders an inline input form for parameterized actions and passes the
      values through `invokeAction`. Backward-compatible (the `actions`
      name list is still populated).
- [x] **U10.2 — Node ops in the UI.** The k3s per-node `logs` and
      `restart` operations (previously CLI-only) are now Cluster-level
      **parameterized actions** on the in-process k3s provider, reusing the
      rolling-upgrade machinery: `enumerateUpgradeNodes` + the cluster cert
      bundle + `agentNodeUpgrader` (extended with `Logs`/`Restart`) drive a
      node's openctl-k3s-agent over mTLS. `logs` (params: `node` optional,
      `lines` optional) returns the journal as a download; `restart` (param:
      `node` required) restarts k3s. They surface automatically in the
      Cluster Detail action bar via U10.1's `action_specs` param form — the
      only UI change was nicer button labels + a destructive-confirm on
      restart. (`upgrade` was already reachable from U10.1.)
- [x] **U10.3 — Historical / filtered operation browsing.** The ops
      drawer's live store is capped at the most recent 200, so its
      status/source filters only reached that tail. Added a **"Load older"**
      control that queries `OperationService.ListOperations` (status/source
      pushed server-side) and merges the results into the drawer (deduped by
      id, live row preferred, newest-first), so operations beyond the live
      window are browsable. `ListOperations` was previously wired in
      `api.ts` but never called.
- [x] **U10.4 — RBAC visibility & gating.** The shell now shows the
      caller's role as a badge (amber for a read-only `viewer`), reading
      `WhoAmIResponse.role` (the server already populated it; only the TS
      type was missing the field). A `canMutate` derived store (`viewer` →
      false; every other role and the unknown/`--no-auth` case → true, since
      the server is the real gate) drives gating: Detail hides Edit/Delete
      and disables actions/Reconcile, Edit disables Apply, ResourceList
      hides "+ New" — each with a "read-only session" tooltip. *Deferred:* a
      user-management UI (no `UserService` RPC exists yet; `users.yaml` is
      hand-edited).
- [x] **U10.5 — `repo:pull` button.** The git-status header now has a
      **Pull** button (shown whenever a remote is configured, highlighted
      amber when the local is behind) alongside Push, calling the
      previously-unused `RepoService.Pull` wrapper — how an operator picks
      up out-of-band commits (another controller, a manual push to the
      remote).
- [x] **U10.6 — Advanced/composite-child kinds in the create picker.**
      `K3sNode` and `AgentInstall` (composite children a k3s `Cluster` fans
      out into) now carry an "adv" chip in the nav, and their create form
      shows an info banner explaining they're normally produced by a
      `Cluster` (AgentInstall in particular requires an existing Cluster's
      CA bundle) with a "Create a Cluster instead →" link.
  - [x] **U10.6a — Advanced flag is now backend-derived (was a hardcoded UI
        map).** The old `ADVANCED_KINDS` map in `catalogue.ts` is gone.
        `SchemaInfo` gained `advanced` / `owner_kind` / `advanced_note`, stamped
        by `SchemaService.ListSchemas` from a new optional provider capability
        `providers.AdvancedKindDescriber` (a provider declares which of its OWN
        kinds are composite-children + owner + note). The k3s provider declares
        `K3sNode` + `AgentInstall` → `Cluster`; `VirtualMachine` stays unflagged
        because it's proxmox's first-class kind, not a k3s child. External
        plugins carry it too: `pluginproto.KindInfo` gained `ownerKind` /
        `advancedNote`, forwarded by the external adapter's `AdvancedKinds()`.
        The UI (`Nav.svelte` chip, `Edit.svelte` banner) reads the flag off the
        catalogue entry and derives the "Create a &lt;owner&gt; instead" link
        from the child's own apiVersion, so any plugin composite is flagged with
        no client-side list.
  - [x] **U10.6b — Reference example for a plugin composite-child.**
        `plugins/example` gained a `Notebook` composite (a Planner that expands
        into one `Note` child per page, with owner labels) and declares `Note`
        as an advanced composite-child of `Notebook` via the handshake
        (`OwnerKind` / `AdvancedNote`). The external e2e test asserts the
        declaration survives the real subprocess wire round-trip into
        `AdvancedKindDescriber`. `docs/plugin-protocol.md` documents the field.
- [x] **U10.7 — Small polish.** Raw CUE viewer — a "Schema" toggle on each
      kind's ResourceList lazy-fetches `SchemaService.GetSchema` (previously
      never called). Kubeconfig download verified through the existing
      download-action path. **Dependent/cascading form dropdowns** shipped:
      the `@options` CUE convention gained `field` (a dotted path into the
      target resource, e.g. `status.storages`) + `dependsOn` (the sibling
      field whose value selects the instance, e.g. `spec.node`); the proxmox
      client lists a node's storages/bridges and a single-node `Get` enriches
      `ProxmoxNode.status.{storages,bridges}`; the form resolves a VM's disk
      `storage` / network `bridge` from the selected node (free-text fallback
      until a node is picked). *Deferred:* VM console (websocket modality —
      its own feature, different modality).

---

## Future goals (parked)

Cross-cutting items that don't belong to a single track. Promote into a
phase plan when ready to commit.

- [~] **Variables & secrets** — [docs/vars-secrets-design.md](docs/vars-secrets-design.md).
      **(A) secrets — Tier 1 SHIPPED.** A `$secret` spec-level marker
      (`base.#Secret` CUE helper) resolved at apply-time and **redacted from
      everything persisted** — the operations store, `applied_manifests`, the
      on-disk mirror, and git all keep the marker; only `provider.Apply` sees
      the value. Sources are a pluggable `SecretProvider` registry
      (`internal/controller/secrets`): v1 registers built-in `file`
      (`<state-dir>/secrets/<name>`, 0600) and `env`. The security-critical
      ordering: secrets resolve *after* the cache check + `refs_hash`, so a
      low-entropy secret's plaintext never enters a hashed/persisted column.
      VM `cloudInit.password` is annotated `@secret`; the form renders a
      secret-reference control (source + key) instead of a plaintext box.
      Bare plaintext still validates (back-compat). **Tier 2 — Vault
      SHIPPED.** A `secrets.providers` config block registers configured
      backends; a `vault` type resolves `{$secret: {provider: "vault",
      key: "secret/data/db#password"}}` over Vault's KV HTTP API
      (`internal/controller/secrets/vault.go` — dependency-free net/http, KV
      v1 + v2, `X-Vault-Token`/namespace, token via `tokenSecretFile`).
      Registration is pure (same resolver + redaction, no wire-shape change),
      proving the extensibility thesis. *Remaining:* other Tier 2 backends
      (cloud secret managers) + **Tier 3** external secret-provider plugins
      over `pluginproto` — same registration seam.
      **(B) parameterization — SHIPPED.** `openctl ctl apply -f vm.cue
      --values prod.cue` unifies the manifest with one or more CUE values
      files (repeatable flag), the `-var-file` analog:
      **(B) parameterization — SHIPPED.** `openctl ctl apply -f vm.cue
      --values prod.cue` unifies the manifest with one or more CUE values
      files (repeatable flag), the `-var-file` analog:
      `manifest.LoadCUEWithValues` builds each file as a `cue.Value` and
      unifies — a concrete value fills an abstract manifest field, overrides
      a default, and a conflict fails loudly (not last-writer-wins); an
      unfilled abstract field fails the concreteness check. YAML manifests
      are unchanged (`--values` on a YAML manifest is a usage error). Example:
      `examples/vm-parameterized.cue` + `examples/vm-values.cue`.
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
      - *OIDC — SHIPPED (backend):* external IdP → claims → role, the last big
        auth slice. Design: [docs/oidc-design.md](docs/oidc-design.md). OIDC is
        a new session-minting front door (Authorization Code + PKCE, discovery,
        claims→role **deny-by-default**) that reuses the shipped session/cookie/
        RBAC machinery — it only adds an identity source. Config
        `auth.oidc` block (`clientSecretFile`, `roleClaim`+`roleMapping`,
        `defaultRole`); `internal/controller/auth/oidc.go` (go-oidc discovery +
        JWKS verify + highest-role mapping) + `internal/controller/server/
        oidc_http.go` (`/auth/oidc/login` + `/auth/oidc/callback`, state+PKCE
        cookies, mints a session and sets the `openctl_session` cookie).
        Coexists with root token + named tokens (`--no-auth` unchanged). Tested
        against a **fake IdP** (`oidc_test.go` — real go-oidc verify: happy
        path, highest-role-wins, deny-by-default, wrong-audience + expired
        rejection). **UI "Log in with SSO" button SHIPPED:** the login page
        probes an unauthenticated `/auth/oidc/enabled` (only mounted when OIDC
        is configured) and, when present, renders a "Log in with SSO" button
        linking to `/auth/oidc/login`. *Remaining:* validation against a real
        IdP (the one step that needs an actual provider).
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
- [x] **Cloudflare provider (native)** — `plugins/cloudflare`, a native
      pluginproto external provider (stdlib-only, hand-rolled REST v4
      client) managing `cloudflare.openctl.io/v1` **DNSRecord** and
      **Tunnel**. Stateful via `CapabilityState`: Cloudflare-assigned
      record/tunnel IDs round-trip through the `provider_state` store, so
      openctl owns create→update→delete by ID (out-of-band deletes are
      recreated on apply). The tunnel **run token is a `get-token` action**,
      not status — it never lands in the git-synced state mirror (mirrors
      the k3s kubeconfig-as-action precedent). Ships CUE schemas (validated
      through the real external-schema path in tests) + full fake-API unit
      lifecycle tests + a subprocess handshake e2e. Chosen over wrapping the
      Terraform Cloudflare provider through `tfhost` (at the time tfhost
      couldn't yet host a real framework provider — see the tfhost hardening
      item below, which has since closed that gap); the native plugin ships
      working Cloudflare today and validates the "wide, infra-only" thesis.
      *Next:* validate against a real Cloudflare account; consider Zone
      (observed) and WAF/ruleset kinds.
- [x] **tfhost hardening — real providers (protocol 5 + 6, msgpack, nested
      blocks).** The four gaps that blocked hosting real `terraform-provider-*`
      binaries are closed. **msgpack state decoding** + **nested-block/typed
      config encoding** now go through the *public* `tfprotov6` value codec
      (`NewDynamicValue` / `DynamicValue.Unmarshal`) against a schema-derived
      `tftypes` type (values.go) — no deprecated API, full fidelity (the old
      slice sent JSON and couldn't decode msgpack or encode nested blocks).
      **Protocol 5** is negotiated alongside 6 via go-plugin `VersionedPlugins`;
      a protocol-5 (SDKv2) provider is driven through `v5adapter` (client_v5.go)
      converting the field-identical v5⇄v6 messages, so the adapter stays on
      tfplugin6 types (stubs vendored in `pkg/tfplugin5`). **Real-binary e2e:**
      `plugins/tf-fake` upgraded to a nested-block, msgpack-emitting framework
      provider (protocol 6), plus a gated test driving the *published*
      `hashicorp/time` provider (protocol 5) end-to-end —
      create→read→delete `time_static`, decoding its computed msgpack outputs.
      This makes tfhost able to host real SDKv2 and framework providers
      (incl. the Terraform Cloudflare provider). *Next:* UpgradeResourceState
      wiring; real-provider validation in CI (currently gated/local).
- [ ] **Workload deployment + unified cross-layer graph** *(design:
      [docs/deployment-model.md](docs/deployment-model.md))* — grow openctl from
      an infra control plane into a *unified* one that also manages/sees Layer 1
      workloads. A **purpose-built** `plugins/k8s` provider (Helm Go SDK +
      client-go — the Helm SDK is the per-release reconcile engine, so it's not
      "reimplement Argo") deploys `HelmRelease`/`Manifest` to any cluster
      (openctl `Cluster` by `$ref`, or an external kubeconfig); charts from HTTP
      + OCI; values via `$secret`. **Argo** is bootstrapped + read/aggregated
      (create-optional) for pure apps. An **opt-in `Platform` composite**
      (nothing on by default) natively runs the infra-coupled layer —
      **Traefik** ingress + **cloudflared** (token wired from the `Tunnel`) —
      drawing the native/GitOps seam at *infra coupling*. Payoff: the UI graph
      extends up into workloads and down to metal + edge (pod → cluster → VM →
      host; workload → Tunnel → DNSRecord) — the slice a cluster-scoped tool
      like ArgoCD structurally can't show. Phased 1–6.
  - [x] **Phase 1 — `plugins/k8s` + `HelmRelease` (the engine).** Native
        pluginproto plugin driving the Helm Go SDK (`helm upgrade --install` /
        get / list / uninstall) against an explicit kubeconfig; **HTTP + OCI**
        charts; `$secret` values; wait-for-ready. Nearly stateless (Helm keeps
        release state in-cluster). Tested hermetically (Helm memory driver +
        fake kube client + a local chart) and **e2e against a live k3d
        cluster** (published podinfo from HTTP *and* OCI: apply→get→upgrade→
        delete→NotFound, gated on `KUBECONFIG_E2E`).
  - [x] **Phase 2 — cross-layer credential resolution.** A `HelmRelease`
        targets an openctl-managed cluster by resolving its kubeconfig from the
        k3s Cluster's `status.outputs.kubeconfigPath` via openctl's existing
        `$ref` marker (no new controller machinery — `$ref` already resolves a
        nested status field of another resource before the provider runs, and
        DAG-orders the release after the cluster). The plugin reads the resolved
        path and stores only the **path** (not the kubeconfig bytes) for
        Get/Delete — improving on Phase 1's stored-content posture. Inline
        `spec.kubeconfig` (external clusters) still supported. Verified: plugin
        unit (path read + re-read), **e2e vs k3d** via the `kubeconfigPath`
        route, and a root-module refs test resolving a HelmRelease's `$ref` →
        Cluster nested `status.outputs.kubeconfigPath`. (Also corrected the
        design doc: the real marker is `$ref`, not the `valueFrom`/`spec.cluster`
        shorthand.)
  - [x] **Phase 3 — `Manifest` kind (server-side apply).** Applies raw
        Kubernetes YAML (multi-doc) via a client-go dynamic client + discovery
        RESTMapper, server-side-apply with field manager `openctl-k8s`. Tracks
        applied objects in state (GVR+ns+name) so it **prunes** objects that
        leave the manifest on a later apply and cleans up on delete; Get reports
        how many tracked objects are live (Ready/Degraded). Credential fields
        mirror HelmRelease (`kubeconfigPath` `$ref` or inline `kubeconfig`),
        factored into a shared `kubeconfigState`. Verified: unit (multi-doc
        parse, prune diff) + **e2e vs k3d** (SSA two ConfigMaps → get → prune one
        (confirmed deleted) → delete). This is the escape hatch for non-Helm glue
        (Namespaces, ConfigMaps) and, later, Argo `Application` CRs.
  - [x] **Phase 4 — opt-in `Platform` composite.** A curated, infra-coupled
        platform layer that fans out into one Helm release per **enabled**
        component (nothing on by default): **Traefik** ingress + **cloudflared**.
        Since an external plugin's `Plan` children aren't auto-applied by the
        controller (`ChildDispatcherFrom` is k3s-only), `Apply` installs the
        component releases directly (reusing the Helm engine); the provider
        advertises `CapabilityPlan` so the UI graph shows the fan-out (children
        with owner labels). Disabling a component uninstalls it (prune); Get
        aggregates component health (Ready/Degraded). The cloudflared run token
        flows as a `$secret` in that component's values — resolved before Apply
        so Helm gets the real token, but only the raw marker persists (no leak);
        it's user-provided since openctl has no action-output→secret bridge yet.
        Verified: unit (fan-out, owner labels, prune diff) + **e2e vs k3d**
        (enable Traefik → real release installed → disable → pruned
        (confirmed gone) → delete).
  - [x] **Phase 5 — Argo bootstrap + aggregation.** Bootstrap: `argocd` is now a
        `Platform` component (argo-cd chart). Aggregation: a read-only
        `ArgoApplications` kind (cluster ref + optional namespace, default
        `argocd`) whose Apply/Get read the live Argo `Application` CRs via the
        dynamic client and summarize each app's name + health + sync into status
        (Ready/Degraded); Delete is a no-op. This sidesteps the global-`List`
        problem — aggregation is a named per-cluster resource. Creating
        Applications declaratively is already covered by the `Manifest` kind, so
        this stays read-only (create-optional, read-baseline as designed).
        Verified: unit (summarize, observed phases, argocd component) + **e2e vs
        k3d** (register the Application CRD + a sample Application with
        health/sync → read it back through the provider).
  - [x] **Phase 6 — unified cross-layer graph.** The UI graph renderer
        (`DagView.svelte`) is fully kind-agnostic, so the work was entirely
        backend: `GetChildrenGraph` now (a) collects `$ref` edges from the
        **root's own spec** (previously only child specs) and (b) walks the
        composition + `$ref` graph **breadth-first with a visited set + depth
        cap** (previously depth-1). So querying a `HelmRelease` spans
        HelmRelease → (kubeconfigPath `$ref`) → Cluster → (Plan) → K3sNodes/VMs
        in one graph — the cross-layer view a cluster-scoped tool (ArgoCD)
        structurally can't show — and the existing UI renders it with **zero
        frontend changes**. Verified: unit (multi-level span with root-`$ref` +
        recursion into the Cluster's Planner; existing depth-1 tests still pass).
        **Deployment model Phases 1–6 all shipped.**
  - [x] **Cross-layer graph follow-ons — both shipped.**
    - **VM→host placement edge** (#129). A k3s `VirtualMachine` pins itself to a
      host via a plain `spec.node` string; `GetChildrenGraph` now synthesizes a
      `VM → ProxmoxNode` edge from it, completing the workload → Cluster →
      K3sNode → VM → **host** spine. Graph-only, not a `$ref` (a `$ref` would
      couple VM Apply to the observed-only ProxmoxNode being fetchable); the
      host is a terminal node so it doesn't drag in every other guest on the box.
    - **Upward-into-workloads** (`ChildrenOf` on `plugins/k8s`). A `HelmRelease`
      / `Manifest` now hangs its in-cluster objects (Deployments/Services/…)
      under it in the graph. Required one backward-compatible protocol change:
      `pluginproto.RefParams` gained a `State` field so the external adapter can
      thread a stateful plugin's kubeconfig into the children query; HelmRelease
      enumerates via the in-cluster release manifest (`helm get manifest`),
      Manifest from its persisted object refs. Verified: unit (manifest→refs
      mapping, adapter state-threading) + e2e-vs-k3d (podinfo → real
      Deployment/Service). Pods-per-Deployment (needs a live owner-ref walk) is
      a possible future; the release's declared objects are the honest baseline.
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
