# openctl Quickstart

A practical walkthrough for connecting an existing homelab and managing
infrastructure and applications. Kubernetes is optional. For architecture and
design rationale, see [DESIGN.md](DESIGN.md).

## Prerequisites

- Go 1.25+, Node.js 22.12+ and npm on the build host.
- A local systemd user service on Linux, or LaunchAgent on macOS.
- A Proxmox VE host with a cloud-init enabled template (e.g.
  `tpl-jammy-server-cloudimg-amd64`) and an API token.
- An SSH key on the build host (`~/.ssh/id_ed25519` or similar) whose public
  key cloud-init will inject into nodes.

## Install

```sh
make build-cli build-controller
mkdir -p ~/.local/bin
install -m 0755 bin/openctl ~/.local/bin/openctl
export PATH="$HOME/.local/bin:$PATH"
./bin/openctl-controller install --local
openctl ping
```

This builds the CLI and controller with its console, then installs the
controller as a per-user service. No Kubernetes cluster, exec plugins or K3s
agent binaries are required for native Proxmox and application workflows.
Keep the CLI and controller from the same build.

Kubernetes agents are opt-in:

```sh
make build-plugin-k3s-agent-linux
./bin/openctl-controller install --local --with-k3s-agents
```

Omitting that option does not remove existing installed agents. The broader
`make install` target still builds/installs optional legacy plugins and agents.

## Running the controller

The controller is the persistent reconciler. From Phase 4 onward, both
proxmox and k3s providers are compiled into the controller binary; the
CLI's `openctl ctl ...` commands route through it. The legacy exec-plugin
commands still work for transition, and graduate to top-level after
Phase 6.

### Install as a local service (macOS or Linux)

```sh
make build-cli build-controller
./bin/openctl-controller install --local
```

macOS uses a per-user LaunchAgent; Linux uses a user systemd service. Both
verify the listener and seed CLI config only when it is missing. For a
remote Linux host, build the matching controller and use:

```sh
make build-controller-linux
./bin/openctl-controller install --target ssh://user@controller --ssh-key ~/.ssh/controller
```

SSH targets require a preverified server key in `~/.ssh/known_hosts`, or an
explicit `--ssh-known-hosts PATH`. Unknown/changed keys fail before remote
commands or uploads; do not use network keyscan as trust establishment.

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

## Configure

Edit the installed `~/.openctl/config.yaml`; merge this provider block without
replacing the controller's generated connection, TLS or authentication settings:

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
        # Required only for first-boot cloud-init/bootstrap:
        snippetSSH:
          user: root
          identityFile: ~/.ssh/proxmox-bootstrap
          hosts:
            pve1: pve.example.com
    credentials:
      homelab-creds:
        tokenId: root@pam!openctl
        tokenSecretFile: ~/.openctl/secrets/proxmox.token
