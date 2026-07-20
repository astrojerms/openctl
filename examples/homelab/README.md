# Homelab: full stack, end to end

A worked example of the whole homelab vision through openctl — a GPU-capable
k3s cluster, its infra layer, and the self-hosted apps — using only primitives
that ship today. Nothing here is bespoke: the cluster is a `k3s.Cluster`, every
app is a stock Helm chart wrapped in a `HelmRelease`, and public exposure is a
`Tunnel` + `DNSRecord`.

## What gets built

| File | Kind(s) | What |
|------|---------|------|
| `01-cluster.cue` | `k3s/Cluster` | Control plane + a **general** pool, a **GPU** pool (passthrough), and a **storage** pool prepped for Longhorn (`nodePrep: open-iscsi`) |
| `02-platform.yaml` | `k8s/Platform` | traefik, cloudflared, longhorn, nfsProvisioner, nvidiaDevicePlugin, and a generic `kube-prometheus-stack` |
| `03-tunnel.yaml` | `cloudflare/Tunnel` | One connector fronting every public hostname |
| `04-dns.yaml` | `cloudflare/DNSRecord` | CNAMEs that `$ref` the Tunnel's `status.cnameTarget` |
| `05-ollama-openwebui.yaml` | `k8s/HelmRelease` | Ollama on the GPU + Open WebUI at `chat.example.com` |
| `06-media-and-services.yaml` | `k8s/HelmRelease` | Jellyfin (NFS), MinIO (Longhorn), Authentik (SSO) |

## The three wiring mechanisms

Everything is glued together by three openctl features — no hand-copied values:

1. **`$ref` for outputs.** Each workload's `kubeconfigPath` `$ref`s the cluster's
   `status.outputs.kubeconfigPath`. openctl resolves it to the on-disk kubeconfig
   *and* schedules the workload **after** the cluster is Ready. Same mechanism
   points each `DNSRecord.content` at the `Tunnel.status.cnameTarget`. Run
   `openctl explain <apiVersion> <kind>` to see what a kind exposes to `$ref`.
2. **`$secret` for credentials.** cloudflared's tunnel token comes from the
   Tunnel's `get-token` action via the `action` secret provider; MinIO/Authentik
   secrets come from Vault. Only the marker is ever persisted — never the value,
   so nothing sensitive lands in git.
3. **Node prep for prerequisites.** The storage pool installs `open-iscsi` on
   first boot (`nodePrep`), so Longhorn works with no manual node touch.

## Apply order

openctl derives ordering from the `$ref` edges, so you can apply the whole
directory and it sorts itself out — but conceptually:

```
01-cluster.cue        # the k3s cluster (VMs → k3s → kubeconfig)
02-platform.yaml      # ingress + storage classes + GPU scheduling
03-tunnel.yaml        # create the Cloudflare tunnel (publishes cnameTarget)
04-dns.yaml           # CNAMEs → tunnel (waits on 03 via $ref)
05/06 *.yaml          # the apps (wait on 02 for storage/ingress)
```

```sh
openctl validate -f examples/homelab/01-cluster.cue   # the cluster schema-checks offline
openctl apply -f examples/homelab/01-cluster.cue
openctl apply -f examples/homelab/                     # the rest
```

## Prerequisites & placeholders

- **Proxmox**: a `local-lvm` (disks) + a snippets-capable storage (`local`),
  and a host `pve-gpu` with an NVIDIA card exposed as the `rtx4090` resource
  mapping. Adjust `nodes:`/`gpu.devices` to your hardware.
- **Cloudflare**: a configured provider credential + zone; replace
  `example.com` with your domain.
- **Vault** (optional): the MinIO/Authentik `$secret`s assume a Vault backend;
  swap for `env`/`file` providers if you don't run one.
- Replace the SSH key, IP range (`192.168.1.x`), NFS `server`/`path`, and chart
  versions with your own. Chart versions are pinned as examples — check for
  current releases.

These are the deploy-today primitives; the point is that the homelab is just
declarative infra + stock charts, wired by `$ref`/`$secret`.
