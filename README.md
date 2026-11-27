# OpenCtl

OpenCtl is a homelab control plane: a browser console and declarative resource
manager sharing one persistent controller. Manage Proxmox infrastructure,
optional k3s workloads, and standalone applications without user-managed state
files or a whole-lab plan.

The current happy path is controller-driven:

- `openctl-controller` runs as a local daemon.
- `openctl ctl ...` submits resource operations to that controller.
- The UI at `https://127.0.0.1:9445/ui/` gives you forms, templates,
  operation history, topology graphs, drift, settings, and provider editing.

Legacy exec-plugin commands such as `openctl proxmox get vms` still exist for
direct provider access, but new workflows should use the controller path.

## Agent Deployment Skill

Load [`openctl-deploy`](.claude/skills/openctl-deploy/SKILL.md) when asking an
agent to provision or update infrastructure. It guides controller/provider
discovery, YAML/CUE declarations, authenticated schema validation and dry-run,
dependency ordering, operation tracking, and live readiness checks. Destructive
changes require explicit authorization.

Claude Code can invoke `/openctl-deploy` in this checkout. For another project,
copy the entire `.claude/skills/openctl-deploy/` directory into that project's
`.claude/skills/` directory. Other agents can load `SKILL.md` explicitly or
install the directory with their compatible skill loader. Keep its `references/`
directory alongside it.

The skill is self-contained: deployment needs an installed OpenCtl CLI and
authorized access to the intended controller, not the OpenCtl source tree or
the maintainer's homelab credentials. Its preflight reference uses Python 3's
standard library and does not mutate infrastructure.

## What It Can Do

- Create, update, resize, and delete Proxmox VMs.
- Discover controller-side Proxmox inventory and adopt existing VMs without changing them.
- Manage standalone Docker Compose applications and a GPU/NAS-backed Jellyfin recipe.
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

OpenCtl is usable but still young. macOS and Linux local service installs,
SSH deployment, and Proxmox controller-VM bootstrap are implemented. Hardware,
guest and application readiness must be verified independently of provisioning.

Implemented providers:

| Provider | API version | Kinds |
| --- | --- | --- |
| Proxmox | `proxmox.openctl.io/v1` | `VirtualMachine`, `ProxmoxNode` |
| k3s | `k3s.openctl.io/v1` | `Cluster`, `K3sNode`, `AgentInstall` |
| Standalone apps | `apps.openctl.io/v1` | `ComposeApplication`, `Jellyfin` |

## Requirements

- macOS or Linux for `openctl-controller install --local`.
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

# Install locally: LaunchAgent on macOS, user systemd service on Linux.
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

Controller `tokenFile` and `caFile` accept absolute paths or `~/` paths.
Home-relative paths resolve against the home directory of the user running
the CLI, including when connecting to a remote controller.

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
        snippetSSH:
          hosts:
            pve1: pve.home.local
          user: root
          identityFile: ~/.ssh/proxmox
    credentials:
      homelab-token:
        tokenId: root@pam!openctl
        tokenSecretFile: ~/.openctl/secrets/proxmox.token
    defaults:
      storage: local-lvm
      network: vmbr0
```

Cloud-init vendor snippets (`packages` / `runcmd`, plus guest-agent enablement)
are written through SSH: Proxmox's REST upload endpoint does not accept snippets.
Map each Proxmox node explicitly under `snippetSSH.hosts`; the SSH account must
be able to run `pvesm path` and write the selected storage's snippet directory.
`identityFile` is a path on the machine running openctl/controller, not key
material in a manifest. Omit it to use normal OpenSSH agent/default identities.
Uploads use batch authentication, strict host-key checking, and atomic file
replacement. Verify and trust the node's host-key fingerprint beforehand;
unknown or changed host keys fail rather than being accepted automatically.


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
        snippetSSH:
          hosts:
            pve1: pve.home.local
          user: root
          identityFile: ~/.ssh/proxmox
    credentials:
      homelab-token:
        tokenId: root@pam!openctl
        tokenSecretFile: ~/.openctl/secrets/proxmox.token

# Desired manifests are mirrored here after successful applies.
# Omit the block to use ~/.openctl/manifests.
manifests:
  dir: ~/.openctl/manifests
  mode: mirror
  git:
    enabled: true
    branch: main
    remote: git@github.com:YOUR_USER/openctl-manifests.git
    # onCommit | manual | periodic
    pushMode: manual
    pushInterval: 5m

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

Admins can edit provider credentials and controller settings from the UI.
Startup-read settings show a restart-required banner. The console uses the
running authority mode, not an unapplied edit to configuration.

## Back Up And Recover The Controller

Stop the controller and other processes writing the selected roots first.
Use its service manager (or stop the foreground daemon). Run recovery as its OS
user and use the actual configuration root and state directory; the examples
assume the local defaults.

```bash
./bin/openctl-controller backup \
  --root "$HOME/.openctl" --output "$HOME/openctl-backup.tar"

