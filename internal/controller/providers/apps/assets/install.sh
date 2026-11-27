#!/usr/bin/env bash
# Run ONLY inside the dedicated Ubuntu 24.04 GPU guest, never on the dev host.
# Stage compose.yaml beside this script and install the NAS credentials first.
# Installs/configures only: Jellyfin remains disabled until explicitly started.
# Vendor installation references:
# https://docs.docker.com/engine/install/ubuntu/
# https://documentation.ubuntu.com/server/how-to/graphics/install-nvidia-drivers/
# https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html
# https://jellyfin.org/docs/general/post-install/transcoding/hardware-acceleration/nvidia/
set -euo pipefail
umask 022

fail() { printf 'Error: %s\n' "$*" >&2; exit 1; }
[[ $EUID -eq 0 ]] || fail 'Run as root inside the Jellyfin guest.'
. /etc/os-release
[[ $ID == ubuntu && $VERSION_ID == 24.04 ]] || fail 'Ubuntu 24.04 is required.'
[[ $(dpkg --print-architecture) == amd64 ]] || fail 'The RTX 2070 guest must be amd64.'

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
[[ -f $script_dir/compose.yaml ]] || fail 'Stage compose.yaml beside install.sh.'
NAS_SERVER=${NAS_SERVER:-synology-space}
NAS_SHARE=${NAS_SHARE:-space}
[[ $NAS_SERVER =~ ^[a-zA-Z0-9][a-zA-Z0-9._-]*$ ]] || fail 'NAS_SERVER must be a hostname or IPv4 address.'
[[ $NAS_SHARE =~ ^[a-zA-Z0-9][a-zA-Z0-9._-]*$ ]] || fail 'NAS_SHARE must contain only letters, numbers, dots, underscores or hyphens.'
credentials=/etc/samba/synology.credentials
[[ -f $credentials && ! -L $credentials ]] || fail 'Securely install /etc/samba/synology.credentials before running this script.'
chown root:root "$credentials"
chmod 0600 "$credentials"

# Do not compete with an existing fstab entry or unsupported Docker packages.
if awk '$1 !~ /^#/ && $2 == "/mnt/synology" { found=1 } END { exit !found }' /etc/fstab; then
    fail 'Remove the existing /mnt/synology fstab entry before using these systemd mount units.'
fi
for package in docker.io docker-compose docker-compose-v2 docker-doc docker-buildx podman-docker containerd runc; do
    if [[ $(dpkg-query -W -f='${Status}' "$package" 2>/dev/null || true) == 'install ok installed' ]]; then
        fail "Remove conflicting package $package before installing official Docker packages."
    fi
done

# Reruns stop the managed server and keep it disabled; the operator starts it later.
if [[ -f /etc/systemd/system/jellyfin.service ]]; then
    systemctl disable --now jellyfin.service
fi
if [[ -f /lib/systemd/system/docker.service || -f /usr/lib/systemd/system/docker.service ]]; then
    systemctl stop docker.service docker.socket
fi
if [[ -f /etc/systemd/system/mnt-synology.automount ]]; then
    systemctl stop mnt-synology.mount mnt-synology.automount
fi

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y ca-certificates curl gnupg cifs-utils pciutils ubuntu-drivers-common linux-headers-generic python3 util-linux
lspci -Dn -d 10de: | grep -Eq ' (0300|0302): ' || fail 'No NVIDIA display/compute GPU is passed through to this guest.'
# Ubuntu selects its current hardware-compatible branch, including Turing support.
# Use the complete driver metapackage so NVENC/NVDEC libraries are installed too.
ubuntu-drivers install
command -v nvidia-smi >/dev/null || fail 'Ubuntu did not install NVIDIA driver utilities.'

install -d -m 0755 /mnt/synology /opt/jellyfin /etc/apt/keyrings /etc/systemd/system/docker.service.d
install -d -o 1000 -g 1000 -m 0750 /srv/jellyfin/config /srv/jellyfin/cache
if [[ $(readlink -f "$script_dir/compose.yaml") != /opt/jellyfin/compose.yaml ]]; then
    install -o root -g root -m 0644 "$script_dir/compose.yaml" /opt/jellyfin/compose.yaml
fi

# Native units avoid rewriting fstab. No idle timeout: Docker bind mounts must not
# outlive an idle-unmounted NAS. Read-only at both SMB mount and container layers.
cat > /etc/systemd/system/mnt-synology.mount <<EOF
[Unit]
Description=Read-only Synology media share

