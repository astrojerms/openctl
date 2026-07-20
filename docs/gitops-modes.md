# GitOps: two source-of-truth modes (K2 design)

**Status:** design (invariants agreed; code reconciliation is the follow-up).
**Owner:** roadmap K2. Depends on K3 (provenance `source` column — shipped).

## Problem

openctl's git integration grew in two directions that now overlap:

- **git-as-sink** (original): SQLite is the source of truth. After every apply
  the controller *materializes* the desired spec to a disk mirror
  (`manifests.dir`), auto-commits it (`manifests.git`), and optionally pushes.
  On startup it *re-materializes* any missing mirror files so the audit trail
  is complete.
- **git-as-source** (B1–B3): the repo is the source of truth. The controller
  *pulls* a remote (`manifests.gitops.pull`), applies changed files, *prunes*
  resources whose file left the repo (`pull.prune`), and converges immediately
  on a push webhook.

Both sets of machinery can be enabled at once, and they interact badly:

1. **Startup re-materialize fights prune.** Prune deletes resources whose file
   was removed from the repo; startup re-materialize re-creates mirror files
   from SQLite — so a pruned resource's file can reappear and the resource comes
   back.
2. **`--ff-only` pull fights auto-commit.** git-as-source pulls with
   `--ff-only`; git-as-sink auto-commits desired specs on every apply. A local
   auto-commit makes the next `--ff-only` pull fail (diverged history).
3. **Ambiguous truth.** With both on, "what should exist" has two authorities
   (SQLite and the repo) with no rule for which wins.

The machinery — disk mirror, auto-commit, push, pull, watcher, prune, webhook,
plus the always-on drift reconciler — is individually fine; the problem is that
nothing says *which combination is coherent*.

## Goal

Collapse the combinations into **two explicit, non-overlapping modes**, selected
by **one config switch**, where only the machinery for the chosen mode runs.
Each mode has a single, unambiguous source of truth and no self-conflicting
components.

Non-goals: changing the reconcile/apply pipeline itself; changing the drift
reconciler (it runs in both modes); a UI for switching modes.

## The two modes

| Aspect | **mirror** (SQLite is truth) | **gitops** (the repo is truth) |
|---|---|---|
| Source of truth | SQLite `applied_manifests` | the git repo (working tree in `manifests.dir`) |
| git role | read-only **audit mirror** of what was applied | the **input** — authored externally, pulled in |
| Materialize desired spec → disk on apply | **yes** | **no** (the file already exists in the repo) |
| Auto-commit on apply/delete | **yes** | **no** (would fight `--ff-only` pull) |
| Push to remote | **yes** (`onCommit`/`periodic`/`manual`) | **no** (the repo is authored upstream) |
| Startup: re-materialize missing files | **yes** (audit completeness) | **no** (prune must be able to remove them) |
| Pull remote → apply | **no** | **yes** (interval + webhook) |
| Prune (repo = desired SET) | **no** | **yes** (opt-in; heavily guarded) |
| Push webhook | **no** | **yes** (opt-in) |
| fsnotify local file → apply | **no** | **yes** (edit a file, it applies) |
| Drift reconciler (periodic re-apply of stored desired) | **yes** | **yes** |

`mirror` is today's default behavior (one-way sink + optional push). `gitops`
is the DriftlessAF loop.

## Invariants

1. **Exactly one mode is active** for a given controller. No "both."
2. **mirror never reads from git.** No pull, no prune, no webhook, no
   file→apply. git is written, never read as intent.
3. **gitops never writes desired specs to git.** No auto-commit of applied
   manifests, no push of desired state. The controller may still write
   *operational* artifacts elsewhere (op history in SQLite), but the repo's
   desired specs are authored by humans/CI, not the controller.