# Restore into NEW destinations; only their parents should already exist.
mkdir -p "$HOME/openctl-recovered"
./bin/openctl-controller restore \
  --input "$HOME/openctl-backup.tar" \
  --root "$HOME/openctl-recovered/.openctl"
```

Use `--dir` on backup for a separate daemon state directory such as
`/var/lib/openctl/controller`; use it on restore to choose a separate new state
destination. Otherwise restore preserves the archived root-relative state path,
or uses `NEW_ROOT/controller` when the archived state was external.

The archive includes SQLite (including uncheckpointed WAL data), provider opaque
state and identities, operation history, configuration, credentials, TLS material,
and files under the selected roots. It is **plaintext sensitive material**:
created mode `0600`, never overwritten; encrypt offsite copies separately.
External SSH keys, external manifest/secret directories, VM disks and guest/NAS
application data are **not** backed up by this command.

Restore validates format, checksums, paths and the exact SQLite schema of a known
released migration version before publishing private files. Unknown, future or
modified schemas are rejected. Offline backup/restore never upgrades a database;
normal daemon startup applies later migrations. Destinations must not exist, even
if empty; parents must exist. Symlinks/special files are refused, and path ancestors
must be symlink-free. Limits: 16 GiB/file, 32 GiB total, 100,000 entries.

Restore does not rewrite absolute paths, start the daemon, or change infrastructure.
Review archived configuration and external dependencies before pointing the
daemon's `HOME`/`--dir` at the restored locations: startup may resume queued
operations or reconciliation against the original lab. **Never run both copies.**
The daemon and backup share an exclusive state lock; older binaries do not
honor it and must also be stopped.

Restoring an older backup also rolls back accepted Apply/Delete request history. Inspect
provider work submitted after that backup and discard stale browser reviews before
resubmitting: a snapshot cannot deduplicate requests it never recorded.

## Common Commands

Ping the controller:

```bash
openctl ping
```

Inspect persisted control-plane inventory and root work without contacting providers:

```bash
openctl ctl overview
openctl ctl overview --limit 5 -o json
```

The snapshot counts saved desired records, including owned children and providers
that are no longer registered. Discovery-only observations are not managed records.
Pending/running totals count root Apply/Delete requests, never child steps.
Review lists show the latest retained unsuccessful admission per resource; a newer
request supersedes an older failure. These are not resource health checks.
`--limit` caps each operation list (default 10, maximum 100), not its exact total.
Inspect an operation by ID before deciding what to retry; use resource Get/runtime
inspection for live readiness. This requires a matching controller with overview
support; there is no fallback to scanning the lab.

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

Inspect saved intent even when its provider is unreachable or no longer registered:

```bash
openctl ctl get VirtualMachine --api-version proxmox.openctl.io/v1 --saved
openctl ctl get VirtualMachine web-01 --api-version proxmox.openctl.io/v1 --saved -o yaml
```

Without a name, `--saved` lists metadata and last-Apply provenance only. Named
JSON/YAML output is the unresolved desired declaration: secret/reference markers
remain intact, with no observed status or ownership claims. Protect these outputs
like manifests. Plain `ctl get` still requires a successful live observation.
The console likewise retains saved rows and desired YAML through provider
outages or missing resources, explicitly separates unknown/stale observation,
and disables mutation while observation is unavailable. Saved intent does not
prove existence, health, ownership or permission to recreate infrastructure.

Preview one resource against the live controller before applying:

```bash
openctl ctl preview -f vm.yaml
openctl ctl preview -f vm.cue --values homelab.cue -o json
```

Preview reports explicit create/update/no-op, stored-spec edits, desired metadata
edits, independent live drift, child actions and required gates. It uses controller
validation and performs no Apply, operation submission or automatic approval.
Invalid intent prints diagnostics (including usable JSON/YAML with `-o`) and exits
nonzero. It is not an existence reservation, an authorization to mutate, or the
offline graph `plan`; use matching CLI/controller versions.

Apply a manifest and wait for the operation:

```bash
openctl ctl apply -f vm.yaml
```

Apply accepts exactly one resource. Multi-document YAML is rejected rather than
silently applying its first document. Split staged workflow downloads and apply
resources in their reviewed order; they are not a batch Apply or an unordered
GitOps directory.

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

Each Apply or Delete prints a generated UUID **before** submission. Keep it if
the response or connection is lost; recover read-only without an operation ID:

```bash
openctl ctl op get --request-id REQUEST_ID
# Deliberately choose an identity and prohibit updating an existing resource:
openctl ctl apply -f vm.yaml --create-only --request-id YOUR_UUID
```

The same key, canonical actor, action and intent return the original operation,
including its failure; changed intent, target, action or another actor conflicts.
The binding survives normal restarts and operation-history pruning. Pruned work
cannot be resubmitted under its old key. A new attempt after known terminal
failure needs fresh inspection/review and a **new** key, not automatic retries.
Upgrade CLI and controller together: keyed Apply/Delete refuse controllers
without their durable capability. Request IDs are metadata, not credentials;
prefer UUIDs.

Check live readiness of exactly one resource without dumping its declaration:

```bash
openctl ctl health Jellyfin media --api-version apps.openctl.io/v1 -o json
```

`ctl health` makes one live named Get, with viewer access and the normal bounded
`--timeout`; it never polls, retries, applies, invokes actions or falls back to
saved intent. Exit zero requires explicit boolean readiness and no failed,
unknown or malformed check. Optional `not_configured` checks are neutral;
`running`/`Ready` phase text alone is unknown. Failed/unknown checks exit nonzero
**after** emitting diagnostics in table, JSON or YAML. Read/protocol/output errors
also exit nonzero; failed reads do not fabricate a health result.

Structured output is a read-only resource-shaped health projection:
`apiVersion`, `kind`, `metadata.name` and normalized `status.state`, `status.ready`,
provider message and independent checks. It omits specs, annotations, saved intent
and unrelated status fields; it is not an Apply manifest or a raw Get response.
Provider health messages may still be sensitive. Readiness is current evidence,
not playback, access-policy or future-readiness proof.

Inspect controller runtime actions and retrieve application logs:

```bash
openctl ctl action list Jellyfin --api-version apps.openctl.io/v1
openctl ctl action run Jellyfin media logs --api-version apps.openctl.io/v1 --download ./media.log
```

Action listings include provider-owned display labels, descriptions, parameter
types, requirements and defaults. `ACTION` is the stable invocation ID; `LABEL`
is presentation only. CLI/API calls use the raw action name, never the label.
Invocation requires editor/admin access and an explicit API version.
Non-sensitive arguments use repeatable `--param key=value`; use `--params-file PATH` (or `-` for stdin)
for a single JSON object of string values, particularly credentials. Duplicate
keys and non-string values are rejected without echoing them.

Downloads go only to an explicitly selected, new `0600` file; existing files and
symlinks are refused before invocation. Server-provided filenames are not paths.
`--download -` explicitly streams raw content, which can include credentials.
Returned payloads are never printed implicitly, including in JSON/YAML: omitting
`--download` can fail output handling **after the action request succeeded**.
Links are displayed, never opened automatically, and can also be sensitive.

Runtime actions do not change desired intent or use durable admission request IDs.
They are never automatically retried. A successful request may only have queued
a provider task: inspect task completion and independent readiness. After a
timeout, disconnection or output failure, inspect before deciding to repeat.

In the console, every runtime action opens an inline review before invocation,
including parameterless actions and downloads. Review the exact API version,
kind/name, raw action ID, provider-specific effect and parameters, then explicitly
run or cancel. Application restart is not a k3s restart, and a graceful application
stop is not a VM force-power-off. Missing provider labels display the raw name
rather than guessing its meaning. Reviews are transient, not saved authority;
stale observation, navigation or a changed session requires a fresh review.
This is console interaction safety, not an additional API authorization boundary.

Delete a resource:

```bash
openctl ctl delete VirtualMachine web-01 --api-version proxmox.openctl.io/v1
openctl ctl delete Cluster dev --api-version k3s.openctl.io/v1
```

Delete uses the same durable request ledger as Apply, with a distinct action and
target fingerprint. An explicit `--request-id YOUR_UUID` replays that exact
admission; it cannot delete a new resource that later occupies the same name.
After a timeout, first use `ctl op get --request-id REQUEST_ID`; do not mint a
new request identity automatically. Known failed/interrupted/canceled operations
need fresh inspection and explicit authorization before a newly keyed attempt.
Saved intent disappearing or a provider returning NotFound is not proof of
successful deletion: inspect the terminal operation.

The console persists delete-attempt metadata in the current tab, bound to the
controller, principal and resource. Reload restores inspection, not consent or
submission. **Recover request** only reads the original operation, even
when the resource or its provider is gone. A failed acknowledgement never
silently navigates away, resubmits or authorizes deleting a replacement.
After **Recover request** establishes a terminal outcome, **Review new delete
attempt** refetches live state and requires new typed confirmation and a fresh
key. After success, this is available only for a currently tracked resource;
it never turns a missing resource or cached outcome into new deletion authority.

## Connect And Adopt An Existing Lab

In the console, open **Providers**, save the Proxmox credentials, and restart the
controller. Configuration changes are startup-only. Open **Discover
infrastructure**, test each connection, discover existing VMs, and review a VM's
observed desired manifest before adopting it. Adoption is admin-only, requires
an idle controller with GitOps disabled, and never applies or changes the VM.

Native Proxmox uses HTTPS with certificate and hostname verification by default.
For a private/self-signed CA, obtain its PEM bundle through a trusted,
out-of-band channel, place it on the controller host, and set the context's
`caFile` (or **CA bundle file** in Providers). The bundle adds to system roots;
the endpoint's DNS name or IP must still match the certificate. Trust and
credential changes require a controller restart.

**Upgrade note:** older native clients silently disabled verification. Existing
self-signed installations now fail closed until trust is configured. An explicit
per-context `insecureSkipVerify: true` is available only for knowingly insecure
HTTPS: it disables certificate and hostname checks and exposes API credentials
to impersonation. It cannot be combined with `caFile`. Plain HTTP and API
redirects are never allowed; no automatic insecure fallback exists.

The same read-only inventory is available through the controller CLI:

```bash
openctl ctl discover --contexts
openctl ctl discover --test --provider-context homelab
openctl ctl discover --provider-context homelab -o json
```

`ctl discover --test` exits nonzero when the connection test fails, while keeping
its table/JSON/YAML result available for inspection. Discovery remains read-only.

Failed connection tests return a safe `failureCode` and a specific next step in
the console and CLI (`failureCode` in JSON/YAML, `CODE` in the table). Categories
include:

| Failure code | What to inspect on the controller/provider |
| --- | --- |
| `tls_untrusted` | Verified private CA bundle, `caFile`, then restart |
| `tls_hostname_mismatch` | Endpoint DNS/IP versus certificate names; do not disable verification |
| `tls_certificate_invalid`, `tls_handshake_failed` | Certificate dates/chain, controller clock, HTTPS port and TLS support |
| `unauthorized`, `permission_denied` | Token ID/secret versus node/VM read ACLs; discovery needs no write grant |
| `dns_failed`, `endpoint_unreachable`, `timeout` | Controller-side DNS, routing, firewall, API availability |
| `redirect_rejected`, `endpoint_not_found` | Direct HTTPS API URL and reverse-proxy routing |
| `rate_limited`, `provider_unavailable` | API limits or provider/proxy health before explicitly testing again |
| `canceled`, `connection_failed` | Canceled test or an unclassified failure; no read-access claim |

Classification uses typed causes, never upstream error-text matching. Response
bodies, credential-bearing URLs and certificate contents are not forwarded.
Success has no failure code; clients should tolerate future/unknown codes.
Testing all contexts keeps failures local to each connection and discovers only
contexts whose read test succeeded. No automatic retry or trust bypass occurs.

VM operations bind to context, endpoint, node, VMID and SMBIOS UUID rather than
trusting a name alone. After upgrading an older controller, Discovery offers
**Confirm tracked VM identity** for already-managed VMs that lack this binding.
Verify its displayed physical identity before confirming. This also supports
legacy cluster children whose placement can be derived from the saved owner's
plan; it does not adopt children independently or change infrastructure.

```bash
openctl ctl confirm-identity VirtualMachine web-01 \
  --provider-context homelab --identity "$VERIFIED_DISCOVERY_FINGERPRINT"
