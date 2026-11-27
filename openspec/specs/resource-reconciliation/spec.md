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

The system SHALL skip a provider's Apply only when raw `input_hash` and resolved
`refs_hash` match the last successful apply AND live provider observation matches
the declared observable fields under provider-specific comparison semantics.
Missing observation, read errors, empty projections, and secret markers without
an observable revision SHALL NOT establish a cache hit.

#### Scenario: Unchanged intent and unchanged upstream → cache hit

- **WHEN** a resource is re-applied with identical raw intent, unchanged `$ref`
  values, and matching live observable configuration
- **THEN** the provider's Apply is skipped and the operation is marked cached

#### Scenario: Upstream drift invalidates the cache despite identical intent

- **WHEN** a resource's raw manifest is byte-identical but a `$ref` target's
  value has changed since the last apply
- **THEN** the cache misses and the provider's Apply runs

#### Scenario: Live drift invalidates unchanged intent

- **WHEN** intent and references are unchanged but a managed live setting differs
- **THEN** Apply runs rather than returning a cached success

#### Scenario: Metadata-only intent remains durable

- **WHEN** only annotations change and live configuration is unchanged
- **THEN** the updated metadata is persisted even if provider Apply is skipped

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

### Requirement: Overview is persisted metadata, not lab observation

The system SHALL expose a viewer-accessible, internally consistent read-only
snapshot of saved desired inventory and retained root request activity without
provider observation, secret resolution or payload decoding.

#### Scenario: A provider or schema is unavailable

- **WHEN** the operator opens overview or navigation
- **THEN** persisted resources, including owned children and unregistered kinds,
  remain countable without provider Get/List or guest SSH requests
- **AND** discovery-only observations are not presented as saved managed records;
  neither counts nor successful request history establish current readiness

#### Scenario: History exceeds the displayed request cap

- **WHEN** active or review lists are bounded
- **THEN** exact root Apply/Delete totals remain independent of the cap and child
  steps never double-count
- **AND** review uses each resource's latest retained admission rather than
  timestamp ordering; a newer successful or active request supersedes failure
- **AND** explicit inspection can load a retained operation outside the live
  event window without retrying it or changing desired intent

#### Scenario: Overview transport fails or the session ends

- **WHEN** a snapshot cannot be refreshed
- **THEN** missing counts remain unknown and any retained snapshot is explicitly
  stale, with no fallback to provider scans or invented zero totals
- **AND** logout/stop cancels polling and rejects late results from that session

### Requirement: Saved intent remains inspectable without provider observation

Viewer-accessible inventory reads SHALL load saved desired state independently
of providers. Lists SHALL read metadata without selecting spec bodies; named
reads SHALL preserve raw spec markers and provenance without returning status,
drift, children or provider-derived ownership. Existing live Get/List semantics
SHALL remain unchanged.

#### Scenario: A saved resource is unreachable, missing or unregistered

- **WHEN** live observation fails or omits a saved resource
- **THEN** its saved list row, desired declaration and provenance remain available
- **AND** missing, unavailable and stale observations are explicitly distinguished;
  no successful Apply or stored record establishes current health or ownership
- **AND** mutation controls and handlers require a fresh live observation;
  no fallback automatically recreates, reconciles, forgets or deletes resources
- **AND** named CLI saved JSON/YAML is a replayable desired declaration, not a
  fabricated live resource or an envelope containing provenance

#### Scenario: Inspection changes route or authorization expires

- **WHEN** the inspected identity/session ends
- **THEN** pending saved, observed, history and graph reads are canceled or ignored
- **AND** late results cannot replace a newer inspection or restore private data
- **AND** saved-store failures remain errors, never an empty lab or false NotFound

### Requirement: Per-resource previews distinguish all desired intent

Read-only controller previews SHALL distinguish create, update and no-op and
keep stored-spec edits, desired label/annotation changes and live drift separate.

#### Scenario: Only desired metadata changes

