#!/usr/bin/env bash
# Run on the Proxmox host. The selected NVIDIA card's physical console goes offline.
set -euo pipefail
[[ $EUID == 0 ]] || { echo 'Run as root on Proxmox.' >&2; exit 1; }
slot=${1:?Usage: prepare-gpu.sh DOMAIN:BUS:SLOT}
[[ $slot =~ ^[[:xdigit:]]{4}:[[:xdigit:]]{2}:[[:xdigit:]]{2}$ ]] || exit 1
shopt -s nullglob
devices=(/sys/bus/pci/devices/"$slot".*)
((${#devices[@]})) || { echo 'PCI slot not found.' >&2; exit 1; }
for device in "${devices[@]}"; do
    [[ -d $device/iommu_group ]] || { echo 'IOMMU is not enabled.' >&2; exit 1; }
    for member in "$device"/iommu_group/devices/*; do
        [[ ${member##*/} == "$slot".* || $(<"$member/class") == 0x0604* ]] || {
            echo "Unrelated device in IOMMU group: ${member##*/}" >&2; exit 1;
        }
    done
    for peripheral in "$device"/usb*/*; do
        [[ ! -f $peripheral/idVendor ]] || {
            echo "GPU USB port is in use: $peripheral" >&2; exit 1;
        }
    done
done
drm_devices=(/dev/dri/card* /dev/dri/renderD*)
if ((${#drm_devices[@]})) && fuser -s "${drm_devices[@]}"; then
    echo 'A process is using a DRM device; stop it before GPU handoff.' >&2
    exit 1
fi
ids=()
for device in "${devices[@]}"; do
    vendor=$(<"$device/vendor")
    product=$(<"$device/device")
    ids+=("${vendor#0x}:${product#0x}")
done
id_list=$(IFS=,; echo "${ids[*]}")
install -d /etc/modprobe.d /etc/modules-load.d /etc/initramfs-tools/hooks
printf 'options vfio-pci ids=%s\nblacklist nouveau\n' "$id_list" > /etc/modprobe.d/jellyfin-vfio.conf
printf 'vfio_pci\n' > /etc/modules-load.d/jellyfin-vfio.conf
cat > /etc/initramfs-tools/hooks/jellyfin-vfio <<'HOOK'
#!/bin/sh
case "${1:-}" in prereqs) exit 0 ;; esac
. /usr/share/initramfs-tools/hook-functions
force_load vfio_pci
HOOK
chmod 755 /etc/initramfs-tools/hooks/jellyfin-vfio
update-initramfs -u
modprobe vfio-pci
for device in "${devices[@]}"; do
    printf 'vfio-pci\n' > "$device/driver_override"
done
for console in /sys/class/vtconsole/vtcon*; do
    if [[ $(<"$console/name") == *'frame buffer'* && $(<"$console/bind") == 1 ]]; then
        printf '0\n' > "$console/bind"
    fi
done
# Detach auxiliary functions first, leaving display teardown until last.
for ((i=${#devices[@]}-1; i>=0; i--)); do
    device=${devices[i]}
    bdf=${device##*/}
    if [[ -L $device/driver && $(basename "$(readlink "$device/driver")") != vfio-pci ]]; then
        printf '%s\n' "$bdf" > "$device/driver/unbind"
    fi
    if [[ ! -L $device/driver ]]; then
        printf '%s\n' "$bdf" > /sys/bus/pci/drivers_probe
    fi
    [[ $(basename "$(readlink "$device/driver")") == vfio-pci ]] || exit 1
    printf '%s: vfio-pci\n' "$bdf"
done