```

Delete requires managed tracking and refuses owned resources or resources with
managed/in-flight dependents. Use adoption for unmanaged VMs; never use Delete
as a way to take ownership. Export/release are separate from deletion.

## Move VM Management Between Controllers

`ctl export`, `ctl adopt`, and `ctl forget` move management of an existing
standalone Proxmox `VirtualMachine` without a SQLite backup or VM recreation.
They are administrator-only operations. Both controllers must run a version
that supports these commands.

Configure each controller's Proxmox endpoint and credentials independently.
`source-client.yaml` and `destination-client.yaml` below are CLI configurations
pointing at the respective controllers, not VM manifests. The destination
provider context must reach the same VM; its context name can differ.

Before starting, pause reconciliation on both controllers and stop other clients
from applying, deleting, or running VM actions during the handoff. Disable
manifest GitOps on both controllers and restart them: migration refuses while
the startup-loaded GitOps watcher/pull loop is enabled. Migration also requires
an idle controller, with no pending or running operations.

Export only the VM being transferred:

```bash
openctl --config source-client.yaml ctl export VirtualMachine runner-01 \
  --api-version proxmox.openctl.io/v1 -f runner-01.transfer.json
```

Export retains source ownership. The versioned JSON bundle contains desired
intent, its digest, a source-controller identifier, and the live Proxmox node,
VMID, and SMBIOS UUID. It does not copy controller credentials, sessions,
operation history, or the database. Manifests can contain literal cloud-init
secrets: export creates a new file with mode `0600`, refuses to overwrite an
existing file, and never prints the bundle to stdout. Transfer it securely and
keep the original unmodified for the source release.

Adopt at the destination, optionally overriding the provider context:

```bash
openctl --config destination-client.yaml ctl adopt \
  -f runner-01.transfer.json --provider-context proxmox-home
