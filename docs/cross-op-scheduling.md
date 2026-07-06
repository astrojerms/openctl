# Cross-op dependency scheduling — design proposal

**Status:** proposal, awaiting sign-off. Not implemented.
**Author:** autonomous session, 2026-07-05.
**Motivating work:** the composite-apply dependency DAG (#66) and the `$ref`
primitive documented in DESIGN.md § "Dependencies, Value-Passing & Ordering".

This proposes hoisting the dependency-graph machinery that today orders the
*children of one composite apply* up one level, so it can also order and
parallelize *separate top-level operations*. It touches two decisions that
CONTROLLER.md pins as locked ("settled via design walkthrough… any change
requires re-opening the discussion"), so it is written for review before any
code lands.

## Today

`operations.Dispatcher` is a **single goroutine** (`dispatcher.go`). Its
`drain()` loop calls `store.ClaimNextPending` and runs each op to completion
**synchronously**, in submission order — FIFO. Two consequences:

1. **No parallelism.** Two unrelated applies (say, VMs on different Proxmox
   endpoints) run one after the other even though nothing connects them.
2. **No cross-op ordering.** `$ref` resolution happens at apply time inside
   `Dispatcher.ApplyManifest`. If you submit a Proxmox `VirtualMachine` and a
   k3s resource that `$ref`s its `status.ip` as **two separate operations**,
   nothing sequences them: the second op resolves against whatever state
   exists *now* and fails if the VM isn't applied yet. Ordering only exists
   *inside* a composite because `Plan()` emits the children together and the
   composite-apply DAG orders them.

Collisions are handled at **submit time**: `Submit` fails fast with a
`ConflictError` if another op targets the same resource (CONTROLLER.md:52).

**Locked decisions this proposal reopens:** single-goroutine dispatch, and
fail-fast-on-same-resource-collision.

## Proposed model

Add cross-op scheduling **behind a flag** (`OPENCTL_CROSS_OP_SCHEDULING`,
default off), exactly as `OPENCTL_CONVERGE_VIA_PLAN` and
`OPENCTL_APPLY_CONCURRENCY` were introduced. Flag off ⇒ byte-for-byte today's
FIFO behavior, so existing tests and deployments are untouched until the flag
flips.

When on, each `drain()` cycle:

1. Claims the **batch** of currently-pending ops (not just the head).
2. Derives a dependency graph over the batch with `crossOpEdges` (below).
3. Runs it through the **same** `operations.RunGraph` used by #66 — topological
   execution, cycle detection, bounded concurrency — with each task being
   `execute(op)`.

Independent ops run concurrently (up to a cap); dependent ops wait for their
predecessors; the `$ref` in the dependent op resolves against the now-applied
predecessor's live `status` through the existing resolver — no new value
channel.

### Edge derivation: `crossOpEdges`

Mirror `operations.RefChildEdges`, but resolve refs to **owning ops** instead
of sibling children:

```
for each pending op O with an Apply manifest:
    for each $ref{apiVersion, kind, name} in O's manifest:
        if some other pending op P in the batch applies (apiVersion, kind, name):
            add edge O depends-on P
        else:
            no edge — the target is either already applied (resolve live at
            apply time, as today) or genuinely absent (O fails resolution, as
            today)
```

Edges are added **only** when the referenced resource is being applied by a
*concurrent pending op*. A `$ref` to an already-applied resource keeps today's
lazy-resolve semantics. This keeps the change strictly additive: it can only
*delay* an op that would otherwise have raced, never change what a
non-racing op does.

## Decisions that need sign-off

Each is a genuine product/architecture choice, with a recommendation.

### 1. Collision → ordering vs. staying fail-fast

Today a second op on an in-flight *same* resource fails fast. Two readings:

- **(Recommended) Keep same-resource fail-fast; add ordering only across
  *different* resources.** Two applies of the *same* resource remain a
  user error/race (fail fast at submit, unchanged). Cross-op edges only ever
  connect *distinct* resources joined by a `$ref`. This preserves the locked
  collision guarantee verbatim and adds ordering purely as new behavior.
- **(Alternative) Soften collision into queueing.** A second same-resource op
  waits behind the first. More flexible, but changes a guarantee callers and
  tests rely on, and invites confusing "why is my apply hanging" states.

Recommendation: the first. It's the smaller, safer semantic delta.

### 2. Cross-op failure policy

If op A fails and op B depends on A:

- **(Recommended) B is not run; it completes as failed with "dependency
  <A> failed".** This mirrors the composite-apply DAG exactly (`RunGraph`
  stops launching a task's dependents once a predecessor errors) and matches
  the declarative model: B's `$ref` could not have resolved anyway.
  Independent ops in the batch keep running.
- **(Alternative) Hold B as pending for the next cycle.** Risks a stuck op
  that never clears if A keeps failing; the user re-submitting is cleaner.

### 3. Concurrency bound and resource contention

Serial FIFO is its own backpressure. Concurrent ops can hammer one Proxmox
endpoint's API in parallel. Add a global cap `OPENCTL_CROSS_OP_CONCURRENCY`
(default small, e.g. 4), passed straight to `RunGraph`. A per-endpoint bound
is a possible follow-on but is not needed for a first cut.

### 4. Determinism

`RunGraph` already sorts its ready-set for deterministic ordering under
equal readiness, so the batch executes reproducibly given the same pending
set. No extra work.

### 5. Persistence & restart

No new persistence. Ops are already durable; the DAG is **derived fresh from
the pending set each drain cycle**, so a restart simply rebuilds it. In-flight
ops still transition to `interrupted` on restart (unchanged). A partially-run
batch resumes as: completed ops stay completed, the rest re-derive their edges
next cycle.

### 6. Observability

Surface the derived edges so a waiting op is explainable rather than
mysteriously idle. Minimum: log the batch's edges; better: record each op's
`waiting_on` predecessors on the operation row and show them in the UI ops
drawer (the DAG view already renders composite children — the same renderer
can show cross-op edges).

## Implementation sketch

- New `internal/controller/operations/crossop.go`:
  `crossOpEdges(ops []*Operation) (map[string][]string, error)` — decode each
  op's `ManifestJSON`, collect `$ref`s via `refs.Collect`, map each to the
  pending op that applies the referenced resource (`apiVersion+kind+name` →
  op id index), emit sorted/deduped edges; ignore self and external targets.
- `drain()` gains a flag branch: when `OPENCTL_CROSS_OP_SCHEDULING` is set,
  claim the ready batch, build `[]operations.Task{ID: op.ID, DependsOn:
  crossOpEdges[op.ID], Run: func(ctx){ execute(ctx, op) }}`, and call
  `RunGraph(ctx, crossOpConcurrency(), tasks)`. Otherwise the existing serial
  loop.
- `Submit` unchanged (same-resource fail-fast preserved — decision 1).
- No changes to `ApplyManifest`/resolver: dependent ops resolve their `$ref`s
  live after their predecessor applies, exactly as composite children do.

## Testing plan

- `crossOpEdges`: op-B-`$ref`s-op-A ⇒ B depends on A; ref to an
  out-of-batch/already-applied resource ⇒ no edge; external + self refs
  excluded; sorted/deduped; cycle across ops surfaces via `RunGraph`.
- Scheduling: independent ops overlap (rendezvous proving concurrency);
  dependent op waits for predecessor; predecessor failure fails the dependent
  and spares independents; flag-off reproduces FIFO exactly (existing
  dispatcher tests stay green unchanged); determinism of the ready order.

## Non-goals

- **Not** the suspending-scheduler / typed-task-IR of arch Phase 9–10 (see
  docs/target-architecture.html). This is the narrow, high-leverage slice:
  reuse the existing graph to parallelize and order *submitted* ops.
- No cross-op transactions or rollback.
- No change to how composite children are scheduled — that DAG is unchanged.

## Rollout

Ship flag-off. Validate on the homelab (independent multi-endpoint applies
in parallel; a VM + dependent k3s resource submitted separately and correctly
ordered). Flip the default only after that, in its own change.
