# openctl — Direction & Priorities

This is the product direction the roadmap serves. Reaffirmed 2026-11-27:
**an AWS-style console and declarative infrastructure manager for homelabs,
without user-managed state files or whole-lab planning.** A working provider
API is a building block, not evidence that the operator journey is complete.
When a proposed feature conflicts with this direction, revisit it deliberately.

Companion doc: [plugin-architecture.md](plugin-architecture.md) — the provider
contract and long-tail Terraform/OpenTofu adapter. Breadth serves the operator
journey; it does not replace it.

---

## The wedge (identity)

> **Independent, per-resource declarative infra — every resource
> deploys, drifts, and reconciles on its own, with no monolithic plan
> and no shared state file — across any provider, with a real UI.**

The complaints that motivate openctl (HCL, learning provider syntax,
painful state, "add one VM and now I'm hunting drift across a giant
state file") are symptoms. The root differentiator is
**per-resource independence**: you can apply/inspect/reconcile one
resource without re-planning the world.

Crucially, this is **already built**, not aspirational. openctl's
architecture is per-resource by construction:

- each resource is its own dispatcher op,
- its own `applied_manifests` row,
- its own `input_hash` / `refs_hash` verifying-trace cache entry,
- linked to others by `$ref` pointers, **not** a monolithic dependency
  graph or a single `terraform.tfstate`.

**The test every future feature must pass:** *does it preserve
independent per-resource deployment?* Anything that reintroduces a
global plan or shared state file fails the wedge, no matter how
convenient.

## Audience & ambition

Homelab operators, starting with a single operator on Proxmox. Adoption by
others is a goal: connecting an existing lab must not require recreating it,
reading implementation code, or manually repairing controller state.

The product is a control plane, not Terraform with a GUI. The console, CLI,
and declarations operate through the same resource ownership and operation
contracts. Kubernetes is an optional deployment target, never a prerequisite.

## Scope decisions

- **Homelab depth before provider breadth.** Make Proxmox discovery, adoption,
  safe updates, application deployment, health, and recovery work together
  before adding more providers. Keep the provider contract extensible, but
  registry coverage is not the primary success metric.
- **A bounded homelab PaaS** (re-baselined — K5, 2026-07). The original
  line was "infra, not workloads — stop at cluster-Ready, kubectl takes it
  from here." The deployment model (HelmRelease / Manifest / Platform /
  Kustomize) and the homelab-workloads epics (GPU box, storage, node prep,
  tunnel exposure) crossed that line **deliberately**, and this is now the
  ratified scope: openctl **is** a homelab PaaS — infra *and* the workloads
  a homelab runs on it, wired together by `$ref`/`$secret`. The bound is
  **"what a home lab actually needs"** ("AWS console for homelab"), NOT a
  general cloud PaaS competing with Heroku/Cloud Foundry. Deployment methods
  are providers (Helm/Manifest/Kustomize/Argo as kinds), so breadth stays
  in the plugin ecosystem rather than bespoke platform code. The per-resource
  wedge still holds — no global plan/state — so this is PaaS-by-composition,
  not a monolithic control plane.
- **Run anywhere.** Workstation (dev convenience) → portable Linux
  daemon → in-cluster. Deliberately avoid Crossplane's chicken-and-egg
  (needing a k8s cluster to run the thing that manages your infra).
  When openctl runs *locally* (e.g. a Mac) it does **not** manage its
  own host; when deployed *into* supported infra (e.g. Proxmox) it may
  manage — and even self-deploy — its own box.
- **Multi-user eventually.** Single-operator today; real multi-user /
  RBAC is a genuine future need, gated behind adoption + a shared
  deployment actually existing.

## North-star operator journey

1. Install a controller on a workstation, Linux host, or dedicated Proxmox VM.
2. Connect Proxmox, test read access, discover existing infrastructure, and
   adopt a VM without changing it.
3. Provision a dedicated GPU VM or select a managed one; deploy Jellyfin with
   read-only NAS media and protected account onboarding.