openctl --config destination-client.yaml ctl get VirtualMachine runner-01 \
  --api-version proxmox.openctl.io/v1
```

Adoption verifies the VM's physical identity and reads its current state before
recording desired intent. It does not apply configuration, resize, start,
delete, or rerun cloud-init. Existing drift remains visible; a subsequent
explicit Apply or resumed reconciliation can converge that drift. A matching
adoption retry is safe; conflicting destination intent is rejected.

After verifying destination tracking, release only the source controller's
ownership:

```bash
openctl --config source-client.yaml ctl forget -f runner-01.transfer.json
```

Forget validates the original source-controller identifier and full desired
manifest digest, removes desired intent and its mirrored manifest, and leaves
the VM and operation history intact. Changes made since export invalidate the
bundle. Matching retries are safe. A durable release marker blocks stale
Apply, Delete, runtime actions, and GitOps submissions, even after restart;
only explicit adoption can reclaim the resource. There is no distributed
transaction between controllers: keep both paused until this step succeeds.
Update external GitOps repositories to contain only resources each controller
should manage before restarting automation.

Repeat for each runner being moved. Resources not selected for transfer, such
as a dev VM remaining on the Mac controller, stay managed there.

To register an existing VM that has a manifest but no source-controller
record, first discover and review its placement and full opaque `identity`
fingerprint, then use a single YAML or CUE manifest instead of a bundle:

```bash
openctl --config destination-client.yaml ctl discover \
  --provider-context proxmox-home -o json
