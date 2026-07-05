# Plugin architecture — one interface, many implementers

Design doc for openctl's provider plugin layer and the Terraform /
OpenTofu provider host that plugs into it. This is the technical spine
behind Tier 1 of [direction.md](direction.md). Nothing here is built
yet — it's the shared understanding to implement against.

The native external plugin protocol and the Terraform host are **two
halves of one design**: the protocol defines openctl's provider
interface; the TF host is one more implementer of it. Design them
together so the interface is shaped right the first time.

---

## Core principle

openctl's core — the dispatcher, reconciler, UI, git mirror, `$ref`
resolver, DagView — **never talks to Proxmox or AWS directly.** It talks
to a single **provider contract**:

```
Apply · Get · List · Delete · DryRun · Plan · Schema
```

Anything that answers those calls is a provider, and the core doesn't
know or care what's behind the door. That single fact is what lets one
interface have several kinds of implementer.

## Implementer types

| # | Implementer | How it satisfies the contract | Work-per-provider |
|---|---|---|---|
| 1 | **Native in-process** (Proxmox, k3s today) | Go code that calls the target API directly | 1 unit → 1 provider |
| 2 | **External plugin binary** (the native protocol) | Separate binary answering the contract over JSON-over-stdio | 1 unit → 1 provider |
| 3 | **Terraform / OpenTofu host** | ONE openctl provider that delegates to any `terraform-provider-*` over tfplugin6 gRPC | 1 unit → **whole registry** |

