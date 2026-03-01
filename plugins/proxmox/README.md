# OpenCtl Proxmox Plugin

This plugin provides OpenCtl integration with Proxmox Virtual Environment (PVE), allowing you to manage VMs and templates using familiar kubectl-like commands.

## Installation

The plugin is built and installed as part of the main OpenCtl build:

```bash
# From the repository root
make build
make install
```

Or build manually:

```bash
cd plugins/proxmox
go build -o openctl-proxmox ./cmd/openctl-proxmox
cp openctl-proxmox ~/.openctl/plugins/
```

## Configuration

Configure the Proxmox provider in `~/.openctl/config.yaml`:

```yaml
providers:
  proxmox:
    default-context: homelab
    contexts:
      homelab:
        endpoint: https://pve.home.local:8006
        node: pve1
        credentials: homelab-creds
    credentials:
      homelab-creds:
        tokenId: root@pam!openctl
        tokenSecretFile: ~/.openctl/secrets/proxmox.token
```

### Creating an API Token

1. Log into the Proxmox web UI
2. Navigate to Datacenter > Permissions > API Tokens
3. Click "Add" and create a token for your user
4. Copy the token ID (e.g., `root@pam!openctl`) and secret
5. Store the secret in the file referenced by `tokenSecretFile`

```bash
mkdir -p ~/.openctl/secrets
echo "your-token-secret" > ~/.openctl/secrets/proxmox.token
chmod 600 ~/.openctl/secrets/proxmox.token
```

## Resources

### VirtualMachine

Represents a QEMU/KVM virtual machine in Proxmox.

**Actions:** `get`, `list`, `create`, `delete`, `apply`

#### Manifest Schema

```yaml
apiVersion: proxmox.openctl.io/v1
kind: VirtualMachine
metadata:
  name: <vm-name>              # Required: VM name
  labels:                       # Optional: key-value labels
    role: webserver
spec:
  node: <node-name>            # Target Proxmox node (or use config default)

  # VM Source - use ONE of: template, cloudImage, or image
  template:                     # Clone from an existing template
    name: <template-name>       # Template name (resolved to VMID)
    vmid: <vmid>                # Or specify template VMID directly

  cloudImage:                   # Create from cloud image URL (RECOMMENDED - fully automated)
    url: <url>                  # URL to download cloud image (e.g., https://cloud-images.ubuntu.com/...)
    storage: <storage-id>       # Storage for downloading image and creating template
    diskStorage: <storage-id>   # Optional: Storage for VM disk (defaults to storage)
    templateName: <name>        # Optional: Template name (auto-generated from URL if omitted)
    checksum: <checksum>        # Optional: Expected checksum (sha256:... or sha512:...)

  image:                        # Create from a disk image file (requires manual setup)
    storage: <storage-id>       # Storage where image is located (e.g., "local")
    file: <filename>            # Image filename (e.g., "ubuntu-22.04-cloudimg.img")
    contentType: <type>         # Storage content type: "images" (default), "iso", or "import"
    format: <format>            # Optional: qcow2, raw, vmdk (auto-detected if omitted)
    targetStorage: <storage>    # Optional: Storage for imported disk (defaults to storage)
    targetFormat: <format>      # Optional: Format for imported disk

  # Hardware Configuration
  cpu:
    cores: <number>             # Number of CPU cores
    sockets: <number>           # Number of CPU sockets (default: 1)
  memory:
    size: <mb>                  # Memory in MB

  # System Settings
  osType: <type>                # OS type: l26 (Linux 2.6+), win10, other
  bios: <type>                  # BIOS type: seabios (default), ovmf (UEFI)
  machine: <type>               # Machine type: i440fx (default), q35
  agent:
    enabled: <bool>             # Enable QEMU guest agent

  # Storage
  disks:
    - name: <disk-name>         # e.g., scsi0, virtio0
      storage: <storage-id>     # Proxmox storage ID
      size: <size>              # Size with unit: 10G, 100G, 1T

  # Networking
  networks:
    - name: <net-name>          # e.g., net0
      bridge: <bridge>          # Bridge name (e.g., vmbr0)
      model: <model>            # NIC model: virtio (default), e1000, rtl8139

  # Cloud-Init Configuration
  cloudInit:
    user: <username>            # Default user
    password: <password>        # Optional: user password
    sshKeys:                    # SSH public keys
      - ssh-ed25519 AAAA...
    ipConfig:
      net0:
        ip: dhcp                # Or: 192.168.1.100/24
        gateway: 192.168.1.1    # Required for static IP

  startOnCreate: <bool>         # Start VM after creation
```

### Template

Represents a VM template that can be cloned to create new VMs.

**Actions:** `get`, `list`

#### Output Schema

```yaml
apiVersion: proxmox.openctl.io/v1
kind: Template
metadata:
  name: <template-name>
spec:
  node: <node-name>
  vmid: <vmid>
status:
  state: <running|stopped>
```

## Usage Examples

### List VMs

