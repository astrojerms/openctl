#!/bin/bash
# End-to-end test for creating a VM from a cloud image URL
#
# This test uses the cloudImage workflow which:
# 1. Downloads the cloud image from URL into Proxmox storage
# 2. Creates a template from the image
# 3. Clones the template to create the VM
# 4. Applies cloud-init settings
#
# NO MANUAL PROXMOX SETUP REQUIRED - fully automated!

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Source local config if exists (gitignored)
if [[ -f "${SCRIPT_DIR}/.env.e2e" ]]; then
    # shellcheck source=/dev/null
    source "${SCRIPT_DIR}/.env.e2e"
fi

# Configuration - override via .env.e2e or environment variables
VM_NAME="e2e-test-vm-$(date +%s)"
MANIFEST_FILE="${SCRIPT_DIR}/vm-test-manifest.yaml"
CLEANUP_ON_EXIT="${CLEANUP_ON_EXIT:-true}"

# Proxmox settings
PROXMOX_NODE="${PROXMOX_NODE:-pve1}"
PROXMOX_STORAGE="${PROXMOX_STORAGE:-local}"
PROXMOX_DISK_STORAGE="${PROXMOX_DISK_STORAGE:-local-lvm}"
SSH_PUBLIC_KEY="${SSH_PUBLIC_KEY:-}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

cleanup() {
    if [[ "$CLEANUP_ON_EXIT" == "true" ]]; then
        log_info "Cleaning up: deleting VM ${VM_NAME}..."
        if openctl proxmox delete vms "${VM_NAME}" 2>/dev/null; then
            log_info "VM ${VM_NAME} deleted successfully"
        else
            log_warn "VM ${VM_NAME} may not exist or deletion failed (this is OK if create failed)"
        fi
        # Clean up manifest file
        rm -f "${MANIFEST_FILE}"
    else
        log_warn "Skipping cleanup (CLEANUP_ON_EXIT=false)"
    fi
}

# Always cleanup on exit
trap cleanup EXIT

# Get SSH public key
get_ssh_public_key() {
    if [[ -n "$SSH_PUBLIC_KEY" ]]; then
        echo "$SSH_PUBLIC_KEY"
        return
    fi

    # Try to read from default location
    local pub_key_path="$HOME/.ssh/id_ed25519.pub"
    if [[ -f "$pub_key_path" ]]; then
        cat "$pub_key_path"
    else
        log_error "SSH public key not found. Set SSH_PUBLIC_KEY in .env.e2e"
        exit 1
    fi
}

# Generate test manifest with unique VM name
# Using the cloudImage workflow - fully automated
generate_manifest() {
    local ssh_pub_key
    ssh_pub_key=$(get_ssh_public_key)

    cat > "${MANIFEST_FILE}" << EOF
apiVersion: proxmox.openctl.io/v1
kind: VirtualMachine
metadata:
  name: ${VM_NAME}
spec:
  node: ${PROXMOX_NODE}
  cloudImage:
    # Ubuntu 22.04 (Jammy) cloud image - will be downloaded and cached as a template
    url: https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img
    # Storage for downloading the image and creating template
    storage: ${PROXMOX_STORAGE}
    # Storage for the VM disk (can be different, e.g., local-lvm for thin provisioning)
    diskStorage: ${PROXMOX_DISK_STORAGE}
  cpu:
    cores: 2
  memory:
    size: 2048
  disks:
    - name: scsi0
      size: 20G
  networks:
    - name: net0
      bridge: vmbr0
  cloudInit:
    user: ubuntu
    sshKeys:
      - ${ssh_pub_key}
    ipConfig:
      net0:
        ip: dhcp
  startOnCreate: false
EOF
    log_info "Generated test manifest: ${MANIFEST_FILE}"
}

# Test: Create VM
test_create_vm() {
    log_info "TEST: Creating VM ${VM_NAME}..."
    log_info "Manifest content:"
    cat "${MANIFEST_FILE}"
    echo ""

    local output
    if output=$(openctl --debug proxmox create vms -f "${MANIFEST_FILE}" 2>&1); then
        log_info "PASS: VM created successfully"
        echo "$output" | grep -E "(created|VMID|vmid)" || true
        return 0
    else
        log_error "FAIL: VM creation failed"
        echo "$output" | tail -20
        return 1
    fi
}

# Test: Verify VM exists
test_verify_vm_exists() {
    log_info "TEST: Verifying VM ${VM_NAME} exists..."

    if openctl proxmox get vms "${VM_NAME}" -o yaml > /dev/null 2>&1; then
        log_info "PASS: VM ${VM_NAME} exists"
        openctl proxmox get vms "${VM_NAME}"
        return 0
    else
        log_error "FAIL: VM ${VM_NAME} not found"
        return 1
    fi
}

# Test: List VMs includes our VM
test_list_vms() {
    log_info "TEST: Listing VMs to verify ${VM_NAME} is included..."

    if openctl proxmox get vms | grep -q "${VM_NAME}"; then
        log_info "PASS: VM ${VM_NAME} found in VM list"
        return 0
    else
        log_error "FAIL: VM ${VM_NAME} not found in VM list"
        openctl proxmox get vms
        return 1
    fi
}

# Check prerequisites
check_prerequisites() {
    log_info "Checking prerequisites..."

    # Check if openctl is available
    if ! command -v openctl &> /dev/null; then
        log_error "openctl command not found"
        return 1
    fi

    # Check if plugin is available
    if ! openctl plugin list 2>/dev/null | grep -q proxmox; then
        log_error "proxmox plugin not found"
        return 1
    fi

    log_info "Prerequisites OK"
    return 0
}

# Main test execution
main() {
    log_info "=========================================="
    log_info "E2E Test: Create VM from Disk Image"
    log_info "VM Name: ${VM_NAME}"
    log_info "=========================================="
    log_warn ""
    log_warn "NOTE: This test requires the cloud image to be"
    log_warn "properly set up on the Proxmox storage."
    log_warn "See the comments at the top of this script."
    log_warn ""

    local failed=0

    # Check prerequisites
    if ! check_prerequisites; then
        log_error "Prerequisites check failed"
        return 1
    fi

    # Generate manifest
    generate_manifest

    # Run tests
    if ! test_create_vm; then
        failed=1
    fi

    if [[ $failed -eq 0 ]]; then
        # Only run verification tests if create succeeded
        sleep 3  # Give Proxmox a moment to register the VM

        if ! test_verify_vm_exists; then
            failed=1
        fi

        if ! test_list_vms; then
            failed=1
        fi
    fi

    # Summary
    log_info "=========================================="
    if [[ $failed -eq 0 ]]; then
        log_info "ALL TESTS PASSED"
    else
        log_error "SOME TESTS FAILED"
        log_warn ""
        log_warn "Common issues:"
        log_warn "1. Storage 'local' needs 'Import' content type enabled"
        log_warn "2. Network access required to download cloud image"
        log_warn "3. Sufficient storage space for image and VM disk"
    fi
    log_info "=========================================="

    return $failed
}

main "$@"