4. **gitops does not revive files on startup.** A file absent from the working
   tree is a signal ("this resource is no longer desired"), not a gap to fill.
   Reviving it would resurrect a pruned resource (conflict #1) — forbidden.
5. **The drift reconciler is mode-agnostic.** It re-applies the *stored* desired
   state (`applied_manifests`) on drift, in both modes. In gitops mode that
   stored state is simply whatever the last pull/apply wrote — so the reconciler
   converges toward the repo, transitively, without a second code path.
6. **gitops prune trusts provenance.** Prune only deletes resources whose last
   apply was `source=gitops` (or unknown), never `cli`/`ui`. This is now a
   durable column read (K3), not an ops-table reconstruction — so provenance
   survives op GC and prune stays safe.

## Config

Add one switch; keep the existing sub-blocks but scope them to a mode.

```yaml
manifests:
  dir: ~/.openctl/manifests
  mode: mirror            # mirror (default) | gitops

  # mirror-mode settings (ignored in gitops mode):
  git:
    enabled: true         # auto-commit the audit mirror
    remote: git@github.com:me/infra-audit.git
    pushMode: onCommit

  # gitops-mode settings (ignored in mirror mode):
  gitops:
    remote: git@github.com:me/infra.git   # the source repo
    pull:
      interval: 1m
      prune: true
      webhook:
        enabled: true
        secret: { $secret: { provider: env, key: WEBHOOK_SECRET } }
```

- `mode` defaults to **`mirror`** — the current default behavior, so existing
  configs are unchanged.
- **Contradictory combinations are rejected at config load** with a clear
  error, rather than silently half-working. Examples:
  - `mode: gitops` with `git.pushMode`/`git.enabled` set → error ("gitops mode
    does not auto-commit or push desired specs; remove `manifests.git`").
  - `mode: mirror` with `gitops.pull` set → error ("pull/prune require
    `mode: gitops`").
  - `mode: gitops` with no `gitops.remote` → error.

### Back-compat / migration

- Absent `mode` → `mirror`. A config that only uses `manifests.git` keeps
  working verbatim.
- A config that currently sets `manifests.gitops.pull.enabled: true` is the one
  breaking case: on load, if `pull` is configured without `mode: gitops`, emit
  a one-line deprecation error telling the operator to set `mode: gitops` (and
  drop any `manifests.git` push settings). This is deliberate — that config was
  the ambiguous one this change exists to disambiguate.

## Code reconciliation plan (the follow-up)

Small, mechanical once the invariants above are fixed. Per component:

1. **Config** (`internal/config`): add `Manifests.Mode` (default `mirror`); add
   `validateManifestsMode()` rejecting the contradictory combos; move
   `gitops.remote` alongside (today the remote lives on `manifests.git`).
2. **Wiring** (`cmd/openctl-controller/main.go`): branch on `mode`. Wire the
   disk-mirror auto-commit + push + startup-revive **only** in mirror mode; wire
   the watcher + pull loop + pruner + webhook **only** in gitops mode. The
   drift reconciler is wired unconditionally.
3. **DiskMirror** (`internal/controller/manifests/disk.go`): gate the startup
   re-materialize behind mirror mode (a constructor flag or a
   `mode`-aware caller). In gitops mode the mirror dir is the *working tree*,
   not a controller-owned output — so nothing re-materializes.
4. **git hook** (`internal/controller/manifests/githook.go`): auto-commit only
   in mirror mode.
5. **Watcher / Repo / Pruner / Webhook**: unchanged internally; simply not
   constructed in mirror mode. (They already exist and are individually tested.)
6. **No change** to the dispatcher, drift reconciler, or `applied_manifests`
   store — the store stays the desired-state cache in both modes; K3's `source`
   column already distinguishes provenance.

## Test plan

- **Config validation**: each contradictory combination errors with the
  documented message; each valid single-mode config loads; absent `mode`
  resolves to `mirror`.
- **Mode selection**: a fake wiring harness asserts that in mirror mode the
  pull/prune/webhook components are nil and the mirror/commit ones are live, and
  vice-versa in gitops mode.
- **Conflict #1 gone**: in gitops mode, deleting a file + pull → the resource is
  pruned and startup does **not** revive its file.
- **Conflict #2 gone**: in gitops mode, an apply does **not** auto-commit, so a
  subsequent `--ff-only` pull succeeds.
- Existing per-component tests (mirror, watcher, pruner, webhook) continue to
  pass unchanged.

## Open question (ties to K5)

Which mode is the *recommended* default is partly a product-scope question (K5:
is openctl infra-IaC-with-bounded-workloads, or a homelab PaaS?). This doc
fixes the *mechanism* regardless: `mirror` stays the safe default (no surprise
deletes, no external repo required), and `gitops` is the opt-in DriftlessAF
loop. The scope decision only changes which mode the docs lead with, not the
invariants here.
