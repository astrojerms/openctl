# openctl Quickstart

A practical walkthrough for getting a k3s cluster up and managing it. For
architecture and design rationale, see [DESIGN.md](DESIGN.md).

## Prerequisites

- Go 1.25+ on the build host.
- `openssl`, `nmap` (optional, used in troubleshooting recipes below).
- A Proxmox VE host with a cloud-init enabled template (e.g.
  `tpl-jammy-server-cloudimg-amd64`) and an API token.
- An SSH key on the build host (`~/.ssh/id_ed25519` or similar) whose public
  key cloud-init will inject into nodes.

## Install

```sh
make install
```

This builds and installs:

- `openctl` → `$GOBIN` or `/usr/local/bin/`
- `openctl-controller` → `$GOBIN` or `/usr/local/bin/` (Phase 1+ of the
  controller rollout — see [CONTROLLER.md](CONTROLLER.md))
- `openctl-proxmox`, `openctl-k3s` → `~/.openctl/plugins/`
- `openctl-k3s-agent-linux-{amd64,arm64,armv7}` → `~/.openctl/plugins/k3s-agents/`

The agent binaries live in a subdirectory so their `openctl-*` filenames
don't get picked up by plugin discovery.

## Running the controller

The controller is the persistent reconciler. From Phase 4 onward, both
proxmox and k3s providers are compiled into the controller binary; the
CLI's `openctl ctl ...` commands route through it. The legacy exec-plugin
commands still work for transition, and graduate to top-level after
Phase 6.

### Install as a per-user LaunchAgent (macOS, recommended)

```sh
make build
./bin/openctl-controller install --local
```

This copies the controller into `~/Library/Application Support/openctl/bin/`,
writes a LaunchAgent plist at `~/Library/LaunchAgents/io.openctl.controller.plist`,
loads it (RunAtLoad + KeepAlive), verifies the controller comes up, and
seeds `~/.openctl/config.yaml` with a controller section if missing. Logs
land at `~/Library/Logs/openctl/controller.{out,err}.log`.

To remove:

```sh
openctl-controller uninstall          # leaves state intact
openctl-controller uninstall --purge  # also removes ~/.openctl/controller
```

### Foreground run (development)

```sh
# Defaults: 127.0.0.1:9444, state in ~/.openctl/controller/
openctl-controller serve

# In another terminal:
openctl ping
# → ok: echo="ping" server-version=0.1.0-controller
```

On first start the controller generates a self-signed CA + server cert
under `~/.openctl/controller/tls/` and a random API token at
`~/.openctl/controller/token`. Both persist across restarts. The CLI reads
them automatically from the same default paths; override via the
`controller:` section in `~/.openctl/config.yaml`.

## Configure

Create `~/.openctl/config.yaml`:

```yaml
defaults:
  output: table
  timeout: 300

providers:
  proxmox:
    default-context: homelab
    contexts:
      homelab:
        endpoint: https://pve.example.com:8006
        node: pve1
        credentials: homelab-creds
    credentials:
      homelab-creds:
        tokenId: root@pam!openctl
        tokenSecretFile: ~/.openctl/secrets/proxmox.token
```

Put the API token in the `tokenSecretFile` path (mode `0600`).

## Write a cluster manifest

Use [`examples/cluster.yaml`](examples/cluster.yaml) as a template. Local
manifests with environment-specific values (real IPs, SSH keys) belong at
`.cluster.yaml` in the repo root — gitignored.

Minimum viable shape:

```yaml
apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: dev
spec:
  compute:
    provider: proxmox
    image:
      template: tpl-jammy-server-cloudimg-amd64
      storage: local-lvm
    default: { cpus: 2, memoryMB: 4096, diskGB: 30 }
  nodes:
    controlPlane: { count: 1 }
    workers:
      - { name: worker, count: 1 }
  network:
    bridge: vmbr0
    staticIPs:
      startIP: 192.168.1.100
      gateway: 192.168.1.1
      netmask: "24"
  k3s:
    extraArgs: ["--disable=traefik"]
  ssh:
    user: ubuntu
    privateKeyPath: ~/.ssh/id_ed25519
    publicKeys:
      - ssh-ed25519 AAAA... user@host
```

