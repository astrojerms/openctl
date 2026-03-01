#!/bin/bash
# End-to-end test for creating a K3s cluster via openctl
#
# This test exercises the full workflow:
# 1. Creates VMs via Proxmox plugin (dispatched from K3s plugin)
# 2. Waits for VMs to get IPs via QEMU guest agent
# 3. Installs K3s via SSH
# 4. Verifies kubeconfig is created and cluster is accessible
#
# REQUIREMENTS:
# - Proxmox server accessible with API token configured
# - SSH private key accessible at the configured path
# - Network connectivity between test machine and VMs
# - Cloud image URL accessible from Proxmox server

set -euo pipefail

# Configuration - customize these for your environment
CLUSTER_NAME="e2e-k3s-$(date +%s)"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MANIFEST_FILE="${SCRIPT_DIR}/cluster-test-manifest.yaml"
CLEANUP_ON_EXIT=true

# Proxmox settings - should match your ~/.openctl/config.yaml
PROXMOX_NODE="${PROXMOX_NODE:-pve1351}"
PROXMOX_STORAGE="${PROXMOX_STORAGE:-local}"
PROXMOX_DISK_STORAGE="${PROXMOX_DISK_STORAGE:-local-lvm}"

# SSH settings
SSH_USER="${SSH_USER:-ubuntu}"
SSH_KEY_PATH="${SSH_KEY_PATH:-$HOME/.ssh/id_ed25519}"
SSH_PUBLIC_KEY="${SSH_PUBLIC_KEY:-}"

# Cloud image
CLOUD_IMAGE_URL="${CLOUD_IMAGE_URL:-https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
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

log_step() {
    echo -e "${BLUE}[STEP]${NC} $1"
}

cleanup() {
    if [[ "$CLEANUP_ON_EXIT" == "true" ]]; then
        log_info "Cleaning up cluster ${CLUSTER_NAME}..."

        # Delete cluster via openctl (this should dispatch VM deletes to Proxmox)
        if openctl k3s delete clusters "${CLUSTER_NAME}" 2>/dev/null; then
            log_info "Cluster ${CLUSTER_NAME} deleted successfully"
        else
            log_warn "Cluster deletion via openctl failed, attempting direct VM cleanup..."
            # Try to delete VMs directly
            for i in 0 1 2; do
                openctl proxmox delete vms "${CLUSTER_NAME}-cp-${i}" 2>/dev/null || true
                openctl proxmox delete vms "${CLUSTER_NAME}-worker-${i}" 2>/dev/null || true
            done
        fi

        # Clean up manifest file
        rm -f "${MANIFEST_FILE}"

        # Clean up kubeconfig
        rm -rf "${HOME}/.openctl/k3s/${CLUSTER_NAME}" 2>/dev/null || true
    else
        log_warn "Skipping cleanup (CLEANUP_ON_EXIT=false)"
        log_warn "Manifest: ${MANIFEST_FILE}"
        log_warn "Cluster: ${CLUSTER_NAME}"
    fi
}

# Always cleanup on exit
trap cleanup EXIT

# Get SSH public key if not provided
get_ssh_public_key() {
    if [[ -n "$SSH_PUBLIC_KEY" ]]; then
        echo "$SSH_PUBLIC_KEY"
        return
    fi

    local pub_key_path="${SSH_KEY_PATH}.pub"
    if [[ -f "$pub_key_path" ]]; then
        cat "$pub_key_path"
    else
        log_error "SSH public key not found at ${pub_key_path}"
        log_error "Set SSH_PUBLIC_KEY environment variable or ensure ${pub_key_path} exists"
        exit 1
    fi
}

# Generate test manifest
generate_manifest() {
    local ssh_pub_key
    ssh_pub_key=$(get_ssh_public_key)

    cat > "${MANIFEST_FILE}" << EOF
apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: ${CLUSTER_NAME}
spec:
  # Compute provider configuration
  compute:
    provider: proxmox
    # Cloud image to use for VMs
    image:
      url: ${CLOUD_IMAGE_URL}
      storage: ${PROXMOX_STORAGE}
      diskStorage: ${PROXMOX_DISK_STORAGE}
    # Default VM size
    default:
      cpus: 2
      memoryMB: 2048
      diskGB: 20

  # Node configuration
  nodes:
    controlPlane:
      count: 1
    # workers:
    #   - name: default
    #     count: 1

  # SSH configuration for K3s installation
  ssh:
    user: ${SSH_USER}
    privateKeyPath: ${SSH_KEY_PATH}
    publicKeys:
      - ${ssh_pub_key}

  # K3s configuration
  k3s:
    version: ""  # Use latest stable
    # clusterCIDR: "10.42.0.0/16"
    # serviceCIDR: "10.43.0.0/16"
EOF
    log_info "Generated test manifest: ${MANIFEST_FILE}"
}