[Mount]
What=//$NAS_SERVER/$NAS_SHARE
Where=/mnt/synology
Type=cifs
Options=ro,credentials=$credentials,vers=3.1.1,uid=1000,gid=1000,forceuid,forcegid,file_mode=0440,dir_mode=0550,nosuid,nodev,noexec,_netdev
TimeoutSec=60
EOF
cat > /etc/systemd/system/mnt-synology.automount <<'EOF'
[Unit]
Description=Automount Synology media share

[Automount]
Where=/mnt/synology
TimeoutIdleSec=0

[Install]
WantedBy=multi-user.target
EOF
# Dedicated VM: every Docker daemon start, including automatic restore after a
# reboot, must wait for the real SMB mount, not merely the autofs mount point.
cat > /etc/systemd/system/docker.service.d/jellyfin-media.conf <<'EOF'
[Unit]
RequiresMountsFor=/mnt/synology

[Service]
ExecStartPre=/usr/bin/findmnt --noheadings --target /mnt/synology --types cifs --options ro
EOF
cat > /etc/systemd/system/jellyfin.service <<'EOF'
[Unit]
Description=Jellyfin container
Requires=docker.service
After=docker.service network-online.target
Wants=network-online.target
RequiresMountsFor=/mnt/synology
PartOf=docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=/opt/jellyfin
ExecStartPre=/usr/bin/nvidia-smi
ExecStartPre=/usr/bin/findmnt --noheadings --target /mnt/synology --types cifs --options ro
ExecStart=/usr/bin/docker compose --env-file /opt/jellyfin/.env -f /opt/jellyfin/compose.yaml up -d --pull never
ExecStop=/usr/bin/docker compose --env-file /opt/jellyfin/.env -f /opt/jellyfin/compose.yaml down
TimeoutStartSec=120
TimeoutStopSec=120

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now mnt-synology.automount

# Docker's official repository and Compose plugin (not Ubuntu's legacy packages).
curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
chmod 0644 /etc/apt/keyrings/docker.asc
cat > /etc/apt/sources.list.d/docker.sources <<'EOF'
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: noble
Components: stable
Architectures: amd64
Signed-By: /etc/apt/keyrings/docker.asc
EOF
workdir=$(mktemp -d)
trap 'rm -rf -- "$workdir"' EXIT
curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey -o "$workdir/nvidia.asc"
gpg --batch --yes --dearmor -o /etc/apt/keyrings/nvidia-container-toolkit.gpg "$workdir/nvidia.asc"
chmod 0644 /etc/apt/keyrings/nvidia-container-toolkit.gpg
cat > /etc/apt/sources.list.d/nvidia-container-toolkit.list <<'EOF'
deb [signed-by=/etc/apt/keyrings/nvidia-container-toolkit.gpg] https://nvidia.github.io/libnvidia-container/stable/deb/$(ARCH) /
EOF
apt-get update
apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin nvidia-container-toolkit
nvidia-ctk runtime configure --runtime=docker
systemctl daemon-reload
systemctl enable docker.service
systemctl restart docker.service

# The controller stages an explicit official digest before bootstrap. Never
# resolve a mutable tag or source dotenv data as executable shell code.
[[ -f /opt/jellyfin/.env ]] || fail 'The controller must provide an explicit pinned Jellyfin image.'
image_setting=$(grep '^JELLYFIN_IMAGE=' /opt/jellyfin/.env)
[[ $image_setting =~ ^JELLYFIN_IMAGE=(docker\.io/)?jellyfin/jellyfin@sha256:[a-f0-9]{64}$ ]] || fail '/opt/jellyfin/.env must contain exactly one pinned JELLYFIN_IMAGE=<official image>@sha256:<digest>.'
docker pull "${image_setting#JELLYFIN_IMAGE=}"
chown root:root /opt/jellyfin/.env
chmod 0600 /opt/jellyfin/.env

printf '%s\n' \
    'Installation complete; Jellyfin has NOT been started or enabled.' \
    'Reboot the guest to load the NVIDIA driver, then verify nvidia-smi.' \
    'Protect initial onboarding from LAN access before starting the host-networked server.' \
    'When ready: sudo systemctl enable --now jellyfin.service'