- **WHEN** labels or annotations are added, changed or cleared
- **THEN** the preview reports update and metadata-relative differences, even
  when the provider plan has no work; absent and empty maps are equivalent
- **AND** the console and CLI show stored versus submitted values without
  confusing them with desired versus live provider observation
- **AND** desired YAML excludes observed ownership references without removing
  the separate ownership edit guard

#### Scenario: CLI preflight rejects invalid intent

- **WHEN** `ctl preview` receives controller validation errors
- **THEN** it emits diagnostics in the selected output format and exits nonzero
  without Apply, operation admission, gate approval or an existence reservation

### Requirement: Runtime inspection is explicit and protects downloads

Controller-backed CLI runtime actions SHALL remain distinct from desired-state
Apply and SHALL NOT automatically retry uncertain requests or open returned URLs.

#### Scenario: An action returns file content

- **WHEN** an operator requests a download to a path
- **THEN** the CLI reserves a new private file before invoking the action and
  never uses a server-provided filename as the destination
- **AND** an existing file or symlink prevents invocation; failed cleanup never
  removes another writer's replacement
- **AND** payloads are not printed without an explicit raw-stdout request;
  output failure distinguishes a successful action request from task readiness

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

### Requirement: Success includes durable desired-state bookkeeping

The system SHALL require durable desired-state bookkeeping before reporting
operation success and SHALL distinguish partial provider success from rollback.

#### Scenario: Mutation succeeds but intent persistence fails

- **WHEN** provider mutation succeeds but saving or removing desired intent fails
- **THEN** the operation fails with an explicit partial-success diagnostic,
  without claiming the provider mutation was rolled back

### Requirement: Immutable VM identity and safe ownership boundaries

The system SHALL verify managed VM identity and fail closed on ambiguous
placement, ownership or dependency evidence.

#### Scenario: An ordinary operation verifies physical identity

- **WHEN** a managed Proxmox VM is read or mutated
- **THEN** recorded context, endpoint, node, VMID and UUID are checked, and an
  ambiguous name, changed identity, or unreadable ownership fails closed

#### Scenario: A legacy tracked VM needs identity evidence

- **WHEN** an existing tracked VM lacks identity evidence
- **THEN** an administrator can confirm its observed identity against canonical
  saved placement without adopting, applying, or changing the physical VM

#### Scenario: Deletion crosses an ownership or dependency boundary

- **WHEN** a top-level Delete targets an unmanaged, owned, or referenced resource
- **THEN** it is refused; in-flight Apply references and Delete admission fence
  each other transactionally

### Requirement: One desired-state authority

The system SHALL select exactly one desired-state authority and reject
contradictory automation configuration.

#### Scenario: The controller owns desired intent

- **WHEN** mode is `mirror`
- **THEN** the controller owns desired intent and may write/push an audit mirror,
  but never watches or pulls it as an input

#### Scenario: The upstream repository owns desired intent

- **WHEN** mode is `gitops`
- **THEN** upstream authored manifests own intent; no auto-commit, push, startup
  file revival, or direct console/CLI Apply/Delete competes with that repo
  (runtime controls remain available to editor/admin sessions)

### Requirement: Durable Apply request identity

An accepted keyed Apply SHALL atomically bind its original unresolved desired
intent, flags and canonical actor to one operation without storing resolved
secrets in the request ledger.

#### Scenario: The acknowledgement is lost or operation history is pruned

- **WHEN** the same actor presents the same key, intent and flags again
- **THEN** the original operation is returned, including across normal restarts;
  a pruned binding refuses resubmission and reports the original operation ID
- **AND** changed intent or a different actor conflicts without new work
- **AND** request lookup is read-only and accepts opaque printable request IDs

#### Scenario: Generic console Apply loses its acknowledgement

- **WHEN** an editor, template starter or manual reconciliation submits Apply
- **THEN** it uses advertised durable request support, persists bound identity
  metadata before submission and verifies the acknowledged request/operation IDs
