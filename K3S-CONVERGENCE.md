# Retiring `applyExisting` — k3s Cluster convergence plan

Staged plan to migrate existing-cluster convergence (count-up / respec /
delete) off the imperative `applyExisting` branch and onto the Plan-driven
dispatcher model, then delete the legacy code. Tracked in ROADMAP under
"Suggested next order → Retire `applyExisting`".

Status: **in progress.** PR 1 (`DeleteChild` channel), PR 2a (scale-down
via `DeleteChild`), PR 2b (count-up via Plan children), and PR 2c (respec
via destroy+recreate through the dispatcher) have landed, all behind the
gate below. Cutover (3) and deletion (4) remain. Needs homelab validation
before the final deletion.

Note: respec of the *sole* control plane is refused on the plan path —
there's no peer for the recreated node to rejoin, and re-initializing it
would orphan every other node. (It's a catastrophic op gated upstream
anyway.) Use the imperative path or a full recreate for that case.

Homelab validation surfaced (and fixed): joining agents were getting
server-only `k3s.extraArgs`; the Proxmox VM delete was async so a respec
recreate raced the destroy (`DeleteVM` now waits for the destroy task);
scale-down left the departed node's Kubernetes Node object as NotReady;
and a respec's recreated same-hostname node was rejected with "Node
password rejected, duplicate hostname" because the server still held its
old `node-password` secret. The plan-converge path now evicts a node's
Node object **and** node-password secret via a surviving CP — on
scale-down (cleanup) and, crucially, between a respec's destroy and
recreate (so the fresh node registers cleanly). This node-password gap
existed in the legacy `applyRespecs` path too; it only surfaced once
respec was actually validated end to end.

## Gating

The plan-based existing-cluster converge is **off by default** and opt-in
via `OPENCTL_CONVERGE_VIA_PLAN=1` (`convergeViaPlanEnabled()` in
`provider.go`). With it unset, an existing-cluster apply behaves exactly
as before the migration (`runChildVMDelete` for removals, `applyCountUp`
for count-up) — so the merged-but-unvalidated slices stay dormant. Set
the env on the controller to exercise the new path for homelab
validation; PR 3 flips the default to on once proven, and PR 4 deletes
the legacy executors.

---

## Why this is not a delete-only refactor

The imperative branch is the **only** code that does state-aware
convergence. The Plan-driven path is a *create-and-resume engine*: it
emits the full desired child set and applies each, relying on the
verifying-trace cache to no-op unchanged children. It has none of the
convergence primitives.

Two code families exist today:

- **Plan / dispatcher path** — `Provider.Plan`
  (`internal/controller/providers/k3s/cluster_plan.go`) →
  `applyClusterViaPlan` (`cluster_apply_plan.go`) →
  `ChildDispatcher.ApplyChild` (`internal/controller/operations/childdispatch.go`)
  → per-child resolve `$ref` / verifying-cache / `provider.Apply` / save
  (`operations/dispatcher.go:208`). Used for initial create and for
  resuming a "Provisioning" cluster.
- **Imperative convergence** — `Provider.applyExisting`
  (`internal/controller/providers/k3s/provider.go:280`) →
  `applyCountUp` (`countup.go`), `applyRespecs` (`respec.go`), and the
  `Joiner` (`pkg/k3s/cluster/join.go`). Used for every already-Ready
  cluster (`provider.go:203-209`), including reconciler auto-remediation.

### Gaps in the Plan path (what must be built before deletion)

1. **No diff against current state.** `Plan()` emits children from the
   manifest spec only (`cluster_plan.go:45`, `NodeNames`); it can't tell
   an add from an existing node. The only diffing code is
   `computeChangePlan` (`diff.go:40`).
2. **No delete channel.** `ChildDispatcher` exposes only `ApplyChild`
   (`childdispatch.go:23-25`) — scale-down is impossible through it.
3. **No respec.** A `K3sNode`'s Apply fast-paths on `Installed==true`
   (`node_ops.go:62`), so a cpu/mem change is a silent no-op. Respec
   (destroy+recreate+rejoin) lives only in `applyRespecs` (`respec.go:188`).
4. **No guardrails.** The destructive / catastrophic-quorum gates
   (`catastrophicReason` `diff.go:108`, `catastrophicRespecReason`
   `respec.go:165`, and the `allow-destructive` /
   `i-know-this-breaks-the-cluster` annotation checks `provider.go:294-324`)
   live only in `applyExisting`.
5. **Join semantics differ.** Plan points new nodes' `joinFrom` `$ref` at
   the **index-0** CP's `K3sNode` (`cluster_plan.go:252-267`); count-up
   joins a **surviving** CP (`countup.go:51-70`) and extends the CA
   without rotating it (`countup.go:98-115`, `MintServerCerts`).
6. **State overwrite vs. merge.** The Plan path writes cluster state from
   scratch (`saveClusterStateFromChildren`); `applyExisting` merges
   status/endpoints via `rewriteState` (`provider.go:634`).

### What makes it tractable

The **analysis is already factored out and shared with DryRun** — so we
reuse it and only replace the *executors*:

- `diff.go`: `computeChangePlan`, `changePlan`, `catastrophicReason`
  (shared with `dryrun.go`).
- `respec.go` analysis: `computeSpecRespecs`, `desiredSizeFor`,
  `extractCPUMem`, `catastrophicRespecReason` (shared with `dryrun.go`).
- `clusterBundleDir`, `readChildren`, `loadState`/`saveState`, and the
  `certs.*` bundle helpers (shared with the Plan path).

Only `applyCountUp`, `applyRespecs`, and the `Joiner` are genuinely
legacy-only executors.

---

## Staged PRs

### PR 1 — `DeleteChild` plumbing (no behavior change)

Add `DeleteChild(ctx, manifest)` to the `ChildDispatcher` interface and
`*Dispatcher` (submit/execute a child Delete op → `provider.Delete` +
`manifests` removal), mirroring `ApplyChild`
(`childdispatch.go` + `dispatcher.go`). Pure plumbing, fully
unit-testable, ships independently. **Risk: low.**

### PR 2 — dispatcher-driven convergence executor (behind a gate)

New `convergeClusterViaPlan(ctx, manifest, cd)` in the k3s provider:

1. `Plan()` for desired children (pure, unchanged) + `readChildren` /
   `computeChangePlan` / `computeSpecRespecs` for the diff (reuse shared
   analysis).
2. Enforce the **existing** guardrails (reuse `catastrophicReason` +
   the annotation gates).
3. Execute through the dispatcher:
   - **adds** → `ApplyChild(VM, K3sNode, AgentInstall)`
   - **removes** → `DeleteChild(...)` (workers before CPs)
   - **respecs** → per node, `DeleteChild(K3sNode+VM)` then re-`ApplyChild`,
     one at a time (`deleteK3sNode` clears `Installed` state so re-apply
     reinstalls; `node_ops.go:201`).
4. For adds, point new nodes' `joinFrom` `$ref` at a **surviving** CP's
   `K3sNode` (not blindly index-0); rely on `MintServerCerts` extending
   the on-disk CA bundle.
5. Merge state instead of overwriting.

Gate it (annotation/flag) so it can be A/B'd against `applyExisting`
before cutover. **Risk: medium — the heart of the change.** Unit-testable
with fake providers; the SSH/join paths need homelab.

### PR 3 — cutover + homelab validation

Flip the branch at `provider.go:209` so Ready clusters converge via
`convergeClusterViaPlan`. Validate on homelab against `validate-3`:
count-up, scale-down, and a cpu/mem respec. **Risk: medium — touches the
live path; gated behind homelab sign-off.**

### PR 4 — delete the legacy surface

Remove `applyExisting`, `isProvisioningCluster`, `rewriteState`,
`updateAgentEndpoints` (`provider.go`), all of `countup.go`,
`applyRespecs` (`respec.go`), and all of `pkg/k3s/cluster/join.go`.

**Keep** (shared — do not delete): `diff.go`, the respec *analysis*
helpers, `clusterBundleDir`, `readChildren`, the state read/write
helpers, `certs.*`, and `Creator` / `childops.go` / `ips.go` (still used
by the exec'd plugin `pkg/k3s/handler/handler.go` and the CLI-direct
fresh-create fallback `provider.go:221-271`).

**Risk: low** once PR 3 is validated — mechanical deletion + test cleanup
(`provider_test.go`, `diff_test.go`, `respec_test.go`,
`pkg/k3s/cluster/{create,delete}_test.go`, etc.).

---

## Open question to resolve in PR 2 — legacy-cluster adoption

Retirement assumes every managed cluster has per-node `k3s-nodes/*.yaml`
+ `k3s-agent-installs/*.yaml` state so `joinFrom` refs resolve.

- **Controller-applied clusters already do** — every controller Apply
  carries a `ChildDispatcher` (`dispatcher.go:260`), so initial create
  always went through `applyClusterViaPlan`. `validate-3` is plan-native.
- Only a **CLI-direct apply that bypasses the controller** uses the
  legacy fresh-create fallback and lacks that state.

If the CLI-direct path is not reachable in practice (the CLI talks to the
controller), there is **no adoption step**. If it is, PR 2 gains a small
"synthesize per-node state from the cluster YAML + on-disk CA bundle +
a one-time token fetch" adoption step. **Verify this early in PR 2.**

---

## On-disk state (reference)

Rooted at `~/.openctl/` (`stateDir` = `~/.openctl/state/k3s`):

| Path | Notes |
|---|---|
| `state/k3s/<cluster>.yaml` | Cluster state: spec, `status.outputs.agent.endpoints`, `children`. Read/written by the whole existing-cluster path. |
| `state/k3s/<cluster>/` | Per-cluster CA bundle (`ca.pem`, `client.*`, per-node server certs). **Shared** with the Plan path. |
| `k3s/<cluster>/kubeconfig` | kubeconfig; `get-kubeconfig` action. |
| `state/k3s-nodes/<name>.yaml` | **Plan path only** (K3sNode). |
| `state/k3s-agent-installs/<name>.yaml` | **Plan path only** (AgentInstall). |