Types 1 and 2 are one-provider-at-a-time (you wrote Proxmox by hand;
you'd write the next by hand). Type 3 is the **breadth multiplier**.

## The Terraform host as a second implementer

```
                openctl core
                     │   Apply / Get / Delete / DryRun / Schema
                     ▼
           ┌─────────────────────┐
           │   Terraform host    │   ← ONE openctl provider
           │   (the adapter)     │
           └─────────────────────┘
                     │   tfplugin6 gRPC (go-plugin):
                     │   PlanResourceChange, ApplyResourceChange, ReadResource…
                     ▼
   terraform-provider-aws   terraform-provider-google   …  (unmodified, off the registry)
```

Upward it looks exactly like Proxmox. Downward it speaks Terraform's
plugin protocol to real, unmodified provider binaries. To openctl's
core, an `aws_instance` arrives through the **same door** as a Proxmox
VM — so the dispatcher, git mirror, DagView, and reconciler all work on
it unchanged.

**Breadth multiplier, precisely:** the native path is *1 unit of work →
1 provider*. The TF host is *1 unit of work → the entire Terraform
Registry* (AWS, GCP, Azure, Cloudflare, … tens of thousands of resource
types), none of which you write. That single adapter is what turns the
north-star demo from "Proxmox only" into "proxmox **and** aws **and**
gke in one UI."

**Precedent (this genre is solved):** Crossplane **Upjet**, the
**Pulumi Terraform Bridge**, Flux's **tf-controller**. Standard
technique: embed the client-side libraries (`hashicorp/go-plugin` +
`terraform-plugin-go/tfprotov6`), translate schema, hold state. Target
**OpenTofu** for the cleaner license story (identical protocol).

## The crucial subtlety — wrap providers, not the orchestrator

You wrap Terraform **providers** (stateless API-driver libraries: "here's
how to POST to the EC2 API"). You do **not** adopt Terraform's
**orchestrator**. openctl stays the brain: each `aws_instance` is applied
as its **own independent op with its own per-resource state blob**. No
monolithic `terraform.tfstate`, no whole-world `plan`, no
drift-hunt-across-a-giant-file.

That's what makes this *fit* the wedge instead of betraying it: you get
"add one thing without re-planning the universe" **on top of** AWS,
using Terraform only for the boring part (talking to the API) and
throwing away the part we reject (its state model + CLI UX).

## RPC mapping

| openctl | Terraform (tfplugin6) |
|---|---|
| `Apply` | `PlanResourceChange` + `ApplyResourceChange` (threading the private blob) |
| `Get` / drift check | `ReadResource` (refresh) |
| `Delete` | `ApplyResourceChange(planned = null)` |
| `DryRunApply` | `PlanResourceChange` |
| provider config (ConfigService / config.yaml) | `ConfigureProvider` |
| observed-only kinds | data sources (loose analog) |
| — | `UpgradeResourceState` (called on provider version bumps) |
| — | `ImportResourceState` (optional: adopt existing cloud resources) |

Two concept mismatches to keep straight:

- `Planner.Plan()` in openctl means **composite expansion** (Cluster →
  children), *not* Terraform's per-resource diff. Same word, different
  jobs — don't conflate them in code or docs.
- Terraform models **"known after apply"** unknowns across a graph;
  openctl's `$ref` resolution is eager. See the graph section below.

## Sequencing decision

1. Design & ship the **native external plugin protocol** first, with the
   TF host as an explicit second consumer of the interface (so the
   contract is shaped for both). Prove the ABI with one example external
   provider.
2. Build the **TF host** as implementer type 3 against that same
   contract.
3. **Run-anywhere** (Linux daemon) proceeds in parallel — it's
   orthogonal to the plugin work.

---

## The three hard parts, honestly assessed

The conceptual fit is good, but three things don't come for free. Two
are smaller than they look; the third we never had a choice about.

### 1 · Schema prettiness → opt-in overlays (cheap)

tfplugin6 schemas are cty-typed (block/attribute split, nesting modes
single/list/set/map). Auto-translating them to openctl's CUE/form gives
a *working* but not always *pretty* form (set semantics and
block-vs-attribute don't map 1:1). Two levels of remedy, **opt-in per
resource**:

- **Full native provider** — reimplement the API-wrangling (what
  Proxmox/k3s are). Best UX, but you own maintenance. Reserve for the
  2–3 providers used constantly.
- **Schema overlay on a TF-hosted resource** — the cheap middle path,
  and the common one. Terraform still does all the API work; you
  hand-author a nicer CUE form over the subset of fields that matter
  (rename/group/doc/default/enum, hide the 180 obscure ones). This is
  exactly what Upjet does — curate the projection, don't reimplement the
  provider.

Default to the raw auto-translated form; overlay selectively.

### 2 · State store → a new `provider_state` table (medium, low-risk)

**The idea.** TF providers hand back an **opaque** blob (msgpack
`DynamicValue`) after every Apply/Read. openctl's *only* job is: store
it, hand it back next call. You never parse, merge, or hand-migrate it.

New table, keyed exactly like `applied_manifests`:

```
provider_state(api_version, kind, name,  state_blob,  private_blob,  schema_version)
                      ↑ same PK              ↑ opaque bytes — never parsed by openctl
```

**Why it's *not* the TF-state pain you're picturing.** What makes
Terraform state painful is the *monolithic, locked file*: global lock
(`force-unlock`), drift buried in one giant blob, `state mv`/`state rm`
surgery, merge conflicts. **None of that applies** — the store is
per-resource and opaque, so each resource's blob is independent. This is
the wedge again: the pain is the *monolithic* file, not the concept of a
blob. openctl already stores `spec_json`/`metadata_json` blobs; this is
one more blob column with the same upsert-on-apply / delete-on-delete
lifecycle.

**Where the real (bounded) work is — disciplined round-tripping.** TF's
plan→apply is a two-blob handshake:

- `Plan`  : `prior_state + config` → `planned_state + private`
- `Apply` : `prior_state + planned_state + private` → `new_state + private`
- `Read`  : `current_state` → refreshed `state`

So the store holds `prior_state`, and the **op** carries the `private`
blob from plan into apply. openctl's dispatcher already threads the
manifest through plan→apply, so this is an *extension of an existing
pattern*, not a new concept. Plus `UpgradeResourceState`: on a provider
version bump with a changed schema, call the provider's own migration
RPC with the old blob + stored `schema_version`; **the provider
migrates**, openctl just invokes and re-stores. (That's why
`schema_version` is a column.)

**Verdict:** conceptually small, maps onto storage we already have, and
the scary parts of TF state are structurally excluded by going
per-resource + opaque. This is the **fifth persistence store**, distinct
from `applied_manifests` (which holds *desired* state + cache hashes,
not *observed provider* state) — see the state-model map below.

### 3 · Graph / unknowns → we own it; we already did (the delta is contained)

The key fact that reframes the whole question:

> **Terraform's dependency graph lives in Terraform *core*, not in the
> providers.** Providers are deliberately dumb — `PlanResourceChange` /
> `ApplyResourceChange` operate on **one resource at a time**. Topo
> ordering, dependency edges, and unknown-value propagation are all
> core's job.

So when you host providers you *cannot* reuse TF's graph — it isn't on
offer — and you don't want to, because it's welded to the monolithic
plan/state we reject. **You own orchestration regardless.** And the good
news: openctl already owns an equivalent.

| Terraform core | openctl (already built) |
|---|---|
| HCL `${aws_vpc.main.id}` interpolation | `$ref: {kind: aws_vpc, name: main, field: status.id}` |
| dependency graph + topo order | `refs.Collect` edges + dispatcher ordering |
| refresh / drift | `Get` / reconciler |

For **single resources** and **dependencies whose upstream already
exists**, this works with what we have today: the dispatcher applies
`aws_vpc` first, then `aws_instance` resolves `vpc_id` against the
now-real VPC status. No new concept.

**The one genuine delta — "known after apply" unknowns.** To *plan a
brand-new interdependent set in one shot* (VPC + instance together,
before the VPC exists), Terraform represents `vpc_id` as an **unknown**
at plan time and propagates it, so it can preview the whole graph up
front. openctl's `$ref` resolution is currently **eager** (resolve via
`Get`), and handles not-yet-exists by *ordering* rather than *unknowns*.
To get an accurate up-front `DryRun` of a not-yet-createable resource:

- teach the ref resolver to **emit a tfplugin "unknown" sentinel** for
  an unresolved ref instead of failing (providers understand this
  natively — they return "known after apply" for downstream computed
  fields), and
- teach `DryRun` to tolerate a partial plan.

This work is **contained to the `refs` package + `DryRun`** — not a
rewrite, not new infrastructure — and only matters for multi-resource TF
graphs (existing Proxmox/k3s composites already apply-in-order fine).

**And it reinforces the wedge:** TF's monolithic graph is coupled to its
monolithic plan/state — that coupling is *why* it can't cleanly apply
one resource without the whole config. Keeping our own per-resource
`$ref` graph preserves the independence openctl exists for. "Implement
our own" isn't the tax; it's the point.

### Scorecard

- **Schema** → opt-in overlays; not all-or-nothing.
- **State store** → smaller/safer than it sounds; per-resource + opaque
  excludes the painful parts of TF state.
- **Graph/unknowns** → never a reuse option (it's core, not provider);
  we already own an equivalent; the only new work (unknown propagation
  for up-front multi-resource previews) is bounded to `refs` + `DryRun`
  and deepens the differentiator.

---

## Where this lands in the state model

openctl persists across four backends today (SQLite `state.db`, the
`~/.openctl/manifests` disk mirror, per-provider YAML under
`~/.openctl/state`, and config/creds). Notably there is **no generic
"observed provider state" blob**: k3s providers hold bespoke YAML state,
and Proxmox is entirely stateless (re-reads the API). The TF host is the
first thing that needs a generic one — hence the new **`provider_state`**
table (part 2 above), the **fifth** store. It sits alongside
`applied_manifests`, not inside it:

- `applied_manifests` = **desired** manifest + `input_hash`/`refs_hash`
  (user intent + verifying-trace cache).
- `provider_state` = **observed provider state** (the opaque TF blob) +
  `private_blob` + `schema_version`.

Same per-resource key shape, different meaning — keep them separate.

---

## Implementation status & the apply-path plan

The TF host is being built in slices (see ROADMAP.md for PRs):

- **Transport (done).** `internal/controller/providers/tfhost` launches a
  real tfplugin6 provider over HashiCorp go-plugin (fixed magic-cookie
  handshake, protocol v6, `"provider"` plugin set) and calls
  `GetProviderSchema`. Stubs are vendored in `pkg/tfplugin6` (Terraform keeps
  them `internal/`). Tested against an in-repo fake provider —
  no registry download needed.
- **Schema translation (done).** `SchemaAttributes` / `ObjectTypeForSchema`
  turn a tfplugin6 resource schema into a `tftypes.Object`, via a
  hand-rolled `parseCtyType` (see below).
- **Apply/Read path (next).** The `providers.Provider` adapter mapping
  Apply→`PlanResourceChange`+`ApplyResourceChange`, Get→`ReadResource`,
  Delete→`ApplyResourceChange(null)`, threading the `provider_state` blob.

### The load-bearing decision: avoid the deprecated msgpack codec

`terraform-plugin-go` **deprecates every value-encoding helper a client
needs** — `tftypes.ParseJSONType`, `ValueFromJSON`, `Value.MarshalMsgPack`,
`ValueFromMsgPack` all say "not meant to be called by third parties." The
library is built to *author* providers, not *consume* them. Fighting that
with SA1019 suppressions everywhere, or hand-rolling tftypes' exact msgpack
wire format (extension types for unknowns, etc.), is the real risk of the
whole approach.

**openctl's opaque `provider_state` design sidesteps it.** A tfplugin6
`DynamicValue` carries **both** a `Json` and a `Msgpack` field, and providers
accept either. So:

- **Config → provider:** encode openctl's `map[string]any` spec as a
  **schema-conformant JSON** `DynamicValue` (`DynamicValue{Json: …}`). No
  msgpack, no deprecated API — just JSON that matches the `ObjectTypeForSchema`
  shape (nulls for absent optionals, numbers as numbers).
- **Prior state → provider:** wrap the **raw bytes** stored in
  `provider_state` back into a `DynamicValue` and hand them over. openctl
  never parses them (the opaque-blob contract), so the provider's own
  msgpack round-trips through us untouched.
- **New state ← provider:** store the returned `DynamicValue`'s **raw bytes**
  verbatim in `provider_state`. Again no decode.

The one thing that still needs *decoding* is surfacing observed field values
into a resource's `status` for the UI/drift view — and that can start minimal
(a couple of computed fields) and grow, without blocking Apply/Delete. The
type parser we already own (`parseCtyType`) plus a small JSON value walk
covers it when needed, still off the deprecated path.

**Fake provider:** the test double should be built with the *public,
intended* `tf6server` + a `tfprotov6.ProviderServer` implementation (that's
what terraform-plugin-go is *for*), so all the encoding on the provider side
is the library's own non-deprecated internal use — not ours.
