# Homelab verification

Unit tests and fakes prove the logic; they cannot prove the controller drives
real VM and k3s-cluster lifecycles on *your* Proxmox hardware. That last mile —
timing, QEMU guest-agent quirks, template edges, real network behavior — only a
run against metal validates. `hack/homelab-verify.sh` is a reusable, safe
harness for exactly that.

The [bounded console/application acceptance checkpoint](#bounded-consoleapplication-acceptance)
below records additional native-provider evidence; it is not a run of the
broader stopped-VM/k3s harness.

## What it checks

| Stage | Proves |
|-------|--------|
| **Preflight** (`--dry-run`) | Controller reachable + authenticated, required config present, sandbox names free. Mutates nothing. |
| **VM lifecycle** (default) | Create a VM → **in-place resize** (memory + disk grow) proven by a *stable vmid* → disk **shrink rejected** → **idempotent** unchanged re-apply → delete → confirmed gone. |
| **Cluster lifecycle** (`--cluster`) | Apply a small k3s cluster → nodes provision + join → delete → confirmed gone. |

It drives the **real `openctl` CLI**, so it exercises the exact path you use, not
a stand-in. `ctl apply`/`ctl delete` block until the op completes, so pass/fail
rides on their exit codes. Observation goes through **`openctl proxmox get vms`**,
not `ctl get`: `ctl get` lists only openctl-managed VMs, while `proxmox get vms`
sees every VM on the node (so the collision guard can't be fooled) and its `-o
yaml` surfaces the **vmid** — the field that proves an in-place resize did not
recreate the VM.

The verify VM is created **stopped** on purpose: booting a guest is unnecessary
for a resize test and makes the harness slower. Get/List now report configured
CPU/RAM from Proxmox's configuration API, even while a running guest still uses
its pre-reboot allocation. Guest-visible resources can therefore differ until
the guest applies the change or reboots; the configuration API is authoritative
for validating the resize.

## Safety

- **Only touches names it owns.** `VERIFY_VM_NAME` / `VERIFY_CLUSTER_NAME` are
  prefixed `openctl-verify-` and the harness **refuses to start if they already
  exist** — it can't stomp a real resource.
- **Cleans up on exit** (trap) unless you pass `--keep`.
- **`--dry-run` mutates nothing** — run it first, always.
- Uses your homelab's safe IP range (`.235–.238` by default) for the cluster,
  and a static VM IP you set outside that range.

## Prerequisites

Do the [QUICKSTART](../QUICKSTART.md) first. The harness assumes:

1. `openctl-controller` is running (`openctl ping` succeeds).
2. A Proxmox endpoint + API token are configured in the controller (secret via
   `tokenSecretFile` — the harness never handles credentials).
3. A cloud-init template exists on the target node.

## Run it

```sh
cp hack/homelab-verify.env.example homelab-verify.env
$EDITOR homelab-verify.env         # endpoint node, template, SSH keys, ranges

hack/homelab-verify.sh --dry-run   # 1. preflight only — safe, ~seconds
hack/homelab-verify.sh             # 2. VM lifecycle — ~1-3 min
hack/homelab-verify.sh --cluster   # 3. + cluster lifecycle — ~10-20 min
```

Useful flags: `--cluster-only`, `--vm-only`, `--keep` (leave resources up for
inspection), `-h`.

## Recommended first-run sequence

This mirrors the safest onboarding order — narrow blast radius first:

1. `--dry-run` until preflight is all green.
2. VM lifecycle. This validates the core apply → reconcile → observe → delete
   loop on your metal before any composite complexity.
3. `--cluster` once the VM path is solid.
4. Practice [offline controller backup/restore](../README.md#back-up-and-recover-the-controller)
   before relying on automation. Stop the daemon and use the actual config/state
   roots; a live filesystem copy or manifest git history is not a complete backup.

## Reading the in-place resize check

The VM stage re-applies with a bumped memory value. openctl updates an existing
VM **in place** for the resizable fields — memory, CPU (cores/sockets), and disk
**growth** — without recreating it (see `CONTROLLER.md`, "Apply on existing
atomic resource"). So the expected result is: re-apply succeeds and the VM's
memory reflects the new value. Non-resizable changes (template, networks) are
not applied in place and still require delete + re-apply; disk **shrink** is
rejected with a clear error.

## Note on the harness itself

The harness is validated for shape (shellcheck, `bash -n`) and uses verified CLI
commands, but its **first real run against your hardware is itself part of the
verification** — that run both exercises openctl and shakes out any
environment-specific rough edges here. Report failures with `openctl ctl op
list` / `openctl ctl op get <id>` output.

## Cloud-image disks and cloud-init

Cloud-image creation must apply disk options after cloning and resizing, before
starting the guest. Check `qm config <vmid>` on the Proxmox node: a requested
300G SSD with TRIM must retain its volume reference and show `size=300G`,
`ssd=1`, and `discard=on`. This path was exercised on Proxmox 9.1.1; the
regression test also checks that a rejected option update prevents startup.

Proxmox's HTTP upload API does not accept `content=snippets`, even when the
storage supports snippets. Vendor-data delivery uses the context's explicit
`snippetSSH` node-to-host mapping instead. Verify the node's SSH host key and
configure an identity available to the controller process; the SSH user needs
`pvesm path` and write access to the selected snippet directory. Uploads replace
the destination atomically. See the [provider configuration](../README.md) for
the context fields.

A successful apply proves VM provisioning, not completion of first-boot
cloud-init. Check cloud-init's final status and bootstrap results inside the
guest, including its network and installed tools, before making a recovery
snapshot. Keep warnings distinct from bootstrap failures; never infer readiness
from the VM merely being running.

For example, cloud-init 26.1 reports `degraded done` and exits 2 for the deprecated
string `user` field generated by Proxmox 9.1.1, even with no bootstrap errors.
Its JSON status distinguishes `status: done`, an empty `errors` list, and
`recoverable_errors.DEPRECATED` warnings. Retain and report those warnings;
accept a completed bootstrap only after checking its own readiness results.

## Cloud-image checksum verification

`cloudImage.checksum` accepts `algorithm:hex-digest`: md5, sha1, sha224, sha256,
sha384, or sha512, with the full digest length required by that algorithm.
Prefer SHA-256 or SHA-512 and obtain the expected digest from a trusted publisher.
Omitting the field leaves downloads unverified. Malformed and unsupported
checksums are rejected before template lookup.

openctl passes the digest and algorithm to Proxmox's `download-url` API and
requires a successful task `exitstatus` before creating or importing the template.
A stopped task is not necessarily successful. Successful imports carry an
`openctl-image-<algorithm>-<digest>` provenance tag. A pinned request can reuse a
cached template only when that tag matches. Legacy unverified templates or a
different digest fail closed: choose an unused `cloudImage.templateName` to build
a newly verified template. Do not manually add a provenance tag to bypass this
check.

The native handler was exercised on Proxmox 9.1.1 with a disposable 1 MiB raw
disk, never booted: a wrong SHA-256 failed before any VM/template creation, the
correct digest imported and cloned successfully, a second VM reused the verified
template without another download, and a changed digest could not use that
cache. Both sandbox VMs, their template, and the downloaded image were removed;
the existing development VM remained running.

## Bounded console/application acceptance

**2026-11-27.** Exercised a real Proxmox 9.1.1 lab with an explicitly authorized
disposable guest: one vCPU, 1024 MiB RAM, 8 GiB disk, DHCP, no GPU passthrough
and no public endpoint. Existing development and Jellyfin guests were retained.

- Verified both configured Proxmox endpoints with their CA and certificate
  identity. Corrected a hostname/SAN mismatch through verified local name
  resolution, not disabled TLS verification.
- Created the sandbox, exported/released its tracking, and adopted the exact
  discovered instance through the actual console. Resized memory in place with
  stable physical identity. Deleted/recreated the sandbox and proved replay of
  the old successful Delete request did not delete the replacement.
- Verified cloud-init completion and guest-agent networking independently of
  Apply success. The known deprecated string-user warning remained visible;
  bootstrap errors were empty.
- Pinned the guest's SSH host key through the already trusted hypervisor/guest
  agent. A real multi-key OpenSSH server exposed the client's key-negotiation
  mismatch; the fix selects only algorithms supported by verified known-host
  entries and retains strict verification, including hashed/nondefault-port
  tests.
- Installed publisher-checksummed Compose 5.8.1 in only the disposable guest.
  The previously installed 2.40 plugin now fails Preview with an explicit
  supported-version diagnostic. Then deleted/recreated the guest from the
  pinned cloud-init declaration: first boot downloaded/checksummed the plugin
  itself, completed with no bootstrap errors, and the native app rollout served
  healthy v2 HTTP without any manual plugin upload. Changing cloud-init on an
  already-running guest is not a bootstrap rerun.
- Deployed an actual digest-pinned Compose HTTP app through native Apply,
  including recovery of a failed first rollout's owned partial state with a
  new create-only request. Neither adoption nor deletion of ownership metadata
  was needed. Raw-intent no-op Preview followed by Apply refreshed the same
  environment secret reference: actual guest-local HTTP changed from v1 to v2.
- Native Stop produced non-ready application/HTTP checks and nonzero health
  exit; Start restored healthy containers and exact v2 content. With an
  explicitly declared `restart: unless-stopped`, native guest Reboot changed the
  boot ID while preserving VM identity and disk. The app recovered healthy v2
  HTTP without another Apply.
- Created a fresh offline controller backup and restored it into a new private
  HOME. Every SQLite table's records matched before boot, including four saved
  declarations, one opaque app locator, one immutable provider binding and
  eleven durable request bindings. The referenced environment file was present
  and byte-identical.
- Booted that current-state restore authenticated and loopback-only, with the
  original controller stopped, no queued/running operations, mirror authority
  and the restored copy's reconciler explicitly disabled. Fresh Proxmox Get,
  app SSH/container/HTTP health and original request-ID lookup succeeded.
  Desired markers, provider state/bindings, request ledger and controller
  identity remained unchanged. Stopped the copy before resuming the original.
- On the existing GPU-backed Jellyfin instance, a distinct regular client
  requested the final two seconds of an existing HEVC video with stream copy
  disabled. HTTP 200 returned 365472 MPEG-TS bytes and reached natural EOF in
  5.04 seconds. Correlated FFmpeg output showed `h264_nvenc`; decoding that exact
  response produced a 640x360 H.264 frame. Task authentication sessions were
  logged out, and direct container process inspection found no remaining
  FFmpeg process. No configuration/library/media mutation was requested.
- Installed the checked fixes and inspected the actual console: the healthy
  reference-backed app no longer showed false target-host drift, and its latest
  successful request superseded earlier failures in topology. Operation badges
  are explicitly historical operation status, not provider health. Both native
  CLI and the reviewed console action produced a real zero-byte quiet-log
  download; the CLI file was exclusive and mode 0600.

**Limits:** this is a same-host recovery drill, not proof of an unattended
fresh-machine disaster recovery. SSH identities/known-host trust, name
resolution, external application paths, provider access and guest/NAS data must
be restored separately and reviewed. The earlier legacy snapshot predates
immutable identity and durable request tables; its byte-exact offline restore
must not be described as preserving records that never existed.

Fresh end-to-end GPU guest provisioning/onboarding, host-power-loss recovery,
public DNS/TLS exposure and the k3s harness were not exercised under this bounded
approval. Existing GPU playback is stronger than a health banner, but is not a
fresh Jellyfin deployment certificate.

Both sandbox incarnations used for the final app/recovery checks were removed
through native app-then-VM Delete. Final physical checks found the sandbox VM
absent and the existing development/Jellyfin VMs still running; saved inventory
returned to its original two declarations. Removed the live fixture secret and
throwaway installer/download tools, retained private evidence archives, and
took a fresh post-cleanup offline backup before resuming the installed
controller. Earlier acceptance snapshots remain historical, not permission to
recreate or replay their now-deleted fixtures.