Static IPs are recommended over DHCP — the proxmox plugin reads VM IPs
via the QEMU guest agent, which isn't installed in stock cloud images.

## Cluster lifecycle

```sh
# Create — long timeout matters; the 300s default cuts off mid-install
openctl --timeout 1200 k3s create clusters -f .cluster.yaml

# List / inspect — both probe live agent state on every call
openctl k3s get clusters
openctl k3s get clusters dev -o yaml         # full per-node detail

# Teardown
openctl k3s delete clusters dev
```

`get clusters dev -o yaml` returns the saved state plus a live `nodes[]`
array showing each node's reachability, k3s status, agent version, distro,
kernel, init system, and capabilities. Unreachable nodes are marked
`reachable: false` and the cluster `health` becomes `degraded` — the call
still succeeds.

## kubectl

The plugin writes a kubeconfig to `~/.openctl/k3s/<cluster>/kubeconfig`:

```sh
export KUBECONFIG=~/.openctl/k3s/dev/kubeconfig
kubectl get nodes -o wide
kubectl get pods -A
```

## Direct agent endpoints

The agent's `logs` and `service control` endpoints don't have a CLI surface
yet (see DESIGN.md "Plugin-defined CLI subcommands"). For now, `curl`
directly with the cluster's mTLS material:

```sh
DIR=~/.openctl/state/k3s/dev
NODE_IP=192.168.1.100   # from get clusters -o yaml → status.outputs.agent.endpoints

# Host facts + k3s status + capabilities
curl --cacert $DIR/ca.pem --cert $DIR/client.pem --key $DIR/client.key \
     https://$NODE_IP:9443/v1/info

# k3s journald logs (lines defaults to 100, max 5000)
curl --cacert $DIR/ca.pem --cert $DIR/client.pem --key $DIR/client.key \
     "https://$NODE_IP:9443/v1/logs/k3s?lines=500"

# Restart k3s on a node
curl -X POST --cacert $DIR/ca.pem --cert $DIR/client.pem --key $DIR/client.key \
     https://$NODE_IP:9443/v1/service/k3s/restart
```

The agent picks the right systemd unit automatically (`k3s` on control
planes, `k3s-agent` on workers).

## Troubleshooting

**Cluster create fails or times out partway.** State may not have been
saved (the plugin's `StateUpdate` response gets cut off). Check Proxmox
directly and clean up by hand:

```sh
openctl proxmox get vms
openctl proxmox delete vms <name>             # repeat for each VM
rm -rf ~/.openctl/state/k3s/<cluster>         # cert bundle dir
```

**Worker VM boots but sshd never comes up.** Almost always an IP collision
— the static IP we tried to claim was already taken by another device.
The VM's cloud-init network config silently fails. Probe the range first:

```sh
for i in $(seq 100 130); do
  ping -c1 -W200 192.168.1.$i >/dev/null 2>&1 \
    && echo "$i: in use" || echo "$i: free"
done
```

Then update `network.staticIPs.startIP` to a stretch of consecutive free
addresses (one per node).

**`openctl k3s create` errors with `plugin execution timed out`.** Pass
`--timeout 1200` (20 minutes). The default 300s isn't enough for a real
cluster create — VMs need to boot, k3s needs to install, and the agent
verification step polls each node.

**Tearing down a stopped VM says it was deleted but it still appears in
`get vms`.** Run `delete vms <name>` again — the first call stops it,
the second removes it. (Proxmox-side eventual consistency.)

## What's next

See [DESIGN.md](DESIGN.md):

- "K3s Plugin Agent" — the agent architecture, mTLS model, OS
  heterogeneity strategy.
- "TODO: Plugin-defined CLI subcommands" — the deferred CLI surface for
  `openctl k3s logs/restart/upgrade`. Includes the recommended protocol
  extension and file-by-file change list when we're ready to ship it.
