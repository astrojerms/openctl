# OpenCtl

OpenCtl is a CLI tool that provides a unified interface for managing infrastructure resources across different providers. It combines the familiar kubectl/eksctl user experience with a Terraform-like exec-based plugin system.

## Features

- **Kubectl-like UX**: Familiar commands like `get`, `create`, `delete`, and `apply`
- **Plugin Architecture**: Extend functionality through exec-based plugins
- **Multi-provider Support**: Manage resources across different infrastructure providers
- **Declarative Manifests**: Define resources using YAML manifests
- **Multiple Output Formats**: Table, YAML, JSON, and wide output formats
- **Context-based Configuration**: Switch between different environments easily

## Installation

### From Source

```bash
# Clone the repository
git clone https://github.com/openctl/openctl.git
cd openctl

# Build both the CLI and plugins
make build

# Install (copies CLI to PATH and plugins to ~/.openctl/plugins/)
make install
```

### Manual Installation

```bash
# Build
go build -o openctl ./cmd/openctl

# Build the Proxmox plugin
cd plugins/proxmox
go build -o openctl-proxmox ./cmd/openctl-proxmox

# Install
cp openctl /usr/local/bin/
mkdir -p ~/.openctl/plugins
cp openctl-proxmox ~/.openctl/plugins/
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

Store your API token in the secret file:

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
NAME                 PATH
proxmox              /home/user/.openctl/plugins/openctl-proxmox
  Resources: [vms templates]
```

### 3. List Resources

```bash
# List all VMs
openctl proxmox get vms

# List with wide output (more columns)
openctl proxmox get vms -o wide

# Get a specific VM
openctl proxmox get vm web-01

# Output as YAML
openctl proxmox get vm web-01 -o yaml

# Output as JSON
openctl proxmox get vms -o json
```

### 4. Create Resources

Create a VM manifest file `vm.yaml`:

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

Create the VM:

```bash
openctl proxmox create vm -f vm.yaml
```

Or use the auto-detecting apply command:

```bash
openctl apply -f vm.yaml
```

### 5. Update Resources

Modify your manifest and apply changes:

```bash
openctl proxmox apply -f vm.yaml
```

### 6. Delete Resources

```bash
openctl proxmox delete vm web-01
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

#### `openctl <provider> get <resource-type> [name]`

Get one or more resources.

```bash
# List all VMs
openctl proxmox get vms

# Get a specific VM
openctl proxmox get vm web-01
```

#### `openctl <provider> create <resource-type> -f <manifest>`

Create a resource from a manifest file.

```bash
openctl proxmox create vm -f vm.yaml
```

#### `openctl <provider> delete <resource-type> <name>`

Delete a resource.

```bash
openctl proxmox delete vm web-01
```

#### `openctl <provider> apply -f <manifest>`

Create or update a resource from a manifest.

```bash
openctl proxmox apply -f vm.yaml
```

#### `openctl apply -f <manifest>`

Apply a manifest with automatic provider detection (based on `apiVersion`).

```bash
openctl apply -f vm.yaml
```

#### `openctl plugin list`

List installed plugins and their supported resources.

```bash
openctl plugin list
```

#### `openctl config view`

Display the current configuration.

```bash
openctl config view
```

#### `openctl version`

Print version information.

```bash
openctl version
```

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
        tokenSecret: <secret>        # Inline secret (not recommended)
        tokenSecretFile: <path>      # File containing secret (recommended)
    defaults:
      <key>: <value>
```

### Multiple Contexts

Switch between different environments using contexts:

```bash
# Use a specific context
openctl proxmox get vms --context work

# Set default context in config
providers:
  proxmox:
    default-context: work
```

### Proxmox Configuration

For Proxmox, you need to create an API token:

1. Go to Datacenter → Permissions → API Tokens
2. Create a new token for your user
3. Copy the token ID and secret
4. Add them to your config

```yaml
providers:
  proxmox:
    contexts:
      homelab:
        endpoint: https://192.168.1.100:8006
        node: pve1
        credentials: api-token
    credentials:
      api-token:
        tokenId: root@pam!openctl
        tokenSecretFile: ~/.openctl/secrets/proxmox.token
```

## Resource Manifests

### VirtualMachine

```yaml
apiVersion: proxmox.openctl.io/v1
kind: VirtualMachine
metadata:
  name: my-vm
  labels:
    environment: production
spec:
  node: pve1                    # Target Proxmox node
  template:
    name: ubuntu-22.04          # Template name to clone
    # or vmid: 9000             # Template VMID
  cpu:
    cores: 4
    sockets: 1
  memory:
    size: 8192                  # Memory in MB
  disks:
    - name: scsi0
      storage: local-lvm
      size: 50G
  networks:
    - name: net0
      bridge: vmbr0
      model: virtio
  cloudInit:
    user: ubuntu
    password: secret            # Optional
    sshKeys:
      - ssh-ed25519 AAAA...
    ipConfig:
      net0:
        ip: dhcp
        # or ip: 192.168.1.100/24
        # gateway: 192.168.1.1
  startOnCreate: true
```

## Testing

Run the test suite:

```bash
# Run all tests
make test

# Run tests with verbose output
go test -v ./...

# Run specific package tests
go test ./internal/config/...
```

## Development

See [DESIGN.md](DESIGN.md) for architecture details and plugin development guide.

## License

MIT License