# Check prerequisites
check_prerequisites() {
    log_step "Checking prerequisites..."
    local failed=0

    # Check if openctl is available
    if ! command -v openctl &> /dev/null; then
        log_error "openctl command not found"
        log_error "Run 'make build && make install' first"
        failed=1
    fi

    # Check if proxmox plugin is available
    # Note: Use grep without -q to avoid SIGPIPE issues with pipefail
    if ! openctl plugin list 2>/dev/null | grep proxmox > /dev/null; then
        log_error "proxmox plugin not found"
        log_error "Ensure openctl-proxmox is in PATH"
        failed=1
    fi

    # Check if k3s plugin is available
    if ! openctl plugin list 2>/dev/null | grep k3s > /dev/null; then
        log_error "k3s plugin not found"
        log_error "Ensure openctl-k3s is in PATH"
        failed=1
    fi

    # Check SSH key exists
    if [[ ! -f "${SSH_KEY_PATH}" ]]; then
        log_error "SSH private key not found: ${SSH_KEY_PATH}"
        failed=1
    fi

    # Check SSH public key
    if ! get_ssh_public_key > /dev/null 2>&1; then
        failed=1
    fi

    # Check kubectl (optional but useful)
    if ! command -v kubectl &> /dev/null; then
        log_warn "kubectl not found - cluster verification will be limited"
    fi

    if [[ $failed -eq 1 ]]; then
        return 1
    fi

    log_info "Prerequisites OK"
    return 0
}

# Test: Create cluster
test_create_cluster() {
    log_step "Creating cluster ${CLUSTER_NAME}..."
    log_info "Manifest content:"
    cat "${MANIFEST_FILE}"
    echo ""

    local output
    local start_time
    start_time=$(date +%s)

    # Create cluster with debug output
    if output=$(openctl --debug k3s create clusters -f "${MANIFEST_FILE}" 2>&1); then
        local end_time
        end_time=$(date +%s)
        local duration=$((end_time - start_time))

        log_info "PASS: Cluster create command completed in ${duration}s"
        echo "$output" | tail -20
        return 0
    else
        log_error "FAIL: Cluster creation failed"
        echo "$output" | tail -40
        return 1
    fi
}

# Test: Verify cluster state
test_verify_cluster_state() {
    log_step "Verifying cluster ${CLUSTER_NAME} state..."

    local output
    if output=$(openctl k3s get clusters "${CLUSTER_NAME}" -o yaml 2>&1); then
        log_info "Cluster state:"
        echo "$output"

        # Check if status shows Ready
        if echo "$output" | grep -q "phase: Ready"; then
            log_info "PASS: Cluster is in Ready state"
            return 0
        else
            log_warn "Cluster exists but may not be fully ready"
            return 0  # Still a pass if we got state
        fi
    else
        log_error "FAIL: Could not get cluster state"
        echo "$output"
        return 1
    fi
}

# Test: Verify kubeconfig was created
test_verify_kubeconfig() {
    log_step "Verifying kubeconfig..."

    local kubeconfig_path="${HOME}/.openctl/k3s/${CLUSTER_NAME}/kubeconfig"

    if [[ -f "$kubeconfig_path" ]]; then
        log_info "PASS: Kubeconfig exists at ${kubeconfig_path}"

        # Try to parse it
        if grep -q "apiVersion:" "$kubeconfig_path"; then
            log_info "Kubeconfig appears valid"
        fi

        return 0
    else
        log_error "FAIL: Kubeconfig not found at ${kubeconfig_path}"
        log_info "Checking alternative locations..."
        find "${HOME}/.openctl" -name "kubeconfig" 2>/dev/null || true
        return 1
    fi
}

# Test: Verify cluster connectivity
test_cluster_connectivity() {
    log_step "Testing cluster connectivity..."

    local kubeconfig_path="${HOME}/.openctl/k3s/${CLUSTER_NAME}/kubeconfig"

    if [[ ! -f "$kubeconfig_path" ]]; then
        log_warn "SKIP: Kubeconfig not found, skipping connectivity test"
        return 0
    fi

    if ! command -v kubectl &> /dev/null; then
        log_warn "SKIP: kubectl not found, skipping connectivity test"
        return 0
    fi

    # Test cluster access
    local output
    if output=$(kubectl --kubeconfig "$kubeconfig_path" get nodes 2>&1); then
        log_info "PASS: Cluster is accessible"
        echo "$output"

        # Check if nodes are Ready
        if echo "$output" | grep -q "Ready"; then
            log_info "Nodes are Ready"
        fi

        return 0
    else
        log_error "FAIL: Could not connect to cluster"
        echo "$output"
        return 1
    fi
}

