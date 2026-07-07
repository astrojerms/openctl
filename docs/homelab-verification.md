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

The verify VM is created **stopped** on purpose. A resize test doesn't need a
booted guest, it's faster, and it keeps memory observation reliable: `proxmox
get vms` reports a *running* VM's live `MaxMem`, which lags a config change until
reboot — so a running VM would show the old memory even though the config was
correctly updated. (This nuance was found on the first real-hardware run and is
why the harness now creates stopped.)

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