```bash
# List all VMs
openctl proxmox get vms

# List with more details
openctl proxmox get vms -o wide

# Get a specific VM
openctl proxmox get vm my-vm

# Output as YAML
openctl proxmox get vm my-vm -o yaml

# Output as JSON
openctl proxmox get vms -o json
```

### List Templates

```bash
openctl proxmox get templates
openctl proxmox get template ubuntu-22.04
```

### Create VM from Cloud Image URL (Recommended)

This is the easiest way to create VMs - no manual Proxmox setup required! The plugin will:
1. Download the cloud image to Proxmox storage
2. Create a template with cloud-init support
3. Clone the template to create your VM
4. Apply your cloud-init settings

Create a manifest file `vm-from-cloudimage.yaml`:

```yaml
apiVersion: proxmox.openctl.io/v1
kind: VirtualMachine
metadata:
  name: web-01
spec:
  node: pve1
  cloudImage:
    # Ubuntu, Debian, Rocky, Fedora, etc. - any cloud image URL works
    url: https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img
    storage: local           # Storage for downloading (needs "Import" content type)
    diskStorage: local-lvm   # Storage for VM disks (can be different)
  cpu:
    cores: 2
  memory:
    size: 4096
  disks:
    - name: scsi0
      size: 32G              # Resize the disk as needed
  networks:
    - name: net0
      bridge: vmbr0
  cloudInit:
    user: ubuntu
    sshKeys:
      - ssh-ed25519 AAAA... user@host
    ipConfig:
      net0:
        ip: dhcp
  startOnCreate: true
```

```bash
openctl proxmox create vm -f vm-from-cloudimage.yaml
```

**Popular Cloud Image URLs:**

| Distribution | URL |
|-------------|-----|
| Ubuntu 22.04 | `https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img` |
| Ubuntu 24.04 | `https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img` |
| Debian 12 | `https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-generic-amd64.qcow2` |
| Rocky 9 | `https://download.rockylinux.org/pub/rocky/9/images/x86_64/Rocky-9-GenericCloud.latest.x86_64.qcow2` |
| Fedora 40 | `https://download.fedoraproject.org/pub/fedora/linux/releases/40/Cloud/x86_64/images/Fedora-Cloud-Base-Generic.x86_64-40-1.14.qcow2` |

**Note:** The template is cached - subsequent VMs using the same cloud image URL will reuse the existing template, making creation much faster.

### Create VM from Template

Create a manifest file `vm-from-template.yaml`:

```yaml
apiVersion: proxmox.openctl.io/v1
kind: VirtualMachine
metadata:
  name: web-01
  labels:
    role: webserver
spec:
  node: pve1
  template:
    name: ubuntu-22.04-cloudinit
  cpu:
    cores: 4
  memory:
    size: 8192
  disks:
    - name: scsi0
      storage: local-lvm
      size: 50G
  networks:
    - name: net0
      bridge: vmbr0
  cloudInit:
    user: ubuntu
    sshKeys:
      - ssh-ed25519 AAAA... user@host
    ipConfig:
      net0:
        ip: dhcp
  startOnCreate: true
```

```bash
openctl proxmox create vm -f vm-from-template.yaml
# or
openctl apply -f vm-from-template.yaml
```

### Create VM from Disk Image

This is useful for creating VMs from cloud images (e.g., Ubuntu Cloud Images, Debian Cloud, etc.).

**Step 1: Download and prepare the cloud image on your Proxmox server**

Cloud images must be placed in the storage's `images/` directory with Proxmox's naming convention:

```bash
# On the Proxmox host

# Download the cloud image
cd /tmp
wget https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img

# Create the images directory (VMID 0 is used as a template source)
mkdir -p /mnt/pve/your-storage/images/0

# Copy with Proxmox naming convention
cp jammy-server-cloudimg-amd64.img /mnt/pve/your-storage/images/0/vm-0-disk-0.raw
```

**Step 2: Create the manifest** `vm-from-image.yaml`:

```yaml
apiVersion: proxmox.openctl.io/v1
kind: VirtualMachine
metadata:
  name: cloud-vm-01
spec:
  node: pve1
  image:
    storage: local
    # Use full volume ID with Proxmox naming convention
    file: local:0/vm-0-disk-0.raw
    targetStorage: local-lvm    # Where to store the imported disk
  cpu:
    cores: 2
  memory:
    size: 4096
  osType: l26
  bios: seabios                 # Or ovmf for UEFI
  machine: q35
  agent:
    enabled: true
  disks:
    - name: scsi0
      size: 32G                 # Resize the disk after import
  networks:
    - name: net0
      bridge: vmbr0
  cloudInit:
    user: ubuntu
    sshKeys:
      - ssh-ed25519 AAAA... user@host
    ipConfig:
      net0:
        ip: 192.168.1.100/24
        gateway: 192.168.1.1
  startOnCreate: true
```

```bash
openctl proxmox create vms -f vm-from-image.yaml
```

### Update VM Configuration

```bash
# Modify your manifest and apply changes
openctl proxmox apply -f vm.yaml
```