# Review the selected VM and set REVIEWED_IDENTITY to its exact identity.
openctl --config destination-client.yaml ctl adopt \
  -f existing-runner.yaml --provider-context proxmox-home \
  --expected-identity "$REVIEWED_IDENTITY"
```

Manifest adoption requires the reviewed fingerprint, matches the existing VM by
name and node, rejects ambiguous matches, and observes it without modifying it.
A same-name replacement, changed node/VMID/UUID, empty fingerprint or whitespace
only value is rejected before tracking is written. Rediscover and explicitly
review a replacement before adopting it; never fetch a fresh fingerprint inline
as an automatic fallback. The REST/gRPC field is `expectedIdentity` /
`expected_identity`. Upgrade clients together: old unguarded manifest-adoption
requests fail closed. Bundles already carry their required identity; an optional
explicit override must agree exactly with the bundle.

The console sends the fingerprint shown in the reviewed inventory. Retesting a
context invalidates its old inventory and consent; unrelated contexts keep their
reviews. Session/role or manifest-authority changes clear reviews and private
bundles. **Clear bundle** discards a local bundle without uploading it. Leaving
the page stops waiting but cannot undo tracking already accepted by the
controller; after an uncertain response, inspect saved intent and fresh
discovery before any retry.

Manifest adoption has no source-controller receipt and cannot be passed to
`ctl forget`; export from the adopting controller first if that ownership must
later be released.

Migration currently supports only standalone `proxmox.openctl.io/v1`
`VirtualMachine` resources. Composites, owned children, resources with inbound
or outbound references, and resources requiring opaque provider state are
rejected rather than incompletely transferred. Proxmox templates and VMs
without a valid SMBIOS UUID cannot be adopted or exported.

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
  mode: mirror
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

## Example: Enable Upstream-Authored GitOps

Choose exactly one authority. In default `mirror` mode, SQLite owns intent;
the controller writes an optional git audit mirror but never watches or pulls
it. In `gitops` mode, the upstream repo owns intent; the controller never
auto-commits, pushes, or revives removed manifest files at startup.

```yaml
manifests:
  mode: gitops
  dir: ~/.openctl/desired
  gitops:
    remote: git@github.com:YOUR_USER/openctl-manifests.git
    deleteOnRemove: false
    pull:
      interval: 1m
      prune: false
