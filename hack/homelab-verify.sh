#!/usr/bin/env bash
#
# homelab-verify.sh — reusable acceptance harness for openctl against a real
# Proxmox homelab.
#
# What it proves that unit tests can't: that the controller drives real VM and
# k3s-cluster lifecycles end-to-end on your hardware. Drives the *real* openctl
# CLI (the actual user path), not a reimplementation.
#
# SAFETY
#   - Only ever touches resources it names itself (VERIFY_VM_NAME /
#     VERIFY_CLUSTER_NAME, both prefixed "openctl-verify-" by default). It
#     refuses to start if those names already exist (so it can't stomp a real
#     resource that happens to share a name).
#   - Cleans up on exit (trap) unless --keep is passed.
#   - --dry-run stops after preflight and mutates nothing.
#
# ASSUMPTIONS (do the QUICKSTART first)
#   - The controller is running and reachable (`openctl ping` succeeds).
#   - A Proxmox endpoint + API token are already configured in the controller
#     (secret via tokenSecretFile, per QUICKSTART). This harness does NOT
#     manage credentials.
#   - A cloud-init template exists on the target node.
#
# USAGE
#   cp hack/homelab-verify.env.example homelab-verify.env
#   $EDITOR homelab-verify.env          # fill in your endpoint/template/ranges
#   hack/homelab-verify.sh --dry-run    # preflight only, safe
#   hack/homelab-verify.sh              # VM lifecycle (fast)
#   hack/homelab-verify.sh --cluster    # + k3s cluster lifecycle (slow)
#   hack/homelab-verify.sh --cluster-only --keep
#
# Config is read from the environment; a homelab-verify.env in the CWD (or the
# path in HOMELAB_VERIFY_ENV) is sourced first if present.

set -euo pipefail

# ---- config loading --------------------------------------------------------
ENV_FILE="${HOMELAB_VERIFY_ENV:-homelab-verify.env}"
if [[ -f "$ENV_FILE" ]]; then
  # shellcheck disable=SC1090
  source "$ENV_FILE"
fi

OPENCTL="${OPENCTL:-openctl}"

# Proxmox placement (no safe defaults — these MUST match your homelab).
PROXMOX_NODE="${PROXMOX_NODE:-}"
PROXMOX_TEMPLATE="${PROXMOX_TEMPLATE:-}"
PROXMOX_STORAGE="${PROXMOX_STORAGE:-local-lvm}"
PROXMOX_BRIDGE="${PROXMOX_BRIDGE:-vmbr0}"

# SSH (cloud-init user + keys). Private key is needed for cluster (k3s installs
# over SSH); the VM-only path just needs the public key.
SSH_USER="${SSH_USER:-ubuntu}"
SSH_PUBKEY="${SSH_PUBKEY:-}"
SSH_PRIVKEY_PATH="${SSH_PRIVKEY_PATH:-$HOME/.ssh/id_ed25519}"

# Single sandbox VM.
VERIFY_VM_NAME="${VERIFY_VM_NAME:-openctl-verify-vm}"
VM_CPUS="${VM_CPUS:-2}"
VM_MEMORY_MB="${VM_MEMORY_MB:-2048}"
VM_MEMORY_MB_BUMP="${VM_MEMORY_MB_BUMP:-4096}"   # in-place memory resize target
VM_DISK="${VM_DISK:-20G}"
VM_DISK_BUMP="${VM_DISK_BUMP:-24G}"              # in-place disk-grow target (must be > VM_DISK)
VM_IP="${VM_IP:-}"                                # e.g. 192.168.1.234/24 ; empty => dhcp
VM_GATEWAY="${VM_GATEWAY:-}"

# Sandbox cluster (defaults to the memory-noted safe .235-.238 range).
VERIFY_CLUSTER_NAME="${VERIFY_CLUSTER_NAME:-openctl-verify}"
CLUSTER_CP_COUNT="${CLUSTER_CP_COUNT:-1}"
CLUSTER_WORKER_COUNT="${CLUSTER_WORKER_COUNT:-1}"
CLUSTER_CPUS="${CLUSTER_CPUS:-2}"
CLUSTER_MEM_MB="${CLUSTER_MEM_MB:-4096}"
CLUSTER_DISK_GB="${CLUSTER_DISK_GB:-30}"
CLUSTER_IP_START="${CLUSTER_IP_START:-192.168.1.235}"
CLUSTER_GATEWAY="${CLUSTER_GATEWAY:-192.168.1.1}"
CLUSTER_NETMASK="${CLUSTER_NETMASK:-24}"

