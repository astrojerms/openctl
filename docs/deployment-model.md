# Deployment model: workloads, GitOps, and the unified cross-layer graph

Status: **design agreed, Phase 1 scoped.** This doc records how openctl grows from
an infrastructure control plane into a *unified* control plane that also sees —
and selectively manages — the workloads running on the infrastructure it
provisions.

## The problem

openctl owns **Layer 0 (substrate):** Proxmox hosts, VMs, k3s clusters,
Cloudflare DNS + Tunnels, secrets. It does **not** own **Layer 1 (in-cluster
workloads):** the apps and platform services (ingress, cloudflared, MinIO,
NATS, blogs, …). Today that's Argo/Flux/Helm territory.

Tools like ArgoCD live *inside* the cluster boundary — they cannot see the VM a
node runs on, the Proxmox host under it, or the Cloudflare tunnel in front. The
value openctl can uniquely provide is the **vertical graph**:

```
blog pod → Deployment → home cluster → K3sNodes → VMs → pve2 host
                                    └→ Cloudflare Tunnel → DNSRecord (blog.you.dev)
```

Click the blog, walk the whole stack down to metal and out to the edge. No other
tool spans those layers.

## Key insight: openctl is already a GitOps engine, just not cluster-scoped

Strip Argo down and it is: git → reconcile → detect drift → show status. openctl
already has that loop (git disk-mirror + `repo:pull`, reconciling controller,
drift, plan/dry-run, state, secrets). The only difference is scope. So the
unified tool is **not** a second product — it is openctl's existing reconcile
loop growing a **workload provider**.

And purpose-built Helm is tractable — *not* "reimplement Argo" — because **the
Helm SDK is the per-release reconciliation engine.** `action.Install/Upgrade/
Uninstall` already do diff, ownership, prune-on-upgrade, hook ordering, and
wait-for-ready. openctl orchestrates *releases* as resources and inherits Helm's
engine under each one. Because **Helm stores release state in-cluster** (as
secrets), the provider is nearly stateless — Get reads live status back, like the
Proxmox provider.

## Design principles

1. **Minimize reinventing Argo.** The unique value is the cross-layer graph and a
   single reconcile loop, not better-than-Argo k8s reconciliation. Let Helm's SDK
   do the per-release heavy lifting; let Argo do pure-app GitOps.
2. **Seam = infra coupling.** Manage **natively** anything wired to
   openctl-managed infra (cloudflared needs the tunnel token openctl issued;
   ingress fronts services behind the tunnel; external-secrets reads the Vault
   openctl configured). **Aggregate via Argo** everything with no infra coupling
   (blogs, driftless workers, media).
3. **Opt-in, never default.** The infra-coupled platform is opinionated. openctl
   ships the *capability*; nothing deploys until a component is explicitly
   enabled.
4. **Purpose-built, not tfhost.** The k8s/Helm provider is native (Helm SDK +
   client-go). `tfhost` stays for real metal/cloud providers (AWS, …).
5. **Argo: create-optional, read-baseline.** openctl *can* declaratively register
   Argo `Application`s, but the baseline is read/aggregate so it adopts an
   existing Argo setup without owning it.

## The native k8s provider

- **New external plugin `plugins/k8s`** (separate module, like `plugins/k3s`) so
  Helm's large dependency tree stays out of the controller binary.
- **Kinds** under `k8s.openctl.io/v1`:
  - `HelmRelease` — *managed.* Apply = `helm upgrade --install`; Get = release
    status + workload readiness; Delete = uninstall; List = releases in a
    namespace. Backed by the Helm Go SDK (`helm.sh/helm/v3`).
  - `Manifest` — *managed.* Server-side-apply of raw YAML (glue: Namespaces,
    ConfigMaps, and — when openctl registers them — Argo `Application` CRs).
  - `ArgoApplication` — *observed/read.* Surfaces Argo app health + sync + managed
    resources into openctl for the unified graph.
