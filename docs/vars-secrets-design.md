# Variables & secrets — design proposal

**Status:** proposal, awaiting sign-off. Not implemented.
**Author:** autonomous session, 2026-07-07.
**Roadmap item:** "Future goals → Variables & secrets" (ROADMAP.md).
Surfaced when the VM create form prompted for an SSH key and raised the
question: what does openctl have where Terraform has tfvars and secret
handling?

Short answer today: **nothing purpose-built.** This proposal designs the two
distinct things that question conflates — *keeping secrets out of the
git-synced manifest* and *parameterizing manifests for reuse* — and
recommends shipping them as two independent slices, secrets first (it closes a
real leak), parameters second (it's convenience).

## Two needs, often conflated

1. **Secrets.** Sensitive values (a VM root password, an API token, a private
   key) must not end up in plaintext in a manifest that openctl **materializes
   to disk and commits to git** (Phase U2 DiskMirror + git sync). This is a
   correctness/security concern, not a convenience.
2. **Parameterization ("variables").** Reuse one manifest shape across many
   instances or environments with values that vary — the tfvars / `-var-file`
   use case. This is convenience and DRY-ness.

They look similar (both are "a value that lives outside the manifest body")
but the requirements diverge: secrets care about *never being persisted*;
variables care about *being substituted and reused*. Designing them as one
feature is how you end up writing secrets into your state file.

## What already exists (the foundations to build on)

- **`$ref` cross-resource value passing** (`internal/schema/schemas/base/
  resource.cue` `#Ref`; resolver in `internal/controller/refs`). Resolves
  another resource's `status`/`spec` field at apply time. This is the
  Terraform *resource-reference / output* analog — **not** input variables.
  Critically, the dispatcher already **preserves the raw, marker-bearing
  manifest through resolve → apply** and persists *that*, not the resolved
  values (the refs_hash work, migration 0008 — done precisely so a resolved
  upstream IP isn't frozen into stored desired state). **This is the exact
  plumbing precedent secrets need.**
- **Provider credentials, done right** (`internal/config/config.go`
  `Credential{TokenID, TokenSecret | TokenSecretFile}`). `tokenSecretFile`
  (`config.go:264`) reads a 0600 file; the secret never enters a manifest or
  the wire to the UI (ConfigService uses a `has_secret` bool +
  blank-preserves). This is the pattern to generalize — but today it covers
  only provider auth, nothing in a resource spec.
- **Template parameters** (`internal/controller/server/template.go`,
  `TemplateParameter{Name, Required, Default}`). Closest thing to tfvars input
  variables, but Go-compiled starters rendered server-side into a manifest
  once at create — not a reusable values overlay.
- **CUE** is already the schema/validation engine, embedded in both client and
  server (`internal/schema`). It has native defaults, imports, and unification
  — the machinery a variables layer needs — but none of it is currently
  exposed for user parameterization.

## The gap, and the sharp edge

- The SSH **private key** is handled *well*: specs carry `privateKeyPath` (a
  path on the controller host), so key material never enters the manifest.
  This is the model.
- But SSH **public keys** and **`password`** go **inline, plaintext, in the
  spec** (`internal/schema/schemas/proxmox/vm.cue:145` `password?`, `:147`
  `sshKeys?`). And applied manifests are materialized to disk and git-synced.
  So **an inline `password` is committed to your git repo.** A public key isn't
  sensitive; a password absolutely is — and there is no indirection to keep it
  out.

That single fact — a sensitive spec field that lands in git — is the concrete
motivation for Part A.

## Part A — Secrets: `secretRef` / `valueFrom` (ship this first)

A spec-level indirection, resolved at apply time, **redacted from the
materialized + git-synced manifest**. It generalizes the `tokenSecretFile`
convention from provider config to any spec field.

### Authoring shape (mirrors `#Ref`)

A `#Secret` CUE helper in `base`, usable in any field a schema marks
`@secret`:

```cue
spec: cloudInit: {
    // instead of:  password: "hunter2"
    password: base.#Secret & { valueFrom: file: "vm-root.pw" }   // 0600 under the state dir
}
```

Wire shape: `{ "$secret": { file: "vm-root.pw" } }` (or `env: "VM_ROOT_PW"`),
paralleling `{ "$ref": {...} }` so the resolver, form walker, and redaction
logic all recognize a single marker convention.

### Sources (decision 2)

- **file** — path resolved under the state dir, mode 0600, exactly like
  `tokenSecretFile`. **(Recommended for v1.)**
- **env** — a named environment variable on the controller. **(Recommended for
  v1** — cheap, good for CI-injected secrets.)
- **keyring** — OS keyring / an external secret manager. **(Follow-on.)** The
  marker is extensible (`$secret.keyring: "..."`), so adding a source later is
  additive.

### Resolution + redaction (the load-bearing part)

- A `internal/controller/secrets` resolver (the direct analog of
  `internal/controller/refs`) walks the spec pre-Apply and substitutes each
  `$secret` marker with the resolved value into a **transient** manifest handed
  to `provider.Apply`.
- The **stored** `applied_manifest` — the one DiskMirror writes and git commits
  — keeps the **marker, never the value.** This reuses the raw-manifest-
  preservation the dispatcher already does for `$ref` (so `spec_json` /
  disk / git carry `{"$secret":{"file":"vm-root.pw"}}`, not the password).
- Drift comparison compares markers, not resolved secrets; the reconciler
  re-resolves on each apply. A changed secret *file* is not spec drift (the
  manifest is unchanged) — matching how `tokenSecretFile` behaves today.

The critical invariant, and the test that must pin it: **after applying a
manifest with a `$secret` field, the on-disk materialized YAML and the git
commit contain the marker and never the secret value.**

### UI

The form walker maps a schema's `@secret` attribute to `Field.Secret`; the
renderer shows a "reference a secret" input (source picker + name), never a
plaintext box, for those fields. DryRun previews render the value as `••••`.
Existing inline plaintext still loads (back-compat) but the field nudges toward
a reference.

## Part B — Parameterization: lean on CUE, don't invent tfvars

openctl already embeds CUE, which has variables, defaults, imports, and
unification natively. Rather than bolt on a bespoke tfvars format, accept a
**values overlay** that unifies with the manifest:

- Author a manifest as `.cue` (or plain YAML) and supply a values document:
  `openctl ctl apply -f vm.cue --values prod.cue`. The controller/CLI unifies
  `vm.cue & prod.cue`, validates the concrete result against the kind's
  schema, then applies. YAML manifests keep working unchanged — this is purely
  additive.
- Defaults and required-but-unset variables fall out of CUE's own semantics
  (a required field left `_|_` fails validation with a path-attributed error —
  the same machinery `ValidateStructured` already surfaces per-field in the
  UI).

This gives real input variables with type-checking for free, and keeps a
single expression language across schemas, forms, and now parameterization.

## Decisions (each needs a call; recommendation given)

1. **Split into two slices, secrets first.** Secrets is a live leak risk;
   parameters is convenience. **(Recommended: yes.)**
2. **Secret sources for v1: file + env; keyring/external later.**
3. **Redaction: persist the marker, not the value** — reuse the `$ref`
   raw-manifest-preservation precedent (migration 0008). **(Recommended;
   non-negotiable for the feature to be worth anything.)**
4. **Marker shape: a `#Secret` helper emitting `{"$secret":{...}}`,** mirroring
   `#Ref`, so resolver + form + redaction share one convention.
5. **Which fields are secret: mark in CUE with an `@secret` attribute** (so the
   form redacts and a lint can flag inline literals). Apply to `password` and
   any future token-like fields.
6. **Parameters engine: CUE unification via a `--values` overlay,** not a new
   file format.
7. **Back-compat for existing inline plaintext: keep it working,** but add an
   opt-in lint/warning when an `@secret` field carries a literal in a
   git-synced manifest (so the leak is visible, not silent).

## Implementation sketch

- `internal/schema/schemas/base/resource.cue`: add the `#Secret` helper;
  annotate `vm.cue` `password` (and peers) `@secret`.
- `internal/controller/secrets/` (new): `Resolver` mirroring
  `internal/controller/refs` — sources `file` (state-dir-relative, 0600) and
  `env`; returns a resolved copy + leaves the raw manifest untouched.
- **Dispatcher**: resolve `$secret` into the transient manifest alongside the
  existing `$ref` resolution, before `provider.Apply`; persist the raw
  (marker-bearing) manifest exactly as the refs path already does. No new
  persistence store needed.
- `internal/schema/form/`: `@secret` → `Field.Secret`; UI renders a secret
  input and redacts previews.
- **Part B**: `internal/cli` gains `--values <file.cue>`; the apply path
  unifies overlay + manifest before validation. Server-side `DryRunApply`
  learns the same overlay for UI parity (follow-on).

## Testing plan

- **The redaction invariant** (the one that matters): apply a manifest whose
  `password` is a `$secret` file reference; assert the provider received the
  real value AND the materialized YAML + git commit contain the marker, never
  the value.
- Secret resolved from `file` and from `env`; missing source → apply fails with
  a clear "secret X: not found" message (mirroring the `$ref` not-found path).
- DryRun redacts the value.
- Back-compat: an inline plaintext `password` still applies (with the lint
  warning when enabled).
- Part B: `vm.cue & prod.cue` unifies, validates, applies; a required-but-unset
  variable fails validation with a path-attributed error.

## Non-goals

- A full secret-management backend (Vault, cloud secret managers). File + env
  + eventual keyring cover the homelab threat model; an external-manager source
  is a later marker extension, not v1.
- Encryption at rest of the secret *source* files — rely on filesystem perms
  (0600), exactly as `tokenSecretFile` does.
- A templating language beyond CUE. If parameterization needs more than CUE
  unification, that's a signal to reconsider, not to add a second language.

## Rollout

Part A is additive (inline plaintext keeps working), so it can land without a
flag: ship the `$secret` resolver + redaction + `@secret` annotations, add the
form support, then migrate the VM `password` docs/examples to `secretRef` and
document the convention in QUICKSTART/README alongside `tokenSecretFile`. Part
B is an additive `--values` CLI flag. Only once Part A's redaction test is
green — proving no secret reaches disk or git — is the "secrets" half of the
roadmap item done.