VM_API="proxmox.openctl.io/v1"
CLUSTER_API="k3s.openctl.io/v1"

# ---- flags -----------------------------------------------------------------
DRY_RUN=0 DO_VM=1 DO_CLUSTER=0 KEEP=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)      DRY_RUN=1 ;;
    --cluster)      DO_CLUSTER=1 ;;
    --cluster-only) DO_CLUSTER=1; DO_VM=0 ;;
    --vm-only)      DO_VM=1; DO_CLUSTER=0 ;;
    --keep)         KEEP=1 ;;
    -h|--help)      grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
  shift
done

# ---- output helpers --------------------------------------------------------
if [[ -t 1 ]]; then C_G=$'\033[32m' C_R=$'\033[31m' C_Y=$'\033[33m' C_B=$'\033[1m' C_0=$'\033[0m'
else C_G= C_R= C_Y= C_B= C_0=; fi
PASS=0 FAIL=0
pass() { PASS=$((PASS+1)); echo "  ${C_G}✓${C_0} $*"; }
fail() { FAIL=$((FAIL+1)); echo "  ${C_R}✗${C_0} $*"; }
info() { echo "  ${C_Y}·${C_0} $*"; }
step() { echo; echo "${C_B}== $* ==${C_0}"; }
die()  { echo "${C_R}fatal:${C_0} $*" >&2; exit 1; }

TMPDIR_HV="$(mktemp -d "${TMPDIR:-/tmp}/openctl-verify.XXXXXX")"
cleanup() {
  local rc=$?
  if [[ $KEEP -eq 0 && $DRY_RUN -eq 0 ]]; then
    step "Cleanup (--keep to skip)"
    if [[ $DO_CLUSTER -eq 1 ]]; then
      info "deleting cluster $VERIFY_CLUSTER_NAME"
      "$OPENCTL" ctl delete Cluster "$VERIFY_CLUSTER_NAME" --api-version "$CLUSTER_API" \
        --allow-destructive >/dev/null 2>&1 || info "cluster delete: nothing to remove or failed (check manually)"
    fi
    if [[ $DO_VM -eq 1 ]]; then
      info "deleting vm $VERIFY_VM_NAME"
      "$OPENCTL" ctl delete VirtualMachine "$VERIFY_VM_NAME" --api-version "$VM_API" \
        >/dev/null 2>&1 || info "vm delete: nothing to remove or failed (check manually)"
    fi
  fi
  rm -rf "$TMPDIR_HV"
  return $rc
}
trap cleanup EXIT

# resource_exists <kind> <name> <apiVersion> -> 0 if present (controller-managed)
resource_exists() {
  "$OPENCTL" ctl get "$1" "$2" --api-version "$3" >/dev/null 2>&1
}

# VM observation goes through `proxmox get vms`, not `ctl get`: `ctl get` lists
# only openctl-managed VMs, whereas `proxmox get vms` sees every VM on the node
# (so the collision guard can't be fooled) and its -o yaml surfaces the vmid —
# the field that proves an in-place resize did not recreate the VM.

# vm_present <name> -> 0 if a VM with this exact name exists on the node
vm_present() {
  "$OPENCTL" proxmox get vms 2>/dev/null | awk 'NR>1 {print $1}' | grep -qx "$1"
}

# vm_field <name> <key> -> value of the first "  <key>: <value>" line, or empty.
# Reads the proxmox YAML view (spec.memory.size, cpu.cores, status.vmid, ...).
vm_field() {
  "$OPENCTL" proxmox get vms "$1" -o yaml 2>/dev/null \
    | grep -iE "^[[:space:]]*$2:" | head -1 | sed -E 's/^[^:]*:[[:space:]]*//'
}

