# OpenCtl

OpenCtl is a local infrastructure controller and UI for managing homelab
resources declaratively. Today it focuses on Proxmox-backed virtual machines
and k3s clusters, with a controller that persists desired state, runs async
operations, mirrors manifests to disk, detects drift, and shows what is
happening in a browser UI.

The current happy path is controller-driven:

- `openctl-controller` runs as a local daemon.
- `openctl ctl ...` submits resource operations to that controller.
- The UI at `https://127.0.0.1:9445/ui/` gives you forms, templates,
  operation history, topology graphs, drift, settings, and provider editing.

Legacy exec-plugin commands such as `openctl proxmox get vms` still exist for
direct provider access, but new workflows should use the controller path.

## What It Can Do

- Create, update, resize, and delete Proxmox VMs.
- Create and converge k3s clusters on Proxmox.
- Scale k3s worker pools up and down.
- Destroy and recreate nodes one at a time when CPU or memory changes.
- Keep per-resource operation history, including parent and child operations.
- Cancel pending or running operations cooperatively.
- Show live operation status in the UI, including topology graph overlays.
- Materialize applied manifests under `~/.openctl/manifests`.
- Optionally commit manifest changes to git and run two-way GitOps from disk.
- Render built-in and user-authored CUE templates.
- Serve a local HTTPS UI and gRPC API with token auth.

## Status

OpenCtl is usable but still young. The macOS local controller install path is
implemented. Linux and remote controller installs are roadmap items.

Implemented providers:

| Provider | API version | Kinds |
| --- | --- | --- |
| Proxmox | `proxmox.openctl.io/v1` | `VirtualMachine`, `ProxmoxNode` |
| k3s | `k3s.openctl.io/v1` | `Cluster`, `K3sNode`, `AgentInstall` |

## Requirements

- macOS for `openctl-controller install --local`.
- Go and Node/npm for source builds.
- A Proxmox VE API token.
- A Proxmox bridge such as `vmbr0`.
- SSH key access for the Linux nodes OpenCtl creates.
- For k3s clusters, Ubuntu cloud-init clones must complete cleanly and have
  working outbound network access to `https://get.k3s.io`.

## Install From Source

```bash
git clone https://github.com/astrojerms/openctl.git
cd openctl

# One-time on macOS if an app firewall keys rules by code signature.
make codesign-setup

# Build CLI, controller, UI, plugins, and Linux k3s agent payloads.
make build build-plugin-k3s-agent-linux

# Install the controller as a per-user macOS LaunchAgent.
./bin/openctl-controller install --local

# Verify CLI -> controller TLS/auth.
./bin/openctl ping
```

Examples below use `openctl` for readability. From a source checkout, either
use `./bin/openctl` or add the build directory to your shell:

```bash
export PATH="$PWD/bin:$PATH"
```

The local install writes:

```text
~/.openctl/controller/                         controller state, token, TLS
~/Library/Application Support/openctl/bin/     installed controller + agents
~/Library/LaunchAgents/io.openctl.controller.plist
~/Library/Logs/openctl/controller.out.log
~/Library/Logs/openctl/controller.err.log
```

Open the UI:

```text
https://127.0.0.1:9445/ui/
```

If the browser asks for a token, use:

```bash
cat ~/.openctl/controller/token
```

To rebuild and reinstall after local changes:

```bash
make build build-plugin-k3s-agent-linux
./bin/openctl-controller install --local
./bin/openctl ping
```

To uninstall the LaunchAgent:

```bash
./bin/openctl-controller uninstall

# Also remove controller DB, TLS material, and token:
./bin/openctl-controller uninstall --purge
```

## Run The Controller In The Foreground

For development:

```bash
make build build-plugin-k3s-agent-linux
./bin/openctl-controller serve
```

Useful flags:

```bash
./bin/openctl-controller serve \
  --listen 127.0.0.1:9444 \
  --http-listen 127.0.0.1:9445 \
  --dir ~/.openctl/controller
```

The CLI defaults match those local addresses and paths.

## Configure OpenCtl

The main config file is:

```text
~/.openctl/config.yaml
```

Create a Proxmox API token in Proxmox, then store the token secret outside the
config file:

```bash
mkdir -p ~/.openctl/secrets
printf '%s\n' 'PASTE_PROXMOX_TOKEN_SECRET_HERE' > ~/.openctl/secrets/proxmox.token
chmod 600 ~/.openctl/secrets/proxmox.token
```