- **Cluster credentials, generic by design.** A release/manifest just needs a
  kubeconfig, so the provider accepts a kubeconfig from either source:
  - `spec.kubeconfigPath` resolved via `$ref` from the k3s Cluster's produced
    `status.outputs.kubeconfigPath` — the convenient path for openctl-managed
    clusters (the `$ref` also DAG-orders the release after the cluster):

    ```yaml
    kubeconfigPath:
      $ref: { apiVersion: k3s.openctl.io/v1, kind: Cluster, name: edge,
              field: status.outputs.kubeconfigPath }
    ```

    openctl resolves the `$ref` before the provider runs and the plugin reads
    that file; only the path (never the kubeconfig bytes) is stored.
  - `spec.kubeconfig: {$secret: …}` → an explicit kubeconfig for an
    **external/non-openctl** cluster (e.g. a managed EKS). Free once the provider
    is kubeconfig-generic; supported from the start.

  > Note: `$ref` is openctl's real cross-resource marker (`{$ref: {apiVersion,
  > kind, name, field}}`); the `valueFrom`/`spec.cluster` shorthands used
  > elsewhere in early drafts of this doc are not implemented markers.
- **Values + secrets** reuse openctl's `$secret` markers, resolved in the
  transient apply path so they never hit git. This is how the cloudflared token
  (issued by the openctl `Tunnel`) flows into chart values.
- **Chart sourcing: HTTP repos and OCI registries** (the Helm SDK's registry
  client handles `oci://`), since charts are increasingly OCI-distributed.

## Argo aggregation

- **Bootstrap** Argo as an opt-in platform component (the `argo-cd` chart via
  `HelmRelease`).
- **Read** `Application` CRs per managed cluster → `ArgoApplication` observed
  resources (health, sync, managed resources) for the graph. openctl does *not*
  reconcile them — Argo does.
- **Optionally create** Applications declaratively via `Manifest` (an
  `Application` CR pointing at a git repo). Baseline is read-only; creation is
  opt-in.

Open point: Argo apps are per-cluster, but openctl providers `List` a kind
globally. Aggregation likely hangs Argo apps under each managed `Cluster` (as
children/status) rather than a global `List(ArgoApplication)`. Resolved in the
aggregation phase.

## Opt-in platform bundles

A `Platform` composite (Planner) with per-component enable flags that fans out
into `HelmRelease`s — **nothing on by default**:

```yaml
kind: Platform
metadata: { name: edge-platform }
spec:
  cluster: { $ref: Cluster/edge }
  traefik:         { enabled: true }                       # ingress
  cloudflared:     { enabled: true, tunnel: { $ref: Tunnel/edge } }
  externalSecrets: { enabled: false }
  argocd:          { enabled: false }
```

- **Traefik** is the ingress choice (Helm-native, no Cilium-CNI dependency;
  ingress-nginx is in maintenance mode). Pattern: cloudflared → one Traefik
  service → standard k8s `Ingress` objects per site, so the tunnel isn't
  reconfigured per app.
- **cloudflared** wires the `Tunnel`'s `get-token` as a `$secret` into the chart
  values — closing the tunnel→cloudflared loop into one graph.

## The unified cross-layer graph

Extend openctl's existing composition/children graph:

- **Upward:** `HelmRelease` / `ArgoApplication` → the k8s resources they manage
  (Deployments, Services, Pods) with health.
- **Downward / sideways:** workload → `Cluster` → `K3sNode`s → `VM`s → Proxmox
  host, and workload → `Tunnel` → `DNSRecord`.

This is the payoff and the differentiator.

## Phased plan

Each phase is an independently shippable, CI-green PR.

| Phase | Scope | Deliverable |
|------|-------|-------------|
| **1** | `plugins/k8s` + `HelmRelease` (Helm SDK CRUD against a kubeconfig; HTTP+OCI charts; `$secret` values; health via wait) | Deploy any chart to any cluster |
| **2** | Cross-layer credential resolution: `spec.cluster {$ref}` → kubeconfig; DAG-order release after cluster | Workloads target openctl clusters natively |
| **3** | `Manifest` kind (server-side apply of raw YAML) | Escape hatch for non-Helm bits + Argo CRs |
| **4** | Opt-in `Platform` composite; first components **Traefik** + **cloudflared** (token via `$secret`) | Infra-coupled platform, opt-in, tunnel loop closed |
| **5** | Argo bootstrap (chart) + read `Application`s → `ArgoApplication`; optional declarative create | Pure-app GitOps visible in openctl |
| **6** | Unified cross-layer graph in the UI | Click a workload, walk to metal + edge |

## Testing strategy

Helm/k8s operations need a live API server:

- **`envtest`** (k8s API server binary, no kubelet) — fast CI integration on
  release *logic* (install/upgrade/uninstall, values rendering, status).
- **k3d** (containerized k3s) — gated full e2e: real workloads, real readiness,
  Traefik/cloudflared wiring.
- **Homelab k3s** — manual metal validation via the existing `/validate-homelab`
  harness. Tunnel/cloudflared e2e is inherently metal-gated.