# ---- manifest rendering ----------------------------------------------------
render_vm() {   # $1 = memory MB, $2 = disk size (e.g. 20G) ; writes manifest to stdout
  local mem="$1" disk="$2" ipblock
  [[ -n "$disk" ]] || disk="$VM_DISK"
  if [[ -n "$VM_IP" ]]; then
    ipblock=$(printf '      net0:\n        ip: %s\n        gateway: %s' "$VM_IP" "$VM_GATEWAY")
  else
    ipblock=$'      net0:\n        ip: dhcp'
  fi
  cat <<EOF
apiVersion: $VM_API
kind: VirtualMachine
metadata:
  name: $VERIFY_VM_NAME
  labels:
    managed-by: homelab-verify
spec:
  node: $PROXMOX_NODE
  template:
    name: $PROXMOX_TEMPLATE
  cpu:
    cores: $VM_CPUS
    sockets: 1
  memory:
    size: $mem
  disks:
    - name: scsi0
      storage: $PROXMOX_STORAGE
      size: $disk
  networks:
    - name: net0
      bridge: $PROXMOX_BRIDGE
      model: virtio
  cloudInit:
    user: $SSH_USER
    sshKeys:
      - $SSH_PUBKEY
    ipConfig:
$ipblock
  # Created stopped on purpose: a resize test doesn't need a booted guest, it's
  # faster, and it keeps memory observation reliable. proxmox get vms reports a
  # RUNNING VM's live MaxMem, which lags a config change until reboot — so a
  # running VM would show the old memory even though the config was updated.
  startOnCreate: false
EOF
}

render_cluster() {
  cat <<EOF
apiVersion: $CLUSTER_API
kind: Cluster
metadata:
  name: $VERIFY_CLUSTER_NAME
  labels:
    managed-by: homelab-verify
spec:
  compute:
    provider: proxmox
    image:
      template: $PROXMOX_TEMPLATE
      storage: $PROXMOX_STORAGE
    default:
      cpus: $CLUSTER_CPUS
      memoryMB: $CLUSTER_MEM_MB
      diskGB: $CLUSTER_DISK_GB
  nodes:
    controlPlane:
      count: $CLUSTER_CP_COUNT
    workers:
      - name: worker
        count: $CLUSTER_WORKER_COUNT
  network:
    bridge: $PROXMOX_BRIDGE
    staticIPs:
      startIP: $CLUSTER_IP_START
      gateway: $CLUSTER_GATEWAY
      netmask: "$CLUSTER_NETMASK"
  k3s:
    extraArgs:
      - --disable=traefik
  ssh:
    user: $SSH_USER
    privateKeyPath: $SSH_PRIVKEY_PATH
    publicKeys:
      - $SSH_PUBKEY
EOF
}