- **AND** browser recovery storage contains no manifest, parameters, credentials,
  destructive flags, consent or cached readiness
- **AND** a restored record keeps its original target separate from current draft
  defaults and performs no automatic submission, navigation or baseline update
- **AND** explicit read-only recovery validates action, target and request identity
  even if the template, schema, provider or managed resource is unavailable
- **AND** only confirmed terminal work can start a newly reviewed attempt, with
  refreshed preview, cleared gates and a new key; unknown/pruned work stays reserved
- **AND** route, principal or preview changes reject old asynchronous work;
  first-create retries without a baseline remain create-only
- **AND** manual reconciliation opens the same preview/review Apply flow rather
  than using a second unkeyed mutation path

### Requirement: Durable Delete identity cannot target replacement infrastructure

An accepted keyed Delete SHALL atomically bind its action, canonical actor and
fully qualified target to one root operation in the same request namespace as
Apply. It SHALL retain all fresh ownership, identity, dependency and manifest
authority guards; accepted replay SHALL observe the original operation without
reapplying those mutable checks or calling providers.

#### Scenario: Delete acknowledgement is lost or its target is replaced

- **WHEN** a keyed Delete is replayed concurrently, after restart, after removal
  of tracking/provider configuration, or after a same-name replacement
- **THEN** it returns its original operation without admitting another deletion
- **AND** changed target, action or actor conflicts; pruned history retains the
  reserved binding and cannot authorize new work
- **AND** CLI and console require advertised durable Delete capability and a
  matching acknowledged request ID and nonempty operation ID
- **AND** errors preserve the original request identity for read-only recovery,
  without automatically submitting again or choosing a fresh key

#### Scenario: The console restores an uncertain deletion

- **WHEN** the tab reloads or observation disappears after submission
- **THEN** persisted attempt metadata is inspected only after validating the
  controller/principal/target binding, without restoring consent or readiness
- **AND** explicit recovery reads the original operation even without a live
  resource; unknown lookup never proves that pending admission is impossible
- **AND** deletion success requires terminal operation evidence, not NotFound
- **AND** known failed/interrupted/canceled attempts require fresh inspection,
  typed confirmation and a new request identity before any new mutation

### Requirement: A saved review is not execution authority

Prepared reviews SHALL be bound to the controller and principal and require
explicit resumption; browser persistence SHALL NOT substitute for live checks.

#### Scenario: An operator resumes a prepared deployment

- **WHEN** a saved review is resumed after reload or a lost acknowledgement
- **THEN** accepted work is observed without automatic resubmission, previous
  successful dependencies are reverified, and new attempts require fresh preview
  and consent rather than restored approvals or cached readiness
- **AND** known terminal retries use new request identities; discarding a browser
  record neither cancels controller work nor deletes infrastructure

### Requirement: Historical controller snapshots remain recoverable

Offline recovery SHALL validate an exact known migration schema without modifying
the source or upgrading the restored database before normal daemon startup.

#### Scenario: An older supported snapshot is restored

- **WHEN** a snapshot matches a known historical schema
- **THEN** restore preserves its recorded identity and state and normal startup
  applies subsequent migrations; unknown, future or modified schemas are refused
- **AND** request deduplication covers only history present in that snapshot,
  not provider work submitted after it was taken

### Requirement: Standalone application lifecycle is independent of VM lifecycle

The system SHALL manage standalone applications with verified ownership,
protected credentials, data retention and independently observed readiness.

#### Scenario: An application is applied or updated

- **WHEN** an application is applied or updated
- **THEN** verified SSH, controller-specific ownership, immutable placement,
  protected secret delivery and live application observation govern the rollout

#### Scenario: Application containers are removed

- **WHEN** application containers are removed
- **THEN** configuration, volumes and media remain; VM deletion is a separate task

#### Scenario: Application health is inspected

- **WHEN** application readiness is shown
- **THEN** guest, containers, HTTP and configured storage/GPU/public checks remain
  distinct, and VM power state alone cannot establish application readiness

