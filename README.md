# OpenCtl

OpenCtl is a CLI tool that provides a unified interface for managing infrastructure resources across different providers. It combines the familiar kubectl/eksctl user experience with a Terraform-like exec-based plugin system.

## Features

- **Kubectl-like UX**: Familiar commands like `get`, `create`, `delete`, and `apply`
- **Plugin Architecture**: Extend functionality through exec-based plugins
- **Multi-provider Support**: Manage resources across different infrastructure providers
- **Declarative Manifests**: Define resources using YAML manifests (Kubernetes-style)
- **Multiple Output Formats**: Table, YAML, JSON, and wide output formats
- **Context-based Configuration**: Switch between different environments easily
- **Cross-plugin Dispatch**: Plugins can delegate to other plugins for complex orchestration

## Included Plugins

| Plugin | Description |
|--------|-------------|
| [Proxmox](plugins/proxmox/README.md) | Manage VMs and templates in Proxmox VE |
| [K3s](plugins/k3s/README.md) | Deploy K3s Kubernetes clusters across compute providers |

## Installation

### From Source

```bash
# Clone the repository
git clone https://github.com/openctl/openctl.git
cd openctl

# Build CLI and all plugins
make build

# Install (copies CLI to PATH and plugins to ~/.openctl/plugins/)
make install
```

### Manual Installation

```bash
# Build CLI
go build -o openctl ./cmd/openctl

# Build plugins
cd plugins/proxmox && go build -o openctl-proxmox ./cmd/openctl-proxmox
cd plugins/k3s && go build -o openctl-k3s ./cmd/openctl-k3s

# Install
cp openctl /usr/local/bin/
mkdir -p ~/.openctl/plugins
cp plugins/*/openctl-* ~/.openctl/plugins/
```

## Quick Start

### 1. Configure OpenCtl

Create a configuration file at `~/.openctl/config.yaml`:

```yaml
defaults:
  output: table
  timeout: 300

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

Store your API token securely:

```bash
mkdir -p ~/.openctl/secrets
echo "your-api-token-secret" > ~/.openctl/secrets/proxmox.token
chmod 600 ~/.openctl/secrets/proxmox.token
```

### 2. Verify Plugin Installation

```bash
openctl plugin list
```

Output:
```
NAME      PATH                                        RESOURCES
proxmox   /home/user/.openctl/plugins/openctl-proxmox [vms templates]
k3s       /home/user/.openctl/plugins/openctl-k3s     [clusters]
```

### 3. List Resources

```bash
# List all VMs
openctl proxmox get vms

# List with wide output (more columns)
openctl proxmox get vms -o wide

# Get a specific VM
openctl proxmox get vm web-01

# Output as YAML or JSON
openctl proxmox get vm web-01 -o yaml
openctl proxmox get vms -o json
```

### 4. Create Resources

Create a VM manifest file `vm.yaml`:

```yaml
apiVersion: proxmox.openctl.io/v1
kind: VirtualMachine
metadata:
  name: web-01
spec:
  node: pve1
  cloudImage:
    url: https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img
    storage: local
    diskStorage: local-lvm
  cpu:
    cores: 4
  memory:
    size: 8192
  disks:
    - name: scsi0
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

Create the VM:

```bash
openctl proxmox create vm -f vm.yaml
# or
openctl apply -f vm.yaml  # Auto-detects provider from apiVersion
```

### 5. Create a K3s Cluster

Create a cluster manifest `cluster.yaml`:

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
      storage: local
      diskStorage: local-lvm
    default:
      cpus: 2
      memoryMB: 4096
      diskGB: 32
  nodes:
    controlPlane:
      count: 1
  network:
    dhcp: false
    staticIPs:
      startIP: "192.168.1.100"
      gateway: "192.168.1.1"
      netmask: "24"
  ssh:
    user: ubuntu
    privateKeyPath: ~/.ssh/id_ed25519
    publicKeys:
      - ssh-ed25519 AAAA... user@host
```

Create the cluster:

```bash
openctl k3s create cluster -f cluster.yaml
```

Use the cluster:

```bash
export KUBECONFIG=~/.openctl/k3s/dev-cluster/kubeconfig
kubectl get nodes
```

### 6. Delete Resources

```bash
openctl proxmox delete vm web-01
openctl k3s delete cluster dev-cluster
```

## Command Reference

### Global Flags

| Flag | Description |
|------|-------------|
| `--config` | Path to config file (default: `~/.openctl/config.yaml`) |
| `--context` | Context to use for the operation |
| `-o, --output` | Output format: `table`, `yaml`, `json`, `wide` |
| `--timeout` | Timeout in seconds (default: 300) |

### Commands

| Command | Description |
|---------|-------------|
| `openctl <provider> get <resource> [name]` | Get one or more resources |
| `openctl <provider> create <resource> -f <file>` | Create a resource from manifest |
| `openctl <provider> delete <resource> <name>` | Delete a resource |
| `openctl <provider> apply -f <file>` | Create or update a resource |
| `openctl apply -f <file>` | Apply with auto-detected provider |
| `openctl plugin list` | List installed plugins |
| `openctl config view` | Display current configuration |
| `openctl version` | Print version information |

## Configuration

### Config File Structure

```yaml
defaults:
  output: table          # Default output format
  timeout: 300           # Default timeout in seconds

providers:
  <provider-name>:
    default-context: <context-name>
    contexts:
      <context-name>:
        endpoint: <url>
        node: <node-name>
        credentials: <credentials-name>
    credentials:
      <credentials-name>:
        tokenId: <token-id>
        tokenSecret: <secret>        # Inline (not recommended)
        tokenSecretFile: <path>      # File path (recommended)
    defaults:
      <key>: <value>
```

### Multiple Contexts

Switch between environments using contexts:

```bash
# Use a specific context
openctl proxmox get vms --context work

# Default context from config
providers:
  proxmox:
    default-context: work
```

## Resource Manifests

Resources follow a Kubernetes-style format:

```yaml
apiVersion: <provider>.openctl.io/v1
kind: <ResourceKind>
metadata:
  name: <resource-name>
  labels:
    key: value
spec:
  # Resource-specific configuration
status:
  # Read-only status (populated by provider)
```

The `apiVersion` enables automatic provider detection with `openctl apply -f`.

## Development

### Building

```bash
make build          # Build CLI and all plugins
make build-cli      # Build CLI only
make build-plugins  # Build all plugins
```

### Testing

```bash
make test           # Run all unit tests
make test-e2e       # Run E2E tests
make lint           # Run linters
make fmt            # Format code
```

### Code Quality

```bash
make modernize       # Apply Go modernizer fixes
make modernize-check # Check for modernize suggestions
```

See [DESIGN.md](DESIGN.md) for architecture details and plugin development guide.

## Project Structure

```
openctl/
├── cmd/openctl/           # CLI entry point
├── internal/
│   ├── cli/               # Cobra commands
│   ├── config/            # Configuration loading
│   ├── manifest/          # YAML manifest parsing
│   ├── plugin/            # Plugin discovery & execution
│   ├── output/            # Output formatting
│   └── state/             # State management
├── pkg/protocol/          # Shared types for plugins
├── plugins/
│   ├── proxmox/           # Proxmox VE plugin
│   └── k3s/               # K3s cluster plugin
└── test/e2e/              # End-to-end tests
```

## License

MIT License
