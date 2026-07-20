# plan-on-PR (I1)

A reusable GitHub Actions workflow that runs [`openctl plan`](../../internal/cli/plan.go)
on every pull request touching your manifests and posts the apply order + `$ref`
dependency graph as a **sticky PR comment** — so a reviewer sees *what will
happen, in what order, and what waits on what* before merge.

This is the PR-gated front half of the DriftlessAF loop: the PR shows the plan;
the controller reconciles on merge (via `manifests.gitops` pull/webhook — see
[../../docs/gitops-modes.md](../../docs/gitops-modes.md)).

## Adopt it

1. Copy [`plan.yml`](plan.yml) into your **infra** repo at
   `.github/workflows/openctl-plan.yml`.
2. Set `MANIFESTS` (in the workflow `env`) to where your manifests live
   (default `manifests`). It accepts a directory (walked recursively) or
   space-separated `-f` paths.
3. Adjust the `on.pull_request.paths` globs to match your layout.
4. Optionally pin `go install …/openctl@vX.Y.Z` to a release for reproducible
   checks.

That's it — no controller connection, no secrets. The workflow needs only
`pull-requests: write` (already declared) to post the comment.

## What the comment shows

```
### openctl plan — ✅ plan computed

Plan: 11 resource(s), 2 wave(s)

Apply order:
  wave 1:
    Cluster/home
    Tunnel/home
  wave 2:
    DNSRecord/chat  ← Tunnel/home
    HelmRelease/ollama  ← Cluster/home
    Platform/home  ← Cluster/home
    ...

External references (must already exist — not in this set):
    HelmRelease/ollama → Cluster/home
```

- **Waves** are topological levels — resources in one wave have no
  inter-dependency and apply concurrently.
- **`← Kind/Name`** is what a resource waits on (a `$ref` into another resource
  in the set).
- **External references** are `$ref` targets *not* in the changed set — they
  must already exist in the cluster.

## Gating

`openctl plan` exits non-zero on a **dependency cycle**, which fails the check
and blocks the merge (the comment shows the offending resources).

## Scope

This is the **offline** preview: ordering + the dependency graph. Per-resource
dry-run **diffs** (spec drift vs the live cluster) are server-only — they need a
running controller and its `DryRunApply` — and are intentionally not part of
this workflow. See the K7 notes in [../../ROADMAP.md](../../ROADMAP.md).