```

Put the API token in the `tokenSecretFile` path (mode `0600`).

New VM bootstraps upload cloud-init snippets over **verified node SSH**, because
the Proxmox API cannot upload snippets. Enable **Snippets** on the intended
Proxmox `local` storage without disabling its existing content types. Install
the configured SSH identity on the controller host and verify the hypervisor's
host key independently before adding it to that host's `~/.ssh/known_hosts`.
Never use `StrictHostKeyChecking=no` or trust an unverified `ssh-keyscan` result.
Existing Docker guests do not require this bootstrap transport.

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

## Connect And Adopt Before Rebuilding

After configuring Proxmox, restart the controller and open **Discover
infrastructure** in the console. Choose **Discover VMs** beside the intended
context: it tests read access and reads only that endpoint. Discovering every
context is a separate explicit action. Review physical context/node/VMID/UUID
evidence and the observed manifest before adopting an existing standalone VM.
Adoption records intent without changing the VM; admin access, an idle controller
and disabled GitOps are required.

The console submits the exact fingerprint shown in that review; a replacement
requires new discovery and consent. Testing or rediscovering a context invalidates
only its old inventory/review; independent contexts remain inspectable. Failed
reads are unavailable, not empty inventories. Session or authority changes clear
private reviews and bundles; leaving the page does not roll back accepted tracking.
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

For an ordinary Docker application, open **Deploy application**. Its new-guest
path installs the supported Docker/Compose toolchain on first boot; existing
guests are never silently upgraded. No GPU or manually staged bootstrap is
required. See the first-application walkthrough below.

**Deploy Jellyfin** remains the separate dedicated Ubuntu 24.04 NVIDIA/NAS
journey. It preserves prepared reviews and explicitly controls the GPU-driver
reboot; ordinary applications do not need those media-server prerequisites.

The **Jellyfin** new-VM path exposes CPU cores, RAM in MiB and OS disk in GiB,
defaulting to 4 / 4096 / 32. Chosen sizes survive review, reload and manifest downloads.
Verify workload/host capacity and the template's existing disk size yourself;
preview does not reserve capacity. Existing guests are not resized.

Guest IP discovery matches Proxmox-configured NIC MACs, not interface order.
Missing or ambiguous matching IPv4 leaves `status.ip` unknown and blocks dependent
references. For intentionally multi-address guests, explicitly declare the
application's `spec.target.host`; verify reachability and SSH trust separately.
Open the VM's **Guest address** section for a safe reason code and repair guidance.
Fresh named CLI/API reads expose the same `status.ipObservation`; List does not
probe agents. Address observation is not SSH/service readiness, and stale or
missing evidence remains unknown.

The Jellyfin wizard checks its referenced VM before application review and again
before Apply. Address blockers link directly to VM inspection; recheck after
repair, refresh the preview and renew consent. Blocked checks never admit another
request or retry VM creation. Accepted-request recovery remains read-only.

For failed references in editor, template or guided previews, use **Inspect
referenced resource**. It opens the declared dependency in another tab without
discarding the draft. Check its requested output, repair explicitly, then obtain
a fresh preview; the link is not existence/readiness evidence or Apply approval.
CLI preview shows the same dependency and output, including structured JSON.

The wizard's staged ZIP contains one numbered YAML file per resource stage;
the current stage can also be downloaded alone. Preview/apply files individually
in order, not as a bulk directory: public deployments have initial and final
versions of the same application. Keep exports private and outside watched
GitOps directories. Downloads contain declarations, not consent or request recovery.

Jellyfin media readiness checks both the guest's protected NAS mount and the
running container's exact read-only `/media` binding and UID-1000 access.
Wrong sources, writable/overridden mounts and unreadable directories block
readiness; missing runtime evidence is unknown. Inspection never repairs mounts
or restarts containers automatically.

Application checks distinguish **Ready**, **Not ready**, **Unknown** and
**Not configured**. Guest access failure leaves skipped dependent checks unknown,
not falsely failed; an absent optional public endpoint is neutral. Required
unknown/failed checks still block readiness and guided progression.
Console lists, details, child summaries and deployment gates require explicit
boolean readiness with consistent checks, just like `ctl health`. Even
`ready: true` cannot override failed, unknown or malformed evidence; a `ready`
phase alone is not application readiness.
For automation, `openctl ctl health Jellyfin NAME --api-version apps.openctl.io/v1 -o json`
reports live health-only diagnostics and exits nonzero unless readiness
is established. It reads only that resource, with no Apply, polling, retry or saved
fallback. A VM merely reported as running is not readiness proof.
Jellyfin public readiness also requires onboarded JSON from the same server
observed through guest SSH. A login page or different instance returning HTTP
200 is not ready; unavailable guest identity is unknown. Public probing sends
no credentials and never bypasses access policy or certificate checks.

The wizard's existing-guest list uses saved VM metadata without scanning every
Proxmox context. An unrelated provider outage does not hide choices; saved names
do not prove GPU/SSH readiness. Preview resolves the selected VM reference.
Active-context and saved-inventory failures are independent; refreshing guest
choices clears the old selection and guest consent rather than reusing them.

Blocked guided previews show schema/field/dependency diagnostics without enabling
Apply or creating a request ID. Repair the reported cause, refresh the preview
and renew consent. Reload does not preserve diagnostics as readiness evidence.
Reference failures identify the destination spec path and a safe failure code:
distinguish an absent output from provider authentication, trust or availability
problems. Raw upstream error details are not forwarded by reference resolution.

Previously accepted prerequisites have an exact-resource blocker with an
inspection link and, when the baseline matches, current readiness checks.
Repair that resource, then choose **Recheck prerequisites** for a fresh preview
and renewed consent. This does not resubmit accepted work or publish anything.

Apply and Delete print request IDs before submission;
`openctl ctl op get --request-id ID` recovers a lost acknowledgement without
submitting again. Console Apply and Delete attempts survive reload in the same
tab as identity metadata, not saved credentials or consent. Explicit recovery
observes the original operation; **Reconcile** opens explicit editor review.
Unlike ordinary Edit, that mode permits reapplying unchanged stored intent:
an interrupted rollout may have changed remote state that a no-op preview cannot
fully observe. Reconciliation still requires a fresh preview, current authority,
all destructive gates and a new request after a terminal failure; it never
silently replays the old request.
Runtime buttons first open a review of the exact target, provider-described
effect and parameters; no request is sent until explicit Run. Cancel does
nothing. Runtime actions are not durable Apply requests; inspect uncertain
outcomes before repeating them.
For CLI application logs and typed runtime actions, see
[controller commands](README.md#common-commands). See also
[managed Jellyfin](README.md#managed-jellyfin-on-proxmox).

## Deploy your first application

1. Install and connect to the controller above. On a dedicated Linux controller,
   enable user-service persistence across logout with
   `sudo loginctl enable-linger "$USER"`. Keep the controller's API listeners
   loopback-only; use SSH forwarding rather than exposing them publicly.
2. Configure and **Test connection** for the intended Proxmox context. Prepare
   a clean **Ubuntu 24.04 amd64 cloud-init template**, snippets-enabled storage
   and verified bootstrap SSH as described above. The template disk cannot
   be shrunk by cloning.
3. Open **Deploy application**, explicitly select the context, node and template
   VMID, and choose capacity. Ordinary guest defaults are **2 vCPU / 2048 MiB /
   16 GiB**; the minimum disk is 8 GiB. Supply a guest public key. Enter the
   matching private-key path and a **new per-guest known_hosts path** on the
   controller host before preparing the review. Never put key contents in the browser.
4. Prepare the review. Inspect the rendered `docker-host-vm` declaration and
   explicitly authorize the VM step. First boot installs Docker CE and
   checksum-verified **Compose 5.8.1**, without a GPU, NAS, manual Compose upgrade
   or acceptance-only script. Inspect cloud-init errors rather than treating
   a running VM as proof that installation finished.
5. Once the guest is running with a unique agent-observed IPv4, run this command
   **on the controller host**, using a new destination file:

   ```sh
   openctl ctl action run VirtualMachine YOUR_VM ssh-host-keys \
     --api-version proxmox.openctl.io/v1 \
     --download "$HOME/.ssh/YOUR_VM.known_hosts"
   ```

   This reads the actual guest Ed25519 host key through the verified Proxmox
   guest-agent channel, bound to the recorded VM identity and fresh address.
   It never auto-enrolls trust or weakens SSH checks. Inspect failures; do not
   substitute a key scan. The private download is mode `0600` and never overwrites.
6. Select **Preview next resource**. Keep the same controller-host key and
   known_hosts paths, inspect the rendered `compose-application` step, explicitly
   confirm trust and authorize Apply. Bootstrap, strict SSH and address checks
   gate admission. Saved managed Docker guests may skip the
   VM step; they are never automatically upgraded or resized.
7. Inspect application readiness; use **Refresh health** if the first container
   health interval is still settling. **Application readiness checks passed**
   confirms independent container and HTTP checks. The welcome service binds
   only to the **guest's loopback**, not the controller's or your laptop's.
   Use the displayed strict SSH forwarding command to access it.

The two templates also appear in **Templates**, and the reviewed declarations
can be downloaded as YAML or a ZIP. Downloads are not Apply authorization or
recovery tokens. Reloading requires explicit recovery of the original request;
it never silently retries. Failure does not delete or roll back the guest.
Delete the application before deleting its guest when cleaning up.

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