### Delete VM

```bash
openctl proxmox delete vm web-01
```

## Configuration Reference

### spec.cloudImage Options

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `url` | string | Yes | URL to download the cloud image from |
| `storage` | string | Yes | Proxmox storage ID for downloading the image (must have "Import" content type enabled) |
| `diskStorage` | string | No | Storage for the VM disk. Defaults to `storage` |
| `templateName` | string | No | Name for the template VM. Auto-generated from URL if omitted |
| `checksum` | string | No | Expected checksum in format `sha256:abc123...` or `sha512:...` |

**Storage Setup:** The download storage needs the "Import" content type enabled in Proxmox:
1. Go to Datacenter > Storage > [your storage] > Edit
2. Under "Content", add "Disk image" and/or "Import"

### spec.image Options

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `storage` | string | Yes | Proxmox storage ID where the image file is located |
| `file` | string | Yes | Full volume ID in Proxmox format (see below) |
| `targetStorage` | string | No | Storage for the imported disk. Defaults to `storage` |

**Important:** The `file` field must use Proxmox's volume ID format for disk images:

```
<storage>:<vmid>/vm-<vmid>-disk-<n>.<format>
```

For example: `local:0/vm-0-disk-0.raw` or `synology-smb:0/vm-0-disk-0.qcow2`

**Setting up your cloud image for import:**

```bash
# On the Proxmox server
# 1. Create the images directory (using VMID 0 as template)
mkdir -p /mnt/pve/<storage>/images/0

# 2. Copy your cloud image with Proxmox naming
cp your-cloud-image.img /mnt/pve/<storage>/images/0/vm-0-disk-0.raw

# 3. Reference in manifest as: <storage>:0/vm-0-disk-0.raw
```

This naming convention is required because Proxmox's storage API expects disk images to follow a specific format for volume identification.

### spec.cpu Options

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `cores` | int | 1 | Number of CPU cores per socket |
| `sockets` | int | 1 | Number of CPU sockets |

### spec.memory Options

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `size` | int | Yes | Memory size in MB |

### spec.osType Values

| Value | Description |
|-------|-------------|
| `l26` | Linux 2.6+ kernel (recommended for modern Linux) |
| `l24` | Linux 2.4 kernel |
| `win11` | Windows 11 |
| `win10` | Windows 10/2016/2019 |
| `win8` | Windows 8/2012 |
| `win7` | Windows 7/2008r2 |
| `wxp` | Windows XP/2003 |
| `other` | Other OS type |

### spec.bios Values

| Value | Description |
|-------|-------------|
| `seabios` | Traditional BIOS (default) |
| `ovmf` | UEFI firmware (required for some cloud images) |

### spec.machine Values

| Value | Description |
|-------|-------------|
| `i440fx` | Standard PC (default, most compatible) |
| `q35` | Modern chipset (recommended for UEFI/NVMe) |

### spec.cloudInit.ipConfig Options

| Field | Type | Description |
|-------|------|-------------|
| `ip` | string | `dhcp` or static IP with CIDR (e.g., `192.168.1.100/24`) |
| `gateway` | string | Gateway IP (required for static IP) |

## Debugging

Enable debug logging by setting the environment variable:

```bash
OPENCTL_DEBUG=1 openctl proxmox get vms
```

This will output detailed API request/response information to stderr.

## Troubleshooting

### Cloud image download fails

1. Ensure the storage has "ISO image" content type enabled:
   - Datacenter > Storage > [storage] > Edit > Content > Add "ISO image"
   - Note: The plugin uses `content=iso` for downloads, as this is the correct content type for the Proxmox download-url API
2. Check that the Proxmox node can reach the image URL (firewall, DNS)
3. Verify sufficient storage space for the download

### "template not found"

Ensure the template exists and the name matches exactly:

```bash
openctl proxmox get templates
```

### "node is required"

Either specify `spec.node` in your manifest or configure a default node in your config:

```yaml
providers:
  proxmox:
    contexts:
      default:
        node: pve1  # Default node
```

### Image import fails

1. Verify the image exists in the specified storage:
   ```bash
   # On Proxmox host
   ls /var/lib/vz/template/iso/
   ```
2. Check that the storage has enough space
3. For ISO storage, images should be in the `iso/` subdirectory
4. Ensure the Proxmox API token has sufficient permissions

### Cloud-init not working

1. Ensure the VM template/image has cloud-init installed
2. Check that you're specifying SSH keys or password
3. Verify network configuration matches your infrastructure

## Compute Provider Interface

The Proxmox plugin implements the `compute.openctl.io/v1` interface, which allows other plugins (like K3s) to use it for VM provisioning:

```json
{
  "computeProvider": {
    "implements": "compute.openctl.io/v1",
    "features": ["cloudImage", "cloudInit", "sshKeys"]
  }
}
```

This enables the K3s plugin to delegate VM creation to Proxmox when deploying K3s clusters. See the [K3s plugin documentation](../k3s/README.md) for more details on cross-plugin orchestration.