4. Optionally publish a domain through an explicitly approved, connected
   tunnel. Public exposure is never an implicit deployment side effect.
5. Inspect dependencies, desired versus observed state, operation errors, and
   independent guest/container/HTTP/media/GPU/public readiness checks.
6. Update or recover a resource without replanning unrelated infrastructure.
   Release tracking and delete infrastructure are distinct operations.

Every step must have reproducible desired manifests. A running VM is not proof
of application health; a passed fake-provider test is not hardware validation.

---

## Priority order

### Tier 1 — trustworthy resource contracts

- Create, update, and no-op previews are explicit; reviewed intent is exactly
  what Apply submits. Form editing must preserve metadata and secret markers.
- A cache hit requires live observable agreement, not just unchanged input.
- Operation success requires durable desired-state bookkeeping. If provider
  mutation succeeds but persistence fails, report partial success explicitly.
- Bind VM operations to immutable placement and identity. Fail closed on
  ambiguous names, unreadable ownership, or unsafe dependent deletion.
- One desired-state authority: controller-owned `mirror`, or upstream-owned
  `gitops`. Never combine automatic output commits with input pulls.

### Tier 2 — a complete homelab workflow

- Connect/test/discover/adopt in the console, with the same controller-side
  inventory available to the CLI.
- A provider-independent control-plane overview and saved-inventory navigation;
  cosmetic counts never trigger whole-lab provider/SSH scans or imply readiness.
- First-class standalone application lifecycle, including updates, logs,
  explicit runtime actions, health, and data-preserving removal.
- One integrated Jellyfin journey, including storage, GPU, optional domain,
  and failure recovery. Prepared reviews now resume explicitly with durable
  request recovery, fresh consent and reverified dependencies; no manual
  guest-script staging.
- Linux/SSH/Proxmox installation and accurate onboarding documentation.

### Tier 3 — proof, then breadth

- Verify the full operator journey on real hardware, including interrupted
  operations, guest reboots, unavailable dependencies, and actual playback.
- Offline controller backup/restore now preserves selected configuration/state
  roots, including identities, opaque provider state and credentials. Practice
  recovery before depending on automation; review retained absolute paths and
  queued operations before starting a restored controller. External credential
  dependencies and guest/NAS data require separate backups. Manifest git history
  is not a complete controller/data backup.
- Widen providers and multi-user features when the existing homelab workflow
  is dependable. Terraform hosting is a long-tail adapter, not the product UX.
- General-purpose cloud PaaS remains out of scope.

### Cross-cutting, ongoing

- Test every capability against the wedge ("did we just sneak in a
  global plan/state?").
- Give the **provider contract** stability + versioning discipline
  *before* the ecosystem widens — once others write plugins and multiple
  users share a deployment, the interface and state schema become
  load-bearing and hard to change. The `NotFound`-sentinel bug (#17) is
  a preview of how a loose contract bites.

---

## Standing tensions (walk in eyes-open)

1. **The TF host partially reimports the complexity we're fleeing.**
   Hosting Terraform providers reintroduces their schemas ("provider
   syntax") and requires a state store — the two things the wedge
   pushes against. Mitigation is real but partial: openctl's CUE/form
   layer *hides* the schema, and the per-resource model *avoids* the
   monolithic-state pain. Net answer is the **hybrid**: clean native
   providers (and/or hand-authored schema overlays) for the handful used
   constantly; the TF host for the long tail of breadth.
2. **Wide + multi-user raises the stakes on the contract and the state
   model.** See the cross-cutting note above — harden before widening.

## State model note

The current **single-daemon SQLite** model (`~/.openctl/controller/
state.db` + the disk mirror + per-provider YAML state) is **correct for
this horizon** — don't over-engineer toward distributed/HA storage.
It only comes under real pressure when "runs in k8s, many users" is
live, which is Tier 2/3. Build breadth and reach on the model we have.
The one addition the TF host forces is a new **`provider_state`** store
(opaque per-resource blobs) — see plugin-architecture.md.
