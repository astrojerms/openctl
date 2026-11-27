# openctl Quickstart

A practical walkthrough for connecting an existing homelab and managing
infrastructure and applications. Kubernetes is optional. For architecture and
design rationale, see [DESIGN.md](DESIGN.md).

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

### Install as a local service (macOS or Linux)

```sh
make build
./bin/openctl-controller install --local
```

macOS uses a per-user LaunchAgent; Linux uses a user systemd service. Both
verify the listener and seed CLI config only when it is missing. For a
remote Linux host, build the matching controller and use:

```sh
make build-controller-linux
./bin/openctl-controller install --target ssh://user@controller --ssh-key ~/.ssh/controller
```

The SSH user needs passwordless sudo. Provider credentials, guest keys and
verified host keys must be installed on the **controller host**. Copy its
controller CA/token securely and point your local CLI config at that controller.
Proxmox controller-VM bootstrap is also supported; see
`openctl-controller install --help`.

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

Only one daemon may open a state directory. Development `--no-auth` requires
loopback gRPC **and** HTTP listeners; use normal authentication for any remote
listener. Provider credentials and global settings require an admin role.

Before relying on the controller, practice
[offline backup and restore](README.md#back-up-and-recover-the-controller).
Stop the daemon first; protect the archive like a private key. Restore into new
paths and review external dependencies before restarting automation.

## Connect And Adopt Before Rebuilding

After configuring Proxmox, restart the controller and open **Discover
infrastructure** in the console. Test the connection, review physical
context/node/VMID/UUID evidence and the observed manifest, then adopt an existing
standalone VM. This records intent without changing the VM. Admin access, an
idle controller and disabled GitOps are required.

The console submits the exact fingerprint shown in that review; a replacement
requires new discovery and consent. Testing a context invalidates that context's
old inventory/review. Session or authority changes clear private reviews and
bundles; leaving the page does not roll back already-accepted tracking.
For CLI adoption of a plain YAML/CUE manifest, pass the full reviewed discovery
`identity` with `--expected-identity`; a UUID alone is not the fingerprint.
Migration bundles already carry their identity. See
[adoption and handoff](README.md#move-vm-management-between-controllers).

```sh
openctl ctl discover --contexts
openctl ctl discover --provider-context homelab
```

Existing tracked VMs from name-only controller versions require explicit
identity confirmation before mutation. Discovery exposes this recovery action;
it preserves desired intent and composite ownership.

The console overview and `openctl ctl overview` show saved inventory and root
request activity without querying every provider. Counts are not live health;
open a resource for actual observation. Snapshot failures are shown as unavailable
or explicitly stale, not an empty lab.
Saved resources remain inspectable in their lists and detail pages when a
provider fails or reports them missing. Use `ctl get KIND NAME --api-version API
--saved -o yaml` for unresolved desired intent; secret/reference markers remain
intact. Stored intent is not live health or permission to mutate/recreate.

For a standalone application, open **Deploy Jellyfin**: select/provision a
dedicated Ubuntu 24.04 NVIDIA VM, supply NAS and secret references, review each
resource, and inspect independent health. Prepared reviews survive reload in the
same tab; explicitly resume to observe accepted work, then refresh previews and
confirmations before any new attempt. Its GPU driver reboot is explicit; there
is no automatic VM recreation. Use `openctl ctl preview -f resource.yaml` before
CLI Apply; previews distinguish metadata edits from spec edits and live drift.

The wizard's existing-guest list uses saved VM metadata without scanning every
Proxmox context. An unrelated provider outage does not hide choices; saved names
do not prove GPU/SSH readiness. Preview resolves the selected VM reference.
Active-context and saved-inventory failures are independent; refreshing guest
choices clears the old selection and guest consent rather than reusing them.

Apply and Delete print request IDs before submission;
`openctl ctl op get --request-id ID` recovers a lost acknowledgement without
submitting again. Console Apply and Delete attempts survive reload in the same
tab as identity metadata, not saved credentials or consent. Explicit recovery
observes the original operation; **Reconcile** opens the editor for fresh review.
For CLI application logs and typed runtime actions, see
[controller commands](README.md#common-commands). See also
[managed Jellyfin](README.md#managed-jellyfin-on-proxmox).

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
        # Optional private CA, installed on the controller/plugin host:
        # caFile: /etc/openctl/proxmox-ca.pem
    credentials:
      homelab-creds:
        tokenId: root@pam!openctl
        tokenSecretFile: ~/.openctl/secrets/proxmox.token
```

Put the API token in the `tokenSecretFile` path (mode `0600`).

Proxmox HTTPS certificates and hostnames are verified by default. Obtain private
CA bundles through a trusted out-of-band channel; `caFile` appends that PEM
bundle to system roots. The endpoint name/IP must match the certificate. Native
clients no longer silently skip verification: upgrading self-signed labs must
configure trust before connecting. Explicit per-context `insecureSkipVerify:
true` disables certificate/hostname checks for HTTPS only, exposes API tokens to
impersonation, and cannot be combined with `caFile`. HTTP and API redirects are
rejected. Restart the controller after saving credential or trust changes.

Use **Discover infrastructure → Test connection** or
`openctl ctl discover --test --provider-context homelab` before adoption.
Failures distinguish CA trust, certificate names/validity, rejected credentials,
missing read permissions and reachability. The CLI exits nonzero, retaining a
safe failure code and next step in table/JSON/YAML output; inspect these rather
than disabling verification or granting write access.

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