```

Restart the controller:

```bash
./bin/openctl-controller install --local
```

Commit desired manifests to the upstream repository. Initial startup sync and
subsequent pulls submit operations tagged `source=gitops`. Direct console/CLI
Apply and Delete are rejected in this mode: edit the repo instead. The console
disables desired-state mutations but retains observation, operation history,
and runtime actions for editor/admin sessions.

Enable `deleteOnRemove` or pull `prune` only after authorizing their deletion
semantics. Legacy configs mixing `manifests.git` output commits with
`manifests.gitops` input automation fail with migration guidance. See
[manifest authority modes](docs/gitops-modes.md).

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

- Overview: saved inventory, exact root-work totals and bounded request review,
  with explicit operation inspection even beyond the live event cache.
- Resources: browse Proxmox VMs, Proxmox nodes, and k3s clusters.
- Discovery: inspect one selected Proxmox context, or explicitly discover all,
  then review existing VM identity before changing tracking.
- Detail: inspect desired state, observed state, drift, history, and topology.
- Templates: create common resources from parameterized starters.
- Operations drawer: watch parent and child operations, cancel running work,
  retry failed applies, and jump to resource detail pages.
- Providers: add, edit, or delete provider credentials.
- Settings: edit controller behavior such as reconciler interval and retained
  operations.

Home and sidebar counts share one persisted snapshot; cosmetic badges never poll
every provider kind or SSH into application guests. Badges count saved desired
records, not live instances. Discovery-only kinds have no managed-count badge, and
saved kinds remain visible when their schema/provider is absent. Snapshot failures
leave counts unknown; Home labels any retained values stale. Neither a count nor
a successful historical operation proves current health.

In **Discover infrastructure**, each connection's **Discover VMs** action runs
a fresh read-access test and inventories only that context. Unrelated endpoints
are not probed; the all-context button remains an explicit opt-in. Refresh clears
the selected context's old rows and consent before reading, but preserves other
contexts' inventory and reviews. Connection/inventory failures stay unavailable,
not zero VMs; an observed zero requires a successful read. Discovery does not
adopt, confirm identity, Apply or retry automatically.

Retry restores the failed Apply's submitted intent even when a first deployment
has no managed resource yet. Secret markers and desired metadata are preserved;
inherited destructive approvals are cleared for fresh review. A failed Get caused
by authentication or connectivity is not treated as an absent resource.

The resource editor and template starters persist only Apply request metadata in
this tab, bound to the controller, principal and review surface. Draft manifests,
template parameters, credentials and checked gates are **not** saved. Keep a
protected manifest copy before leaving an unsent draft. After reload or a lost
response, **Recover Apply request** reads the original operation without another
submission; the submitted target is shown separately from the current draft.
Acknowledgement is not Apply success and never silently updates the baseline or
navigates away. **Review new Apply** rechecks a terminal outcome and starts fresh
preview/consent; unknown, active or pruned requests cannot authorize another key.

**Reconcile** opens the resource editor to review stored intent versus live drift;
it does not immediately Apply. Failed first-creation retries without a managed
baseline remain create-only, so newly discovered same-name resources are not
silently overwritten.

The Jellyfin wizard saves its prepared steps and accepted request/operation IDs
in this tab's session storage, bound to the controller and signed-in principal.
After reload, explicitly **Resume saved review** or discard/download it. Resume
observes accepted work without resubmission; a lost acknowledgement is recovered
by request ID. New attempts require a fresh preview and unchecked confirmations.
Past flags identify the original request, not renewed approval. Resolved secrets,
cached readiness and checked consent are not restored. Earlier successful steps
are reverified before dependent work proceeds. Closing the tab may lose this
browser record; leaving or discarding it does not cancel work or delete resources.

Previews show label/annotation changes separately from spec edits and live drift.
Desired YAML excludes controller-owned ownership references; ownership remains a
separate edit guard, not a field the form can overwrite.

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

## Standalone Compose Applications

Choose `apps.openctl.io/v1` / `ComposeApplication` in the console or declare it
directly. Supply the SSH `target`, `compose`, `environment` (literal or `$secret`
marker), and `probe.url`. Use a current Docker Compose plugin supporting
`config --no-env-resolution --format json` on the guest.

Candidate Compose and environment files are staged privately and validated,
including required service env files, before replacing installed configuration.
Relative paths retain the original project directory. Validation failure leaves
installed files intact; a subsequent pull/rollout failure may leave a partial
deployment and does **not** claim container rollback. Inspect that operation
before retrying. Delete preserves volumes and guest data.

An initial SSH failure proven to precede the first command releases the fresh
locator so target/key settings can be corrected. Once a command might have run,
or an existing binding was present, placement remains bound: inspect the original
guest and retry with the same host/project/directory rather than orphaning it.

## Managed Jellyfin On Proxmox

Open **Deploy Jellyfin** in the console. Select an existing managed dedicated
Ubuntu 24.04 amd64 NVIDIA guest, or provision one from a verified template.
Configure its read-only SMB media share, optional media subdirectory, pinned
official image, and controller-side `$secret` references for SMB/admin/viewer
credentials. The wizard previews each resource and lets you download its
reproducible manifests. Kubernetes is not required.

**Download staged manifests (.zip)** exports one numbered, single-resource YAML
file per stage; **Download current stage YAML** exports only the displayed stage.
Saved reviews also offer the ZIP before resuming, without observing or submitting
work. Labels, ordinary annotations, Unicode values and unresolved `$ref`/`$secret`
markers are preserved; request identities, consent, ownership and observed health
are not exported. Protect these files like manifests, not controller backups.

Extract outside watched GitOps directories. Preview and apply each file in stage
order, verifying upstream resources before proceeding. Public deployments include
separate initial and final Jellyfin files for the **same** resource; do not pass
the whole staged directory to a bulk planner or treat it as final GitOps intent.
Use `--create-only` for reviewed first creates; the final application stage updates
the existing application. A download does not recover accepted requests, prove
readiness or authorize replay/publication.

The existing-guest selector reads **saved VM metadata**, not a whole-lab live
scan. Unavailable unrelated Proxmox contexts do not hide saved choices. Names
are not proof of current existence, dedicated/GPU suitability, guest access or
application readiness: the fresh deployment preview resolves the selected VM
reference and blocks if it cannot be read. Active contexts for new provisioning
load independently. Saved-inventory failures are shown as unavailable, not an
empty lab; refresh explicitly, reselect the guest and renew consent before
preparing a review. No fallback provider scan or automatic Apply occurs.

For a new guest, choose **VM CPU cores**, **VM memory (MiB)** and **VM OS disk
(GiB)**; defaults remain 4 cores, 4096 MiB and 32 GiB. Values must be whole
numbers: CPU and disk positive, memory at least 512 MiB. The reviewed VM,
saved recovery record and downloaded declarations retain the chosen sizes.
These bounds are not a capacity check or reservation: verify host/storage
capacity, workload requirements and a disk at least as large as the template's
existing disk. Selecting an existing guest does not resize it.

Native Proxmox publishes `status.ip` only when the guest agent reports one
usable IPv4 address on NICs matching the hypervisor-configured MACs. Docker
bridges and other unmatched interfaces are not SSH targets. Missing MAC evidence,
no usable IPv4 or multiple distinct matching addresses leave `status.ip` absent;
dependent references block instead of guessing. Inspect guest networking, or
declare the application's `spec.target.host` explicitly for an intentionally
multi-address VM. Verify controller reachability and SSH host keys independently.

Blocked guided previews retain schema, field and dependency diagnostics inline,
including a failure discovered by the final preview immediately before Apply.
Field paths take precedence over duplicate plain-text messages; unrelated
diagnostics remain visible. A reported `create` operation is not authorization
when validation errors exist. No new request ID or Apply is issued for an invalid
preview. Repair the declaration or selected dependency, refresh the preview and
renew consent; accepted work is never canceled by a failed review.

Unresolved `$ref` diagnostics include the destination `fieldErrors[].path`
(for example, `spec.target.host`), the declared target/output and an optional
`code`. Invalid references, missing resources and unavailable fields have
distinct `reference_*` categories; typed TLS, authentication, permission, network
and provider-service failures share connection-test categories and repair hints.
Unknown provider failures use `reference_read_failed`. Consumers must tolerate
new or absent codes. Reference failures in previews and operation errors never
forward raw upstream error text, HTTP bodies, credential-bearing URLs or
certificate fields; wrapped causes remain available internally for classification.

Preview diagnostics are transient inspection data, not saved recovery evidence.
Reloading restores only the declaration and existing request identity; a fresh
review must fetch current diagnostics and authority again.

When previously accepted work blocks a later step, the review identifies its
exact API/kind/name and a reason code. Matching, unchanged application baselines
also show current readiness checks; unreadable resources, changed baselines and
unverified operations do not invent health evidence. **Inspect blocking resource**
opens its detail page. After repair, **Recheck prerequisites** obtains a fresh
preview and requires renewed consent, without replacing accepted requests or
submitting work. These diagnostics are not saved across reload. Raw read errors
are not repeated in the blocker; provider health messages remain sensitive
inspection output.

For CLI/GitOps, use `examples/jellyfin/vm.cue` and
`examples/jellyfin/application.cue` with private CUE values:

```bash
openctl ctl apply -f examples/jellyfin/vm.cue --values /secure/jellyfin-vm.cue
openctl ctl apply -f examples/jellyfin/application.cue --values /secure/jellyfin-app.cue
```

- Prepare host IOMMU/VFIO and a named Proxmox PCI mapping first.
  `examples/jellyfin/prepare-gpu.sh` is an explicit **Proxmox-host** operation
  that disconnects the selected physical GPU console; never run it implicitly.
- Verify the guest SSH host key and install it in the controller's
  `known_hosts`. Key and credential references are controller-side paths.
- `bootstrap: true` installs Docker/NVIDIA/CIFS only in the explicitly
  dedicated guest. The controller embeds its recipe; no script staging is
  required. If drivers require a reboot, Apply fails with instructions:
  explicitly reboot the guest and reapply the same manifest. It never reboots
  or recreates the guest automatically.
- Initial account onboarding runs behind guest loopback before enabling LAN
  access. Remote access is off unless explicitly declared. Config/cache stay
  on guest disk; NAS media is read-only.
- Application details expose container, HTTP, onboarding, media and GPU checks,
  logs, and start/stop/restart actions. Delete removes application containers
  but preserves configuration, volumes and media; deleting the VM is separate.
- Optional public exposure requires explicit consent and an existing connected
  Cloudflare tunnel whose connector can reach the guest. Route/DNS resources
  are previewed after local readiness; public HTTP is checked independently.
  DNS success alone is not application readiness.

The Jellyfin `media` check verifies the declared read-only CIFS share and UID-1000
guest access, then inspects the **running container**, not the desired Compose
file. `/media` must bind the exact selected NAS directory read-only, without
nested mounts overriding its contents. A read/traverse probe runs as UID 1000
inside that same inspected container ID. Wrong/writable bindings or denied access
are **not ready**; unavailable or incomplete runtime metadata is **unknown**.
Both block readiness. Observation never remounts, restarts or writes media, and
diagnostics omit raw mount metadata, container IDs and private probe errors.
This is not a real-file playback test.

For Jellyfin, `publicEndpoint` must return JSON system information (normally
`https://HOST/System/Info/Public`), completed onboarding and the same server `Id`
observed locally through verified guest SSH. HTTP 200 alone is insufficient:
login portals, malformed/oversized responses, unfinished onboarding and a
different Jellyfin instance are **not ready**. A valid public response without a
fresh guest-side identity is **unknown**, not green. Identity is transient
inspection data, never persisted to desired intent or provider placement.
Public probes send no credentials or cookies, reject redirects, retain HTTPS
certificate verification and cap Jellyfin response bodies at 64 KiB. Do not
weaken access policy to make a check pass. Instance agreement is not proof of
authorization or playback; generic Compose health URLs retain status-only checks.

