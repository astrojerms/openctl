# openctl-k8s

A native [openctl](../../README.md) provider plugin for deploying **Kubernetes
workloads** — the engine of openctl's unified deployment model (see
[docs/deployment-model.md](../../docs/deployment-model.md)).

It speaks openctl's v2 plugin protocol (`pkg/pluginproto`) and drives the
**Helm Go SDK** directly (no shelling out to `helm`). The Helm SDK is the
per-release reconciliation engine — diff, ownership, prune-on-upgrade,
hook ordering, wait-for-ready — so this provider orchestrates *releases* as
openctl resources and inherits Helm's engine under each one.

## Kinds (Phase 1)

`k8s.openctl.io/v1`

| Kind | Description |
|------|-------------|
| `HelmRelease` | A Helm chart deployed to a cluster. Apply = `helm upgrade --install`, Get = live release status, Delete = uninstall. |

Charts resolve from **HTTP repos** and **OCI registries** (`oci://…`).

The provider is nearly stateless: Helm stores release state **in-cluster** (as
secrets), so `Get` reads it back. openctl's state blob carries only enough to
reconnect (kubeconfig + release coordinates) for `Get`/`Delete`, which don't
receive the spec.

## Example

```yaml
apiVersion: k8s.openctl.io/v1
kind: HelmRelease
metadata:
  name: podinfo
spec:
  kubeconfig: { $secret: edge-kubeconfig }   # resolved by openctl; never hits git
  namespace: demo
  createNamespace: true
  chart:
    repo: https://stefanprodan.github.io/podinfo   # or: oci://ghcr.io/stefanprodan/charts
    name: podinfo
    version: "6.7.0"
  values:
    replicaCount: 2
  wait: true
  timeout: "5m"
```

`spec.values` entries may use `$secret`/`valueFrom` (resolved before the plugin
sees them). This is how, later, a Cloudflare `Tunnel`'s run token flows into a
`cloudflared` chart's values.

> **Phase 1 scope.** Kubeconfig is supplied explicitly (`spec.kubeconfig`).
> Phase 2 adds `spec.cluster: {$ref: Cluster/…}` so an openctl-managed k3s
> cluster's kubeconfig resolves automatically. See the design doc for the full
> roadmap (`Manifest`/`ArgoApplication` kinds, opt-in `Platform` bundles, Argo
> aggregation, the unified cross-layer graph).

## Setup

```sh
make build-plugin-k8s      # -> bin/openctl-k8s
make install-plugins       # copies it to ~/.openctl/plugins/
```

```yaml
# ~/.openctl/config.yaml
providers:
  k8s:
    command: openctl-k8s
    args: [plugin-serve]
```

## Testing

- **Unit** (hermetic, CI): the Helm engine is exercised via Helm's in-memory
  release driver + a fake kube client and a local test chart — real
  install/upgrade/get/list/uninstall logic, no cluster. Plus pure spec/mapping
  tests.
- **e2e** (gated on `KUBECONFIG_E2E`): drives the full provider against a real
  cluster, installing the published `podinfo` chart from **HTTP and OCI**:

  ```sh
  k3d cluster create openctl-e2e
  KUBECONFIG_E2E="$(k3d kubeconfig write openctl-e2e)" \
    go test ./internal/provider/ -run TestE2EHelmRelease -v
  ```

## Security note (Phase 1)

The kubeconfig is stored in openctl's local `provider_state` (SQLite, not git)
so `Get`/`Delete` can reconnect. Phase 2's `Cluster` `$ref` removes the need to
store it by re-resolving from the referenced cluster.
