# OpenCtl K3s Plugin

This plugin provides OpenCtl integration for deploying and managing K3s Kubernetes clusters across compute providers. It demonstrates the cross-plugin dispatch feature, delegating VM creation to compute provider plugins (like Proxmox) and then installing K3s via SSH.

## Installation

The plugin is built and installed as part of the main OpenCtl build:

```bash
# From the repository root
make build
make install
```

Or build manually:

```bash
cd plugins/k3s
go build -o openctl-k3s ./cmd/openctl-k3s
cp openctl-k3s ~/.openctl/plugins/
```

## Configuration

Configure the K3s provider in `~/.openctl/config.yaml`:

```yaml
providers:
  k3s:
    default-context: default
    contexts:
      default: {}
```

The K3s plugin delegates VM creation to compute providers, so you also need to configure the compute provider (e.g., Proxmox):

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

## Resources

### Cluster

Represents a K3s Kubernetes cluster consisting of control plane and worker nodes.

**Actions:** `get`, `list`, `create`, `delete`

#### Manifest Schema

```yaml
apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: <cluster-name>           # Required: unique cluster name
spec:
  compute:
    provider: <provider-name>    # Required: compute provider (e.g., "proxmox")
    context: <context-name>      # Optional: provider context to use
    image:
      url: <cloud-image-url>     # Cloud image URL (recommended)
      # OR
      template: <template-name>  # Existing template name
    default:                     # Default VM sizing
      cpus: <number>             # CPU cores per VM (default: 2)
      memoryMB: <number>         # Memory in MB (default: 4096)
      diskGB: <number>           # Disk size in GB (default: 32)

  nodes:
    controlPlane:
      count: <number>            # Number of control plane nodes (default: 1)
      size:                      # Optional: override default sizing
        cpus: <number>
        memoryMB: <number>
        diskGB: <number>
    workers:                     # Optional: worker node pools
      - name: <pool-name>        # Pool name (used in VM names)
        count: <number>          # Number of workers in pool
        size:                    # Optional: override default sizing
          cpus: <number>
          memoryMB: <number>
          diskGB: <number>

  k3s:                           # Optional: K3s configuration
    version: <version>           # K3s version (e.g., "v1.29.0+k3s1")
    clusterCIDR: <cidr>          # Pod CIDR (default: 10.42.0.0/16)
    serviceCIDR: <cidr>          # Service CIDR (default: 10.43.0.0/16)
    extraArgs:                   # Extra K3s server arguments
      - --disable=traefik

  ssh:
    user: <username>             # SSH user (default: "ubuntu")
    privateKeyPath: <path>       # Required: path to SSH private key
    publicKeys:                  # Public keys for cloud-init
      - ssh-ed25519 AAAA...
```

## Usage Examples

### Create a Simple Cluster

Create a manifest file `cluster.yaml`:

```yaml
apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: dev-cluster
spec:
  compute:
    provider: proxmox
    image:
      url: https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img
    default:
      cpus: 2
      memoryMB: 4096
      diskGB: 32
  nodes:
    controlPlane:
      count: 1
  ssh:
    user: ubuntu
    privateKeyPath: ~/.ssh/id_ed25519
    publicKeys:
      - ssh-ed25519 AAAA... user@host
```

```bash
openctl k3s create cluster -f cluster.yaml
```

### Create an HA Cluster with Workers

```yaml
apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: prod-cluster
spec:
  compute:
    provider: proxmox
    image:
      url: https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img
    default:
      cpus: 4
      memoryMB: 8192
      diskGB: 50
  nodes:
    controlPlane:
      count: 3              # HA control plane
    workers:
      - name: general
        count: 3
        size:
          cpus: 4
          memoryMB: 16384
          diskGB: 100
      - name: gpu
        count: 2
        size:
          cpus: 8
          memoryMB: 32768
          diskGB: 200
  k3s:
    version: v1.29.0+k3s1
    extraArgs:
      - --disable=traefik
      - --disable=servicelb
  ssh:
    user: ubuntu
    privateKeyPath: ~/.ssh/id_ed25519
    publicKeys:
      - ssh-ed25519 AAAA... user@host
```

### List Clusters

```bash
# List all clusters
openctl k3s get clusters

# Get a specific cluster
openctl k3s get cluster dev-cluster

# Output as YAML
openctl k3s get cluster dev-cluster -o yaml
```

### Delete a Cluster

```bash
openctl k3s delete cluster dev-cluster
```

This will:
1. Delete all VMs associated with the cluster
2. Remove local state and kubeconfig files

## How It Works

The K3s plugin uses OpenCtl's **dispatch system** to orchestrate multi-step operations:

### Cluster Creation Flow

1. **Parse Manifest**: Validate the cluster specification
2. **Dispatch VM Creation**: Send `create` requests to the compute provider (e.g., Proxmox) for each node
3. **Wait for VMs**: The CLI handles dispatch execution and returns results
4. **Get VM IPs**: Query the compute provider for each VM's IP address
5. **Install K3s**: SSH into each node and install K3s
   - First control plane: Initialize cluster
   - Additional control planes: Join as server nodes
   - Workers: Join as agent nodes
6. **Save Kubeconfig**: Download kubeconfig to `~/.openctl/k3s/<cluster-name>/kubeconfig`

### Cluster Deletion Flow

1. **Load State**: Read cluster state to find associated VMs
2. **Dispatch VM Deletion**: Send `delete` requests for each VM
3. **Cleanup**: Remove local state files and kubeconfig

### State Management

Cluster state is stored in `~/.openctl/state/k3s/<cluster-name>.yaml`:

```yaml
apiVersion: k3s.openctl.io/v1
kind: Cluster
spec:
  compute:
    provider: proxmox
    # ... full spec
status:
  phase: Ready              # Creating, Ready, Failed, Deleting
  message: Cluster is ready
  outputs:
    kubeconfigPath: /home/user/.openctl/k3s/dev-cluster/kubeconfig
    serverIP: 192.168.1.100
children:
  - provider: proxmox
    kind: VirtualMachine
    name: dev-cluster-cp-0
  - provider: proxmox
    kind: VirtualMachine
    name: dev-cluster-worker-0
```

## Node Naming Convention

VMs are named based on the cluster name:

- Control plane nodes: `<cluster>-cp-<index>` (e.g., `dev-cluster-cp-0`)
- Worker nodes: `<cluster>-<pool>-<index>` (e.g., `dev-cluster-general-0`)

## Configuration Reference

### spec.compute Options

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `provider` | string | Yes | Compute provider name (e.g., "proxmox") |
| `context` | string | No | Provider context to use |
| `image.url` | string | One of | Cloud image URL to download |
| `image.template` | string | One of | Existing template name |
| `default.cpus` | int | No | Default CPU cores (default: 2) |
| `default.memoryMB` | int | No | Default memory in MB (default: 4096) |
| `default.diskGB` | int | No | Default disk in GB (default: 32) |

### spec.nodes Options

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `controlPlane.count` | int | No | Control plane nodes (default: 1) |
| `controlPlane.size` | object | No | Override default sizing |
| `workers[].name` | string | Yes | Worker pool name |
| `workers[].count` | int | Yes | Workers in pool |
| `workers[].size` | object | No | Override default sizing |

### spec.k3s Options

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `version` | string | latest | K3s version to install |
| `clusterCIDR` | string | 10.42.0.0/16 | Pod network CIDR |
| `serviceCIDR` | string | 10.43.0.0/16 | Service network CIDR |
| `extraArgs` | []string | [] | Extra K3s server arguments |

### spec.ssh Options

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `user` | string | No | SSH username (default: "ubuntu") |
| `privateKeyPath` | string | Yes | Path to SSH private key |
| `publicKeys` | []string | No | Public keys for cloud-init |

## Using the Kubeconfig

After cluster creation, the kubeconfig is saved locally:

```bash
# Use the kubeconfig
export KUBECONFIG=~/.openctl/k3s/dev-cluster/kubeconfig
kubectl get nodes

# Or specify directly
kubectl --kubeconfig ~/.openctl/k3s/dev-cluster/kubeconfig get nodes
```

## Troubleshooting

### SSH connection fails

1. Ensure the SSH private key has correct permissions: `chmod 600 ~/.ssh/id_ed25519`
2. Verify the public key is in the `ssh.publicKeys` list
3. Check that the VM's security group/firewall allows SSH (port 22)
4. Wait for cloud-init to complete on new VMs

### K3s installation fails

1. Ensure VMs have internet access to download K3s
2. Check that the SSH user has sudo privileges
3. Verify adequate disk space and memory

### VMs not getting IPs

1. Check DHCP server configuration on the network
2. For static IPs, configure them in the cloud-init section of the compute provider
3. Ensure the QEMU guest agent is installed in the VM image

### Cluster state is inconsistent

If a creation fails partway through:

```bash
# Check current state
openctl k3s get cluster <name> -o yaml

# Delete and recreate
openctl k3s delete cluster <name>
openctl k3s create cluster -f cluster.yaml
```

## Limitations

- Currently only supports Proxmox as a compute provider
- Requires SSH access from the machine running OpenCtl to the VMs
- No automatic certificate rotation or cluster upgrades yet
- Single-region clusters only