Standalone application evidence uses `status.checks.<name>.state`:

| State | Meaning |
|-------|---------|
| `ready` | The attempted check passed. |
| `not_ready` | The attempted check failed or observed a missing/unhealthy runtime. |
| `unknown` | No usable evidence: for example, a dependent probe skipped because guest SSH is unavailable, or unreadable container output. |
| `not_configured` | Optional public exposure is absent; no public endpoint was tested or published. |

Resource detail and the guided review distinguish these states; only `ready`
is green. An absent public endpoint does not block a healthy private application.
A configured endpoint is checked independently even when guest access fails.
Required unknown/failed checks keep aggregate `status.ready` false and block
guided progression/publication. Console lists, details, child summaries and guided
gates also reject contradictory evidence: `ready: true` cannot override a failed,
unknown, malformed or obsolete reported check. A `ready`/`running` phase cannot
substitute for boolean readiness. Optional `not_configured` checks remain neutral.
This per-check `state` replaces the former `ready` boolean; update structured-output
consumers and upgrade console/controller together. Aggregate `status.ready` remains
a boolean. No check replaces a real playback test or authorizes a reboot, retry
or public exposure.

Changing the pinned image is an in-place application rollout. Target host,
project/directory and NAS placement are immutable to prevent orphaning data.
Test actual transcoded playback, including HDR if used: a healthy GPU probe
does not prove your media/client combination works.

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

### CI runners

All six Linux jobs in `.github/workflows/ci.yaml` use the repository's
self-hosted runner labeled `openctl-linux` for pushes to `main` and
same-repository pull requests. Fork pull requests use GitHub-hosted
`ubuntu-latest` runners instead, even when their workflow runs are approved.
Do not use `pull_request_target` to run fork code on the homelab runner.
Keep repository Actions settings at `all_external_contributors` approval.
The routing expression is not a security boundary: PRs can modify workflow
YAML, so review workflow changes before approving an external contributor's run.

CI installs Go 1.25, and the UI build explicitly installs Node 24.
Workflow tokens have read-only repository contents access, and checkouts do
not persist credentials. With one registered self-hosted runner, trusted CI
jobs execute sequentially and queue while that runner is busy or offline.

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
