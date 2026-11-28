#!/bin/bash
# First-boot bootstrap for a new dedicated Ubuntu Docker guest only.
set -euo pipefail

fail() { printf 'OpenCtl Docker guest bootstrap: %s\n' "$*" >&2; exit 1; }
[[ $EUID -eq 0 ]] || fail 'Run as root inside the dedicated guest.'
systemd-detect-virt --vm --quiet || fail 'Only a virtual-machine guest is supported; never run on the operator or Proxmox host.'
[[ -r /etc/os-release ]] || fail 'Ubuntu 24.04 amd64 is required.'
. /etc/os-release
[[ ${ID:-} == ubuntu && ${VERSION_ID:-} == 24.04 ]] || fail 'Ubuntu 24.04 is required.'
[[ $(dpkg --print-architecture) == amd64 && $(uname -m) == x86_64 ]] || fail 'Only amd64 guests are supported.'

# Fail before changing packages; never uninstall an existing container runtime.
installed() { [[ $(dpkg-query -W -f='${db:Status-Status}' "$1" 2>/dev/null || true) == installed ]]; }
for package in docker.io docker-doc docker-compose docker-compose-v2 podman-docker containerd runc; do
    if installed "$package"; then
        fail "Unsupported conflicting package $package is installed; use a clean dedicated guest."
    fi
done
if command -v docker >/dev/null 2>&1; then
    installed docker-ce && installed docker-ce-cli || fail 'An unsupported Docker installation exists; use a clean dedicated guest.'
fi

export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-remove --no-install-recommends ca-certificates curl
install -d -m 0755 /etc/apt/keyrings
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
apt-get update
apt-get install -y --no-remove --no-install-recommends docker-ce docker-ce-cli containerd.io docker-buildx-plugin

# This system-wide CLI-plugin directory takes precedence over packaged plugins.
# Download and verify before atomically publishing on the destination filesystem.
plugin_dir=/usr/local/lib/docker/cli-plugins
install -d -m 0755 "$plugin_dir"
plugin_tmp=$(mktemp "$plugin_dir/.docker-compose.XXXXXXXX")
trap 'rm -f -- "$plugin_tmp"' EXIT
curl -fsSL https://github.com/docker/compose/releases/download/v5.8.1/docker-compose-linux-x86_64 -o "$plugin_tmp"
printf '%s  %s\n' cf2713394ca63900da5d00e0be12045634cea93a504e1ab2f2ae24eed7f2d359 "$plugin_tmp" | sha256sum --check --status
chown root:root "$plugin_tmp"
chmod 0755 "$plugin_tmp"
mv -fT -- "$plugin_tmp" "$plugin_dir/docker-compose"
trap - EXIT

systemctl enable --now docker.service
