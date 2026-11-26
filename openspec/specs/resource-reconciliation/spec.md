## Purpose

The core control loop of openctl: how a single declared resource is applied,
how cross-resource `$ref` and `$secret` markers are resolved, how work is
scheduled and cached, and how desired state is persisted and continuously
reconciled — all under the per-resource-independence wedge (no global plan or
state). This is the contract every provider (compiled-in, external plugin, or
Terraform host) plugs into.

## Requirements

### Requirement: Uniform resource model and provider dispatch

The system SHALL represent every managed thing as a uniform
`protocol.Resource` (`apiVersion`, `kind`, `metadata`, `spec`, `status`) and
route each operation to the provider that owns its kind through a single
provider interface, so that adding a provider or kind does not change the
control loop.

#### Scenario: Apply routes to the owning provider

- **WHEN** an Apply operation is dispatched for a resource of a given kind
- **THEN** the dispatcher selects the registered provider whose `Kinds()`
  includes that kind and invokes its `Apply`, returning the provider's
  observed resource as the result

#### Scenario: An unroutable kind fails the operation, not the loop

- **WHEN** an operation names a kind no registered provider owns
- **THEN** that single operation fails with an unrouted-kind error and other
  in-flight operations are unaffected

### Requirement: Per-resource independence (no global plan or state)

The system SHALL apply, cache, and reconcile each resource independently,
without constructing a global plan or a global state file. Ordering between
resources SHALL derive only from explicit `$ref` edges, never from a
whole-fleet plan.

#### Scenario: Independent resources make progress in isolation

- **WHEN** multiple unrelated resources are submitted together
- **THEN** each is applied on its own pipeline (resolve, cache, apply, save)
  and one resource's failure does not block or roll back the others

### Requirement: Reference resolution before provider Apply

The system SHALL resolve `$ref` markers (`{"$ref": {apiVersion, kind, name,
field?}}`) in a resource's spec to the referenced resource's value (whole
resource, or a dotted `field` path into its status/spec) BEFORE the provider's
Apply runs, so the provider only ever sees resolved values. An unresolvable
`$ref` SHALL fail the operation with a specific reference error. The raw
manifest with markers intact — never the resolved values — SHALL be what is
persisted.

#### Scenario: A ref resolves to an upstream status field

- **WHEN** a resource's spec contains `$ref` to another resource's
  `status.outputs.<field>` and that resource exists
- **THEN** the marker is replaced with the resolved value in the manifest
  handed to the provider, and the same `$ref` establishes a scheduling
  dependency on the referenced resource

#### Scenario: An unresolvable ref fails the operation

- **WHEN** a `$ref` names a resource or field path that cannot be resolved
- **THEN** the operation fails with a reference-specific error and the
  provider's Apply is not invoked

### Requirement: Secret resolution is transient and git-safe

The system SHALL resolve `$secret` markers (`{"$secret": {provider, key}}`)
through a named secret provider at apply time, hand the plaintext value only to
the provider's Apply, and persist ONLY the raw marker — never the resolved
secret value or a hash of it — to the operations store, applied-manifests
store, disk mirror, or git.

#### Scenario: A secret feeds Apply without landing on disk

- **WHEN** a resource's spec contains a `$secret` marker and is applied
- **THEN** the secret provider resolves the value, the value reaches the
  provider transiently for this apply, and every persisted copy of the manifest
  retains the `$secret` marker rather than the value

### Requirement: Verifying-trace cache keyed on intent and upstream state

The system SHALL skip a provider's Apply when a resource is unchanged, using a
two-dimensional cache: `input_hash` over the raw manifest (user intent) and
`refs_hash` over the resolved `$ref` values (upstream state). A cache hit SHALL
require BOTH hashes to match the last successful apply.

#### Scenario: Unchanged intent and unchanged upstream → cache hit

- **WHEN** a resource is re-applied with a byte-identical raw manifest and all
  its `$ref` targets resolve to the same values as the last success
- **THEN** the provider's Apply is skipped and the operation is marked cached

#### Scenario: Upstream drift invalidates the cache despite identical intent

- **WHEN** a resource's raw manifest is byte-identical but a `$ref` target's
  value has changed since the last apply
- **THEN** the cache misses and the provider's Apply runs

### Requirement: Cross-operation dependency scheduling

The system SHALL schedule a batch of pending operations as a dependency graph
derived from their `$ref` edges: independent operations run concurrently and
dependent operations run after the operations they reference. A dependency
cycle SHALL fall back to unordered scheduling so no operation is left claimed
but unrun. This scheduling SHALL be enabled by default with an escape hatch to
FIFO via `OPENCTL_CROSS_OP_SCHEDULING=0`.

#### Scenario: Dependent operation waits for its reference

- **WHEN** operation B's resource `$ref`s the resource applied by operation A
  in the same batch
- **THEN** A is scheduled before B, and if A fails, B fails at reference
  resolution rather than applying against a missing dependency

#### Scenario: Same-resource operations never run concurrently

- **WHEN** two operations target the same resource
- **THEN** they are serialized (a single claimed batch never holds two
  operations for one resource), enforced at submission time

### Requirement: Desired-state persistence with provenance

On each successful apply the system SHALL persist the resource's raw desired
manifest as the single source of desired state for drift detection, and record
the originating source (`cli`, `ui`, `gitops`, `auto-reconcile`) as a
first-class field. On each successful delete it SHALL remove that row.

#### Scenario: Apply records desired state and its source

- **WHEN** an apply succeeds
- **THEN** the raw manifest (markers intact) is upserted keyed by
  `(apiVersion, kind, name)` and its `source` column records who applied it

### Requirement: Continuous drift reconciliation

The system SHALL periodically re-check every managed resource against its
observed state and surface drift. Remediation SHALL be off by default (drift is
logged) and opt-in per resource via the `openctl.io/autoReconcile` annotation,
which enqueues an Apply of the stored manifest when drift is detected.

#### Scenario: Drift on an unannotated resource is surfaced, not fixed

- **WHEN** the periodic reconciler detects that a managed resource's observed
  state has drifted from its stored desired manifest
- **THEN** the drift is logged and no Apply is enqueued unless the resource
  carries `openctl.io/autoReconcile: "true"`