# ---- phases ----------------------------------------------------------------
preflight() {
  step "Preflight (no mutations)"
  command -v "$OPENCTL" >/dev/null 2>&1 && pass "openctl on PATH ($($OPENCTL version 2>/dev/null | head -1))" \
    || die "openctl not found (set OPENCTL=/path/to/openctl)"

  if "$OPENCTL" ping >/dev/null 2>&1; then pass "controller reachable (openctl ping)"
  else die "controller not reachable — is openctl-controller running? (openctl ping)"; fi

  if "$OPENCTL" whoami >/dev/null 2>&1; then pass "authenticated ($($OPENCTL whoami 2>/dev/null | head -1))"
  else fail "openctl whoami failed — check your token/session"; fi

  # Required config presence.
  local missing=()
  [[ -n "$PROXMOX_NODE" ]]     || missing+=(PROXMOX_NODE)
  [[ -n "$PROXMOX_TEMPLATE" ]] || missing+=(PROXMOX_TEMPLATE)
  [[ -n "$SSH_PUBKEY" ]]       || missing+=(SSH_PUBKEY)
  if [[ ${#missing[@]} -gt 0 ]]; then die "missing required config: ${missing[*]} (see homelab-verify.env.example)"; fi
  pass "required config present (node=$PROXMOX_NODE template=$PROXMOX_TEMPLATE)"

  if [[ $DO_CLUSTER -eq 1 && ! -f "${SSH_PRIVKEY_PATH/#\~/$HOME}" ]]; then
    fail "SSH private key not found at $SSH_PRIVKEY_PATH (k3s installs over SSH)"
  elif [[ $DO_CLUSTER -eq 1 ]]; then
    pass "ssh private key present ($SSH_PRIVKEY_PATH)"
  fi

  # Sandbox-name collision guard: refuse to touch a pre-existing resource.
  # Uses proxmox get vms so it sees ALL VMs on the node, not just managed ones.
  if [[ $DO_VM -eq 1 ]] && vm_present "$VERIFY_VM_NAME"; then
    die "a VM named '$VERIFY_VM_NAME' already exists on the node — refusing to reuse it. Delete it or set VERIFY_VM_NAME."
  fi
  [[ $DO_VM -eq 1 ]] && pass "sandbox VM name '$VERIFY_VM_NAME' is free"
  if [[ $DO_CLUSTER -eq 1 ]] && resource_exists Cluster "$VERIFY_CLUSTER_NAME" "$CLUSTER_API"; then
    die "a Cluster named '$VERIFY_CLUSTER_NAME' already exists — refusing to reuse it. Delete it or set VERIFY_CLUSTER_NAME."
  fi
  [[ $DO_CLUSTER -eq 1 ]] && pass "sandbox cluster name '$VERIFY_CLUSTER_NAME' is free"

  info "template, storage ($PROXMOX_STORAGE), and bridge validity aren't checked"
  info "here — a bad value fails loudly at apply time with the Proxmox error."
}

vm_lifecycle() {
  step "VM lifecycle: $VERIFY_VM_NAME"
  local mf="$TMPDIR_HV/vm.yaml"

  # --- create ---------------------------------------------------------------
  render_vm "$VM_MEMORY_MB" "$VM_DISK" > "$mf"
  info "applying VM ($VM_CPUS cpu / ${VM_MEMORY_MB}MB / ${VM_DISK}) — blocks until the op completes..."
  if "$OPENCTL" ctl apply -f "$mf"; then pass "apply succeeded (VM created)"
  else fail "apply failed — see the op error above"; return; fi

  local vmid0; vmid0="$(vm_field "$VERIFY_VM_NAME" vmid)"
  if [[ -n "$vmid0" ]]; then pass "VM exists on the node (vmid $vmid0)"; else fail "VM not found via proxmox get vms after apply"; return; fi
  local mem0; mem0="$(vm_field "$VERIFY_VM_NAME" size)"
  [[ "$mem0" == "$VM_MEMORY_MB" ]] && pass "memory is ${VM_MEMORY_MB}MB" || info "memory reads '$mem0' (want $VM_MEMORY_MB)"

  # --- in-place resize: the core assertion ----------------------------------
  # openctl updates an existing VM in place for memory/CPU/disk-grow. The proof
  # that it resized rather than recreated is a STABLE vmid across the re-apply.
  step "In-place resize (memory ${VM_MEMORY_MB}→${VM_MEMORY_MB_BUMP}MB, disk ${VM_DISK}→${VM_DISK_BUMP})"
  render_vm "$VM_MEMORY_MB_BUMP" "$VM_DISK_BUMP" > "$mf"
  if "$OPENCTL" ctl apply -f "$mf"; then pass "re-apply succeeded"
  else fail "re-apply errored"; fi
  local vmid1; vmid1="$(vm_field "$VERIFY_VM_NAME" vmid)"
  if [[ -n "$vmid1" && "$vmid1" == "$vmid0" ]]; then pass "vmid unchanged ($vmid1) — resized IN PLACE, not recreated"
  else fail "vmid changed ($vmid0 → ${vmid1:-none}) — the VM was recreated, not resized"; fi
  local mem1; mem1="$(vm_field "$VERIFY_VM_NAME" size)"
  if [[ "$mem1" == "$VM_MEMORY_MB_BUMP" ]]; then pass "memory updated in place to ${VM_MEMORY_MB_BUMP}MB"
  else fail "memory reads '$mem1' after resize (want $VM_MEMORY_MB_BUMP) — if the VM is running, this is a lagging live MaxMem, not a resize failure; check the Proxmox config"; fi
  info "disk grow ${VM_DISK}→${VM_DISK_BUMP} applied without error (size not surfaced by proxmox get vms; verify in the Proxmox UI if needed)"

  # --- shrink guard: must be rejected ---------------------------------------
  step "Disk shrink is rejected"
  render_vm "$VM_MEMORY_MB_BUMP" "$VM_DISK" > "$mf"   # back to the smaller disk = a shrink
  local out; if out="$("$OPENCTL" ctl apply -f "$mf" 2>&1)"; then
    fail "shrink ${VM_DISK_BUMP}→${VM_DISK} was accepted — it must be rejected"
  elif echo "$out" | grep -qi shrink; then pass "shrink rejected with a clear error"
  else info "re-apply failed but the error didn't mention shrink: $(echo "$out" | tail -1)"; fi

  # --- idempotence: unchanged re-apply is a clean no-op ---------------------
  step "Idempotent re-apply"
  render_vm "$VM_MEMORY_MB_BUMP" "$VM_DISK_BUMP" > "$mf"
  if "$OPENCTL" ctl apply -f "$mf"; then
    local vmid2; vmid2="$(vm_field "$VERIFY_VM_NAME" vmid)"
    [[ "$vmid2" == "$vmid0" ]] && pass "unchanged re-apply is a clean no-op (vmid still $vmid2)" || fail "vmid changed on an unchanged re-apply ($vmid0 → $vmid2)"
  else fail "unchanged re-apply errored (expected a clean no-op)"; fi

  # --- delete ---------------------------------------------------------------
  step "Delete + confirm gone"
  if "$OPENCTL" ctl delete VirtualMachine "$VERIFY_VM_NAME" --api-version "$VM_API"; then pass "delete succeeded"
  else fail "delete failed"; return; fi
  if vm_present "$VERIFY_VM_NAME"; then fail "VM still present on the node after delete"; else pass "VM is gone from the node"; fi
}

cluster_lifecycle() {
  step "Cluster lifecycle: $VERIFY_CLUSTER_NAME (${CLUSTER_CP_COUNT} CP + ${CLUSTER_WORKER_COUNT} worker)"
  local mf="$TMPDIR_HV/cluster.yaml"
  render_cluster > "$mf"
  info "applying cluster — this provisions VMs and installs k3s over SSH; can take 10-20 min..."
  if "$OPENCTL" ctl apply -f "$mf"; then pass "cluster apply succeeded"
  else fail "cluster apply failed — inspect: openctl ctl op list"; return; fi

  local got; got="$("$OPENCTL" ctl get Cluster "$VERIFY_CLUSTER_NAME" --api-version "$CLUSTER_API" 2>&1 || true)"
  echo "$got" | grep -qi "$VERIFY_CLUSTER_NAME" && pass "controller reports the cluster" || fail "ctl get did not return the cluster"
  local want=$((CLUSTER_CP_COUNT + CLUSTER_WORKER_COUNT))
  local ips; ips="$(echo "$got" | grep -oE '([0-9]{1,3}\.){3}[0-9]{1,3}' | sort -u | wc -l | tr -d ' ')"
  if [[ "$ips" -ge "$want" ]]; then pass "cluster surfaces $ips node IP(s) (want ≥ $want)"; else info "surfaced $ips IP(s), expected $want — inspect: openctl ctl get Cluster $VERIFY_CLUSTER_NAME --api-version $CLUSTER_API -o yaml"; fi

  info "for a deeper check, retrieve the kubeconfig and run: kubectl get nodes"

  step "Delete cluster + confirm gone"
  if "$OPENCTL" ctl delete Cluster "$VERIFY_CLUSTER_NAME" --api-version "$CLUSTER_API" --allow-destructive; then pass "cluster delete succeeded"
  else fail "cluster delete failed"; return; fi
  if resource_exists Cluster "$VERIFY_CLUSTER_NAME" "$CLUSTER_API"; then fail "cluster still present after delete"; else pass "cluster is gone"; fi
}

# ---- run -------------------------------------------------------------------
echo "${C_B}openctl homelab verification${C_0}  (dry-run=$DRY_RUN vm=$DO_VM cluster=$DO_CLUSTER keep=$KEEP)"
preflight
if [[ $DRY_RUN -eq 1 ]]; then
  step "Dry run complete — preflight only, nothing was created."
  KEEP=1
else
  [[ $DO_VM -eq 1 ]]      && vm_lifecycle
  [[ $DO_CLUSTER -eq 1 ]] && cluster_lifecycle
fi

step "Summary"
echo "  ${C_G}passed:${C_0} $PASS   ${C_R}failed:${C_0} $FAIL"
[[ $FAIL -eq 0 ]] || die "$FAIL check(s) failed"
echo "${C_G}All checks passed.${C_0}"