Minimal controller + Proxmox config:

```yaml
defaults:
  output: table
  timeout: 300

controller:
  # These are the local install defaults. You can omit this whole block
  # when using `openctl-controller install --local`.
  url: 127.0.0.1:9444
  tokenFile: ~/.openctl/controller/token
  caFile: ~/.openctl/controller/tls/ca.crt

providers:
  proxmox:
    default-context: homelab
    contexts:
      homelab:
        endpoint: https://pve.home.local:8006
        node: pve1
        credentials: homelab-token
    credentials:
      homelab-token:
        tokenId: root@pam!openctl
        tokenSecretFile: ~/.openctl/secrets/proxmox.token
    defaults:
      storage: local-lvm
      network: vmbr0
```

Full controller behavior config:

```yaml
defaults:
  output: table
  timeout: 300

controller:
  url: 127.0.0.1:9444
  tokenFile: ~/.openctl/controller/token
  caFile: ~/.openctl/controller/tls/ca.crt

providers:
  proxmox:
    default-context: homelab
    contexts:
      homelab:
        endpoint: https://pve.home.local:8006
        node: pve1
        credentials: homelab-token
    credentials:
      homelab-token:
        tokenId: root@pam!openctl
        tokenSecretFile: ~/.openctl/secrets/proxmox.token

# Desired manifests are mirrored here after successful applies.
# Omit the block to use ~/.openctl/manifests.
manifests:
  dir: ~/.openctl/manifests
  git:
    enabled: true
    branch: main
    remote: git@github.com:YOUR_USER/openctl-manifests.git
    # onCommit | manual | periodic
    pushMode: manual
    pushInterval: 5m
  gitops:
    # When enabled, edits under manifests.dir submit Apply operations.
    enabled: false
    # When true, deleting a manifest file submits Delete.
    deleteOnRemove: false

# Background drift checker. Defaults to enabled every 5m.
reconciler:
  enabled: true
  interval: 5m

# Completed operation rows retained per resource. Defaults to 50.
operations:
  retainPerResource: 50

# Directory scanned for user-authored CUE templates.
# Omit the block to use ~/.openctl/templates.
templates:
  dir: ~/.openctl/templates
```

Restart the controller after editing startup-read settings:

```bash
./bin/openctl-controller install --local
```

You can also edit provider credentials and controller settings from the UI.
Settings that are read at startup show a restart-required banner.

## Common Commands

Ping the controller:

```bash
openctl ping
```

List controller resources:

```bash
openctl ctl get VirtualMachine --api-version proxmox.openctl.io/v1
openctl ctl get Cluster --api-version k3s.openctl.io/v1
```

Get one resource:

```bash
openctl ctl get VirtualMachine web-01 --api-version proxmox.openctl.io/v1
openctl ctl get Cluster dev --api-version k3s.openctl.io/v1
```

Apply a manifest and wait for the operation:

```bash
openctl ctl apply -f vm.yaml
```

Fire and forget:

```bash
openctl ctl apply -f cluster.yaml --no-wait
```

Inspect operations:

```bash
openctl ctl op list --limit 20
openctl ctl op list --status running
openctl ctl op list --api-version k3s.openctl.io/v1 --kind Cluster --name dev
openctl ctl op get OPERATION_ID
```

Delete a resource:

```bash
openctl ctl delete VirtualMachine web-01 --api-version proxmox.openctl.io/v1
openctl ctl delete Cluster dev --api-version k3s.openctl.io/v1
```

## Example: Create A Ubuntu VM

Save as `vm.yaml`:

```yaml
apiVersion: proxmox.openctl.io/v1
kind: VirtualMachine
metadata:
  name: web-01
  labels:
    role: web
spec:
  node: pve1
  cloudImage:
    url: https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img
    storage: local-lvm
  cpu:
    cores: 2
    sockets: 1
  memory:
    size: 4096
  disks:
    - name: scsi0
      storage: local-lvm
      size: 32G
      ssd: true
      discard: true
      iothread: true
  networks:
    - name: net0
      bridge: vmbr0
      model: virtio
  cloudInit:
    user: ubuntu
    sshKeys:
      - ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample user@host
    ipConfig:
      net0:
        ip: dhcp
  agent:
    enabled: true
  osType: l26
  startOnCreate: true
```

Apply and inspect:

```bash
openctl ctl apply -f vm.yaml
openctl ctl get VirtualMachine web-01 --api-version proxmox.openctl.io/v1
```

