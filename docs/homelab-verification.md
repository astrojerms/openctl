# Homelab verification

Unit tests and fakes prove the logic; they cannot prove the controller drives
real VM and k3s-cluster lifecycles on *your* Proxmox hardware. That last mile —
timing, QEMU guest-agent quirks, template edges, real network behavior — only a
run against metal validates. `hack/homelab-verify.sh` is a reusable, safe
harness for exactly that.

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
4. Snapshot `~/.openctl` before and after until you trust the state layer.

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