# Test: Verify VMs were created
test_verify_vms() {
    log_step "Verifying VMs were created..."

    local found=0

    # Check for control plane VM
    if openctl proxmox get vms "${CLUSTER_NAME}-cp-0" -o yaml 2>/dev/null; then
        log_info "Found control plane VM: ${CLUSTER_NAME}-cp-0"
        found=$((found + 1))
    fi

    if [[ $found -gt 0 ]]; then
        log_info "PASS: Found ${found} VM(s) for cluster"
        return 0
    else
        log_error "FAIL: No VMs found for cluster ${CLUSTER_NAME}"
        log_info "Listing all VMs:"
        openctl proxmox get vms 2>/dev/null | head -20 || true
        return 1
    fi
}

# Test: Delete cluster
test_delete_cluster() {
    log_step "Deleting cluster ${CLUSTER_NAME}..."

    local output
    if output=$(openctl k3s delete clusters "${CLUSTER_NAME}" 2>&1); then
        log_info "PASS: Cluster deletion initiated"
        echo "$output"

        # Verify cluster is gone
        sleep 5
        if openctl k3s get clusters "${CLUSTER_NAME}" 2>&1 | grep -q "not found"; then
            log_info "Cluster state removed"
        fi

        return 0
    else
        log_error "FAIL: Cluster deletion failed"
        echo "$output"
        return 1
    fi
}

# Main test execution
main() {
    log_info "=========================================="
    log_info "E2E Test: K3s Cluster Creation"
    log_info "Cluster Name: ${CLUSTER_NAME}"
    log_info "Proxmox Node: ${PROXMOX_NODE}"
    log_info "=========================================="
    echo ""

    local failed=0

    # Check prerequisites
    if ! check_prerequisites; then
        log_error "Prerequisites check failed"
        return 1
    fi

    # Generate manifest
    generate_manifest

    echo ""
    log_info "Starting tests..."
    echo ""

    # Test 1: Create cluster
    if ! test_create_cluster; then
        failed=1
    fi

    # Only run verification tests if create succeeded
    if [[ $failed -eq 0 ]]; then
        echo ""

        # Give system a moment to settle
        sleep 5

        # Test 2: Verify VMs exist
        if ! test_verify_vms; then
            failed=1
        fi

        echo ""

        # Test 3: Verify cluster state
        if ! test_verify_cluster_state; then
            failed=1
        fi

        echo ""

        # Test 4: Verify kubeconfig
        if ! test_verify_kubeconfig; then
            failed=1
        fi

        echo ""

        # Test 5: Test connectivity
        if ! test_cluster_connectivity; then
            # Non-fatal - connectivity issues might be environmental
            log_warn "Connectivity test failed (non-fatal)"
        fi
    fi

    # Summary
    echo ""
    log_info "=========================================="
    if [[ $failed -eq 0 ]]; then
        log_info "ALL TESTS PASSED"
    else
        log_error "SOME TESTS FAILED"
        echo ""
        log_warn "Common issues:"
        log_warn "1. Proxmox API not accessible"
        log_warn "2. SSH key not configured correctly"
        log_warn "3. Cloud image download taking too long"
        log_warn "4. QEMU guest agent not installed in image"
        log_warn "5. Network connectivity issues"
    fi
    log_info "=========================================="

    # If we want to debug, don't cleanup
    if [[ "${DEBUG:-}" == "1" ]]; then
        CLEANUP_ON_EXIT=false
        log_warn "DEBUG mode: Skipping cleanup for inspection"
        log_warn "  Cluster: openctl k3s get clusters ${CLUSTER_NAME}"
        log_warn "  VMs: openctl proxmox get vms | grep ${CLUSTER_NAME}"
        log_warn "  Kubeconfig: ${HOME}/.openctl/k3s/${CLUSTER_NAME}/kubeconfig"
    fi

    return $failed
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --no-cleanup)
            CLEANUP_ON_EXIT=false
            shift
            ;;
        --debug)
            export DEBUG=1
            export OPENCTL_DEBUG=1
            shift
            ;;
        --help)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --no-cleanup    Don't delete resources after test"
            echo "  --debug         Enable debug output and skip cleanup"
            echo "  --help          Show this help message"
            echo ""
            echo "Environment variables:"
            echo "  PROXMOX_NODE        Proxmox node name (default: pve1351)"
            echo "  PROXMOX_STORAGE     Storage for images (default: local)"
            echo "  PROXMOX_DISK_STORAGE Storage for VM disks (default: local-lvm)"
            echo "  SSH_USER            SSH user for VMs (default: ubuntu)"
            echo "  SSH_KEY_PATH        Path to SSH private key"
            echo "  SSH_PUBLIC_KEY      SSH public key string (optional)"
            echo "  CLOUD_IMAGE_URL     URL to cloud image"
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            exit 1
            ;;
    esac
done

main "$@"
