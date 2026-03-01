#!/bin/bash
# Setup script to prepare a cloud image for Proxmox import
#
# This script should be run ON THE PROXMOX SERVER to prepare the cloud image
# for importing via the openctl proxmox plugin.
#
# Usage: ./setup-test-image.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Source local config if exists
if [[ -f "${SCRIPT_DIR}/.env.e2e" ]]; then
    # shellcheck source=/dev/null
    source "${SCRIPT_DIR}/.env.e2e"
fi

# Configuration - adjust these via .env.e2e or environment variables
SOURCE_IMAGE="${SOURCE_IMAGE:-/var/lib/vz/template/iso/jammy-server-cloudimg-amd64.img}"
TARGET_STORAGE="${TARGET_STORAGE:-/var/lib/vz}"
VMID="${VMID:-0}"  # Use 0 as a placeholder VMID for import

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${GREEN}Setting up test image for Proxmox import...${NC}"

# Check if source image exists
if [[ ! -f "$SOURCE_IMAGE" ]]; then
    echo -e "${RED}Error: Source image not found: $SOURCE_IMAGE${NC}"
    echo "Please update SOURCE_IMAGE variable in this script."
    exit 1
fi

# Create target directory
TARGET_DIR="${TARGET_STORAGE}/images/${VMID}"
echo "Creating target directory: ${TARGET_DIR}"
mkdir -p "$TARGET_DIR"

# Detect image format and copy with correct naming
SOURCE_EXT="${SOURCE_IMAGE##*.}"
if [[ "$SOURCE_EXT" == "img" ]]; then
    # Assume raw format for .img files
    TARGET_FILE="${TARGET_DIR}/vm-${VMID}-disk-0.raw"
elif [[ "$SOURCE_EXT" == "qcow2" ]]; then
    TARGET_FILE="${TARGET_DIR}/vm-${VMID}-disk-0.qcow2"
else
    # Default to raw
    TARGET_FILE="${TARGET_DIR}/vm-${VMID}-disk-0.raw"
fi

echo "Copying image to: ${TARGET_FILE}"
cp "$SOURCE_IMAGE" "$TARGET_FILE"

# Set permissions
chmod 644 "$TARGET_FILE"

echo ""
echo -e "${GREEN}Setup complete!${NC}"
echo ""
echo "The image is now available at:"
echo "  Volume ID: synology-smb:${VMID}/vm-${VMID}-disk-0.${TARGET_FILE##*.}"
echo "  Path: ${TARGET_FILE}"
echo ""
echo "Use this in your VM manifest:"
echo "  image:"
echo "    storage: synology-smb"
echo "    file: synology-smb:${VMID}/vm-${VMID}-disk-0.${TARGET_FILE##*.}"
echo "    targetStorage: local-lvm"