## Example: Create A 3-Control-Plane k3s Cluster

A 3-control-plane cluster is recommended when testing control-plane respecs:
etcd quorum survives one control-plane node being destroyed and recreated.

Save as `cluster-ha.yaml`:

```yaml
apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: homelab
  labels:
    environment: homelab
spec:
  compute:
    provider: proxmox
    image:
      url: https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img
      storage: local-lvm
      diskStorage: local-lvm
    default:
      cpus: 2
      memoryMB: 4096
      diskGB: 30
  nodes:
    controlPlane:
      count: 3
    workers:
      - name: worker
        count: 1
  network:
    bridge: vmbr0
    dhcp: false
    staticIPs:
      startIP: 192.168.1.100
      gateway: 192.168.1.1
      netmask: "24"
  k3s:
    extraArgs:
      - --disable=traefik
  ssh:
    user: ubuntu
    privateKeyPath: ~/.ssh/id_ed25519
    publicKeys:
      - ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample user@host
```

Create it:

```bash
openctl ctl apply -f cluster-ha.yaml --wait-timeout 45m
```

Use kubeconfig:

```bash
export KUBECONFIG=~/.openctl/k3s/homelab/kubeconfig
kubectl get nodes -o wide
```

Open the UI and inspect the cluster detail page:

```text
https://127.0.0.1:9445/ui/
```

The topology graph shows the Cluster, child VMs, K3sNode installs, and
AgentInstall resources. In-flight or failed operations are overlaid on graph
nodes; clicking a node opens the ops drawer focused on that resource.

## Example: Scale A Worker Pool Up

Change the worker count:

```yaml
nodes:
  controlPlane:
    count: 3
  workers:
    - name: worker
      count: 2
```

Apply:

```bash
openctl ctl apply -f cluster-ha.yaml --wait-timeout 45m
kubectl get nodes
```

Expected result: one new worker VM is created, k3s joins it, the
openctl-k3s-agent is installed, and the node becomes Ready.

## Example: Scale A Worker Pool Down

Scale-down removes declared children and is intentionally gated.

Change the worker count back:

```yaml
nodes:
  controlPlane:
    count: 3
  workers:
    - name: worker
      count: 1
```

Apply with the destructive confirmation:

```bash
openctl ctl apply -f cluster-ha.yaml \
  --allow-destructive \
  --wait-timeout 45m
```

Expected result: the removed worker's `AgentInstall`, `K3sNode`, and
`VirtualMachine` are deleted, and the Kubernetes Node object disappears
without a manual `kubectl delete node`.

## Example: Resize Nodes

CPU and memory changes are handled as destroy-and-recreate operations, one
node at a time. This is destructive and requires `--allow-destructive`.

Do not respec a single-control-plane cluster unless you intentionally accept
API downtime. For control-plane respecs, use 3 control-plane nodes.

Example: resize control-plane nodes:

```yaml
nodes:
  controlPlane:
    count: 3
    size:
      cpus: 4
      memoryMB: 8192
      diskGB: 30
  workers:
    - name: worker
      count: 1
```

Apply:

```bash
openctl ctl apply -f cluster-ha.yaml \
  --allow-destructive \
  --wait-timeout 60m
```

Watch:

```bash
export KUBECONFIG=~/.openctl/k3s/homelab/kubeconfig
kubectl get nodes -w
```

Expected result: each affected node is destroyed, evicted, recreated at the
new size, rejoins, and returns Ready before the next affected node proceeds.

## Example: Enable Manifest Git Tracking

This makes the controller commit each successful apply/delete to a local git
repo under `~/.openctl/manifests`. Remote push is optional.

```yaml
manifests:
  dir: ~/.openctl/manifests
  git:
    enabled: true
    branch: main
    remote: git@github.com:YOUR_USER/openctl-manifests.git
    pushMode: manual
```

Restart the controller:

```bash
./bin/openctl-controller install --local
```

After applying resources:

```bash
cd ~/.openctl/manifests
git log --oneline --decorate -5
git status
```

The UI History card can show prior committed manifests and diff them against
current desired state.

## Example: Enable Two-Way GitOps

GitOps watches `manifests.dir`. File edits submit Apply operations tagged
`source=gitops`.

```yaml
manifests:
  dir: ~/.openctl/manifests
  git:
    enabled: true
    branch: main
    pushMode: manual
  gitops:
    enabled: true
    deleteOnRemove: false
```

Restart the controller:

