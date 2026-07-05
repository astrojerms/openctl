# openctl — Direction & Priorities

This doc captures the strategic direction that came out of a
direction-setting conversation (2026-07-04). It is the north star the
roadmap serves: audience, the differentiating "wedge," scope decisions,
and the priority ordering the next epics should follow. When a proposed
feature and this doc disagree, this doc is the tiebreaker — or a reason
to revisit it deliberately.

Companion doc: [plugin-architecture.md](plugin-architecture.md) — the
technical spine (one provider interface, many implementers, incl. the
Terraform/OpenTofu host) that most of Tier 1 below flows through.

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

A **general-purpose tool the author also uses** — built to manage real
infra, for learning, and as a portfolio piece; adoption by others is an
explicit goal, not just a nice-to-have. That raises the bar on polish,
docs, provider-contract stability, and (eventually) multi-user.

Time is **not** the limiting factor (steady spare-time investment), so
the architecturally ambitious path is on the table — and doubles as the
best portfolio story.

## Scope decisions

- **Wide, not deep.** Favor a generic, reusable plugin ecosystem that
  can be extended to *any* infra provider over gold-plating Proxmox +
  k3s. openctl should lay down an interoperable substrate; providers
  plug into it.
- **Infra, not workloads.** Explicitly **not** a workload/app platform
  or PaaS — that's scope creep into a crowded space. Stop at "the
  cluster is Ready; kubectl takes it from here."
- **Run anywhere.** Workstation (dev convenience) → portable Linux
  daemon → in-cluster. Deliberately avoid Crossplane's chicken-and-egg
  (needing a k8s cluster to run the thing that manages your infra).
  When openctl runs *locally* (e.g. a Mac) it does **not** manage its
  own host; when deployed *into* supported infra (e.g. Proxmox) it may
  manage — and even self-deploy — its own box.
- **Multi-user eventually.** Single-operator today; real multi-user /
  RBAC is a genuine future need, gated behind adoption + a shared
  deployment actually existing.

## North-star demo (the 2-year check)

Hybrid-cloud infra management: **log in, launch a system in Proxmox /
AWS / GKE, see the result.** State is saved to flat files committed to
git. Users either submit IaC to openctl or commit it to git and openctl
brings the infra up; they log in, check status, and view resources
linked together with their dependencies in graphs.

Reality check: this loop is **~80% built for Proxmox + k3s already**
(DiskMirror + `manifests.git` + two-way GitOps = flat-files-to-git;
DryRun/Apply/reconciler = bring-it-up; DagView = dependency graphs;
the whole UI = log-in-and-see-status). The gap is **breadth** (only two
providers) and **reach** (only runs on the dev Mac) — which is exactly
what the priority order below attacks.

---

## Priority order

### Tier 1 — the spine (roughly in sequence)

1. **External plugin protocol — designed deliberately.** The generic,
   reusable provider interface (the "wide" ecosystem foundation) and the
   thing contributors need. Ship it with **one** external example
   provider to prove the ABI. Design the Terraform host (item 2) as an
   explicit second consumer *now*, so the interface is shaped right the
   first time. See [plugin-architecture.md](plugin-architecture.md).
2. **Terraform / OpenTofu provider host** — a *second implementer* of
   that same interface that delegates to any `terraform-provider-*`
   binary. The breadth multiplier: one adapter unlocks the whole
   provider registry (the AWS/GKE half of the demo). Details + honest
   hard-parts analysis in [plugin-architecture.md](plugin-architecture.md).
3. **Run-anywhere: portable Linux daemon + `install --target ssh://`.**
   A systemd daemon, not just a Mac LaunchAgent. Unlocks real use beyond
   the dev box and is the prerequisite for self-hosting. Largely
   **independent** of items 1–2 — can proceed in parallel.

### Tier 2 — natural follow-ons

4. **Self-hosting bootstrap** (`install --target proxmox://`) — deploy
   openctl onto its own managed infra. Demo-able, fits "run anywhere";
   needs the Linux daemon (item 3) first.
5. **Multi-user auth (OIDC / RBAC)** — real but *downstream of
   adoption*, which is downstream of breadth + reach. Revisit the moment
   a shared deployment (item 3) creates an actual second user. Until
   then root token + TLS + sessions is adequate.

### Tier 3 — park with a clear conscience

6. **Client-side CUE WASM validation** — pure editor-latency polish, off
   the critical path.
7. **Mobile-friendly layout** — cosmetic.
8. **Workloads / PaaS** — vetoed by scope (see above); keep it vetoed.

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