## Open design points

- Argo app enumeration is per-cluster (see Aggregation, above).
- `Manifest` prune/ownership: start conservative (one manifest = tracked
  object), grow later.
- Controller ↔ cluster reachability: assumes the controller can hit each
  cluster's API (true on a homelab LAN; revisit for remote clusters).

---

## Phase 1 — detailed scope

**Goal:** a native `plugins/k8s` external provider that deploys and manages Helm
releases against an explicitly-supplied kubeconfig. No openctl-cluster `$ref`
resolution yet (Phase 2), no platform bundles (Phase 4). This is the engine.

### Module layout (mirrors `plugins/k3s`)

```
plugins/k8s/
  go.mod                       # module github.com/openctl/openctl-k8s; replace => ../..
  go.sum
  cmd/openctl-k8s/main.go      # pluginproto.Serve(newProvider())
  internal/helm/client.go      # Helm SDK wrapper: install/upgrade/get/uninstall/list
  provider.go                  # pluginproto Handler; HelmRelease dispatch
  schema.go                    # CUE schema for HelmRelease
  provider_test.go             # against envtest / a fake action config
  README.md
```

Separate module because the Helm SDK + client-go dependency tree is large; CI
gains `plugins/k8s` vet/staticcheck/lint steps (mirroring proxmox/k3s).

### `HelmRelease` spec (Phase 1 surface)

```yaml
apiVersion: k8s.openctl.io/v1
kind: HelmRelease
metadata: { name: podinfo }
spec:
  kubeconfig: { $secret: edge-kubeconfig }   # explicit for now (Phase 2 adds cluster $ref)
  namespace: demo
  createNamespace: true
  chart:
    repo: https://stefanprodan.github.io/podinfo   # or oci://ghcr.io/…
    name: podinfo
    version: "6.7.0"
  releaseName: podinfo        # defaults to metadata.name
  values:                     # map[string]any; $secret resolved by openctl
    replicaCount: 2
  wait: true                  # wait for resources ready
  timeout: "5m"
```

### Provider behavior

- **Configure** (pluginproto): the plugin needs no global config; kubeconfig
  arrives per-resource in the spec. (Provider-level defaults optional later.)
- **Apply**: build a Helm `action.Configuration` from the spec's kubeconfig +
  namespace; `upgrade --install` the chart with resolved values;
  `createNamespace` if set; honor `wait`/`timeout`. Return observed status
  (release name, revision, chart version, deployed status, resource readiness).
  Helm stores release state in-cluster → openctl state stays minimal.
- **Get**: read the release (`action.Get`/status) + assess workload readiness;
  `NotFound` when the release is absent.
- **List**: `action.List` in the namespace.
- **Delete**: `action.Uninstall`; idempotent (already-absent = deleted).
- **Chart resolution**: HTTP repo (download + load) and OCI (`registry.Client`
  pull). Version pinned; `latest` allowed but discouraged.

### Helm SDK mapping

| openctl | Helm SDK |
|---------|----------|
| Apply (create/update) | `action.NewUpgrade` with `Install=true`, or `action.NewInstall` first-time |
| Get | `action.NewGet` / `action.NewStatus` |
| List | `action.NewList` |
| Delete | `action.NewUninstall` |
| Health | `Wait`/`WaitForJobs` on install/upgrade; readiness from release status |

`action.Configuration.Init(RESTClientGetter, namespace, driver, log)` where the
`RESTClientGetter` is built from the spec's kubeconfig bytes.

### Secrets

`spec.values` and `spec.kubeconfig` accept `$secret`; openctl
resolves them in the transient apply path (never persisted to git), then the
plugin receives concrete values. No plaintext kubeconfig or token on disk/in
git.

### Testing

- **Unit:** chart loading (HTTP + OCI via a local registry), values merge,
  spec→Helm-options mapping, error mapping (missing chart, bad kubeconfig →
  descriptive errors; absent release → NotFound).
- **Integration (envtest, gated):** install `podinfo` → Get shows deployed →
  upgrade changes values → Delete → Get NotFound.
- **Metal (manual):** deploy to the homelab k3s (`dev` cluster's kubeconfig).

### Explicitly out of scope for Phase 1

- `spec.cluster {$ref: Cluster/…}` resolution (Phase 2).
- `Manifest` / `ArgoApplication` kinds (Phases 3/5).
- Platform bundles / Traefik / cloudflared (Phase 4).
- Drift *diff* rendering (reconcile re-applies to converge; a rich diff view is
  later).
