# k3s cluster-wide rolling upgrade — design proposal

**Status:** proposal, awaiting sign-off. Not implemented.
**Author:** autonomous session, 2026-07-05.
**Roadmap item:** "Cluster-wide rolling upgrade (drain/cordon ordering) remains
a follow-up" (ROADMAP.md, plugin-defined CLI subcommands entry). Builds on the
already-shipped **per-node** `upgrade`.

This designs the orchestration that upgrades every node in a k3s cluster to a
target version safely and in order, on top of the shipped per-node upgrade
primitive. Fully autonomous to build; validation wants a multi-node cluster
(homelab), which is the one hardware gate.

## What already exists

- **Per-node upgrade** (`pkg/k3s/agent/upgrade.go`): the openctl-k3s-agent
  exposes `POST /v1/upgrade/k3s` which downloads the target k3s binary for the
  node's arch, verifies its published sha256, atomically swaps it into place,
  and restarts k3s. Exposed as the `openctl k3s upgrade` plugin subcommand
  against one node's agent client.
- **Node ordering precedent** (`provider.go:399-401`): teardown removes workers
  before control planes so schedulable capacity drops before apiserver
  replicas — established **without kubectl drain** ("homelab assumption").
- **Node inventory**: the Cluster's children (VMs / K3sNodes / AgentInstalls)
  and per-node state under `state/k3s-nodes/` give the full node list with
  roles, so the orchestrator knows what to upgrade and in what role-order.

So the missing piece is purely **orchestration**: iterate the nodes in a safe
order, call the existing per-node upgrade on each, and gate progress on health.

## Proposed model

A cluster-level action `upgrade` (or `openctl k3s upgrade --cluster <name>
--version <v>`) that:

1. **Enumerates nodes** from the cluster's children, partitioned into control
   planes and workers.
2. **Upgrades control planes first, one at a time**, oldest-first, waiting for
   each to rejoin healthy (apiserver responding, node `Ready`, etcd member
   healthy) before the next. Serial CP upgrade preserves etcd quorum — the
   non-negotiable safety property of an HA cluster.
3. **Then upgrades workers**, one at a time (or a small configurable batch),
   waiting for `Ready` between each.
4. **Health gate between every step** reuses the existing reachability/health
   probe the cluster apply already uses for success criteria. A node that
   fails to come back healthy **halts** the upgrade (default) rather than
   marching on and taking down quorum.
5. **Idempotent / resumable**: a node already at the target version is skipped,
   so re-running after a halted upgrade continues from where it stopped.

Version target validation (is `<v>` a real k3s release for this arch?) reuses
the per-node upgrade's existing sha256-manifest fetch — a bad version fails
fast on the first node instead of mid-cluster.

## Decisions (each needs a call; recommendation given)

### 1. Drain or no-drain? (the core tension)

The cluster deliberately does **no kubectl drain** (homelab assumption:
workloads tolerate a brief node restart; the operator isn't running
drain-sensitive production workloads). A rolling upgrade restarts k3s on each
node in turn, briefly disrupting pods there.

- **(Recommended) Keep no-drain by default; make drain opt-in.** Match the
  existing cluster stance: upgrade restarts each node's k3s without cordon/drain,
  accepting a brief per-node blip. Add an opt-in `--drain` (cordon → drain →
  upgrade → uncordon) for operators who *do* run disruption-sensitive
  workloads. Default-off keeps the homelab assumption intact and avoids pulling
  a kubectl/k8s-client dependency into the default path.
- Alternative: always drain — safer for real workloads, but reverses the
  documented assumption, needs a k8s client (cordon/drain API or `kubectl`),
  and is slower. Wrong default for the stated audience.

**This is the decision that gates the whole feature's shape** — everything else
follows from it. The recommendation keeps v1 small (orchestrate the shipped
primitive) and defers the k8s-client dependency to the opt-in path.

### 2. Serial vs. batched workers

- **(Recommended) Serial by default, optional `--worker-batch N`.** One worker
  at a time is safest and simplest; a batch knob helps large clusters where
  serial is slow. Control planes are **always** serial (quorum).

### 3. Failure policy

- **(Recommended) Halt on first unhealthy node.** Stop the upgrade, leave the
  cluster in a mixed-version-but-running state, surface which node failed. A
  mixed-version cluster runs fine short-term; marching past a failed CP risks
  quorum. Re-run (idempotent) after fixing the node to finish.

### 4. Where the orchestration lives

- **(Recommended) A cluster-level `Actioner` action** on the k3s Cluster
  provider (`Actions("Cluster")` gains `"upgrade"`; `DoAction` runs the
  orchestration), reusing the per-node agent client per node. Surfaces in the
  UI action bar for free (like VM start/stop) and via
  `openctl ctl invoke-action`. The existing per-node `openctl k3s upgrade`
  subcommand stays for single-node use.

### 5. Progress visibility

- **(Recommended) Emit child ops per node** (like composite apply does), so the
  ops drawer shows "upgrade cp-0 → cp-1 → … → worker-n" with per-node
  status. Reuses the recorder/child-op machinery already on the dispatcher ctx.

## Implementation sketch

- k3s Cluster provider: `Actions` advertises `"upgrade"`; `DoAction("upgrade",
  …)` parses the target version, enumerates nodes from children, and runs the
  CP-then-worker serial loop, calling the per-node agent upgrade + health probe,
  recording a child op per node.
- Health gate: reuse the cluster-apply reachability/health check per node.
- `--drain` (opt-in): a cordon/drain step via a k8s client, gated behind the
  flag so the default path takes no new dependency.
- Idempotency: skip a node already reporting the target version (agent `info`
  already reports the running k3s version).

## Testing plan (multi-node cluster only for the final gate)

- Order: a fake node inventory (3 CP + 2 workers) upgrades CPs before workers,
  serially, in a deterministic order.
- Health gate: an injected unhealthy node halts the loop and leaves later nodes
  un-upgraded; the failure names the node.
- Idempotency: nodes already at the target version are skipped; a re-run after a
  simulated halt resumes.
- Version validation: a bogus version fails on the first node before any real
  swap.
- The **one** gate needing hardware: a real multi-node homelab cluster upgrading
  end-to-end with pod continuity — rollout validation, not a unit test.

## Non-goals

- Automatic rollback on failure (halt + operator re-run is v1; rollback is a
  larger, separate feature — k3s downgrades are not always clean).
- Drain-by-default (opt-in only, per decision 1).
- Coordinated add-on/CRD upgrades — this covers the k3s binary on each node.
- Canary / partial-cluster version pinning.

## Rollout

Ship the no-drain serial orchestration (reuses the shipped per-node upgrade,
no new default dependency). Unit-test the ordering / health-gate / idempotency
layer with a fake inventory. Validate on a multi-node homelab cluster (the
hardware gate). Add opt-in `--drain` as a fast follow if a real workload needs
it. The per-node `openctl k3s upgrade` remains for single-node use.