```bash
./bin/openctl-controller install --local
```

Edit an existing mirrored manifest:

```bash
$EDITOR ~/.openctl/manifests/k3s.openctl.io/v1/Cluster/homelab.yaml
openctl ctl op list --limit 5
```

Set `deleteOnRemove: true` only if you want file deletion to submit resource
Delete operations.

## Example: User-Authored Templates

The controller serves built-in templates and also scans `~/.openctl/templates`
for `*.cue` files.

```bash
mkdir -p ~/.openctl/templates
cp examples/user-template.cue ~/.openctl/templates/dev-vm.cue
./bin/openctl-controller install --local
```

Template files declare:

```cue
template: {
	name:        "dev-vm"
	displayName: "Dev VM"
	description: "A small Ubuntu VM for development"
	apiVersion:  "proxmox.openctl.io/v1"
	kind:        "VirtualMachine"
	parameters: [
		{name: "hostname", type: "string", required: true},
	]
}

params: {...}

resource: {
	apiVersion: "proxmox.openctl.io/v1"
	kind:       "VirtualMachine"
	metadata: {
		name: params.hostname
	}
	spec: {
		startOnCreate: true
	}
}
```

Malformed templates are logged and skipped. A user template with the same
`template.name` as a built-in overrides the built-in.

## UI Workflow

The UI is served by the controller:

```text
https://127.0.0.1:9445/ui/
```

Useful areas:

- Resources: browse Proxmox VMs, Proxmox nodes, and k3s clusters.
- Detail: inspect desired state, observed state, drift, history, and topology.
- Templates: create common resources from parameterized starters.
- Operations drawer: watch parent and child operations, cancel running work,
  retry failed applies, and jump to resource detail pages.
- Providers: add, edit, or delete provider credentials.
- Settings: edit controller behavior such as reconciler interval and retained
  operations.

## Drift And Reconciliation

The reconciler runs by default every 5 minutes. It detects differences between
the desired manifest stored by OpenCtl and the observed provider state.

To disable it:

```yaml
reconciler:
  enabled: false
```

To change interval:

```yaml
reconciler:
  enabled: true
  interval: 30s
```

Automatic remediation is opt-in per resource:

```yaml
metadata:
  annotations:
    openctl.io/autoReconcile: "true"
```

## Legacy Direct Plugin Commands

These commands bypass the controller and talk directly to exec plugins. They
are useful for debugging provider access, but they do not populate the
controller operation log, UI, manifest mirror, or GitOps state.

```bash
openctl plugin list

openctl proxmox get vms
openctl proxmox get vm web-01 -o yaml
openctl proxmox apply -f vm.yaml
openctl proxmox delete vm web-01

openctl apply -f vm.yaml
```

## Development

Build everything:

```bash
make build build-plugin-k3s-agent-linux
```

Run tests:

```bash
make test
cd ui && npm test -- --run
cd ui && npm run check
```

Build the UI only:

```bash
make ui
```

Regenerate gRPC bindings after editing `pkg/api/v1/api.proto`:

```bash
make generate
```

Code quality helpers:

```bash
make fmt
make lint
make modernize-check
```

## Project Layout

```text
cmd/openctl/                         CLI entry point
cmd/openctl-controller/              controller daemon and macOS installer
internal/controller/                 controller server, ops, storage, providers
internal/templates/                  built-in and disk-loaded templates
pkg/api/v1/                          gRPC and HTTP gateway API
pkg/proxmox/                         Proxmox client and legacy plugin resources
pkg/k3s/                             k3s resource and agent implementation
plugins/                             legacy exec plugins and k3s agent command
ui/                                  Svelte UI embedded in the controller
examples/                            copy/paste manifests and template examples
```

## Troubleshooting

Controller logs:

```bash
tail -f ~/Library/Logs/openctl/controller.out.log
tail -f ~/Library/Logs/openctl/controller.err.log
```

Check controller health:

```bash
openctl ping
```

If k3s install appears stuck on a newly cloned VM, check cloud-init and
networking on that VM:

```bash
cloud-init status --long
systemd-analyze critical-chain cloud-final.service
journalctl -u cloud-final.service --no-pager | tail -40
ip a
ip r
cat /etc/resolv.conf
curl -v --max-time 15 https://get.k3s.io
```

If a cluster operation times out in the CLI, the operation may still be
running server-side:

```bash
openctl ctl op list --limit 10
openctl ctl op get OPERATION_ID
```

## License

MIT License
