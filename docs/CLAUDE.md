# CLAUDE.md

This file provides context for Claude Code when working on the OpenCtl project.

## What is OpenCtl?

OpenCtl is a CLI tool that provides a unified interface for managing infrastructure resources across different providers. It combines the familiar kubectl/eksctl user experience with a Terraform-like exec-based plugin system. The core CLI discovers and communicates with provider plugins via a JSON-over-stdio protocol, allowing providers to be developed independently.

The project currently includes two plugins: **Proxmox** (for managing VMs and templates in Proxmox VE) and **K3s** (for orchestrating K3s cluster deployment across compute providers). The K3s plugin demonstrates cross-plugin dispatch, where it delegates VM creation to the Proxmox plugin and then installs K3s via SSH.

## Current State

### Working Features

- **Core CLI**: Full kubectl-like UX (`get`, `create`, `delete`, `apply`)
- **Plugin System**: Discovery, execution, and cross-plugin dispatch
- **Configuration**: Contexts, credentials, secrets file support
- **Output Formats**: Table, YAML, JSON, wide
- **State Management**: Persistent state for complex resources

- **Proxmox Plugin** (fully working):
  - VM lifecycle: list, get, create, delete, apply
  - Clone from templates
  - Create from cloud images (auto-downloads and creates template)
  - Cloud-init configuration (user, SSH keys, static/DHCP IP)
  - Disk import and resizing
  - QEMU guest agent support for IP detection

- **K3s Plugin** (fully working):
  - Cluster create/delete with dispatch to compute providers
  - Static IP allocation (recommended) or DHCP with QEMU agent
  - SSH-based K3s installation
  - HA control plane support
  - Worker node pools
  - State management for tracking clusters and child resources
  - Kubeconfig retrieval and storage
  - E2E tested and working

- **Dispatch System** (fully working):
  - Cross-plugin request dispatch
  - Continuation tokens for multi-phase operations
  - Wait conditions for resource readiness
  - Retry with timeout for transient failures

### Known Limitations

- **QEMU Guest Agent**: Ubuntu cloud images don't enable qemu-guest-agent by default. The Proxmox API doesn't support uploading snippet files for cicustom. **Workaround**: Use static IP configuration in K3s clusters (recommended) or manually enable qemu-guest-agent in your VM template.

- **Static IP Requirement**: For reliable K3s cluster creation, use static IPs via `spec.network.staticIPs`. DHCP mode requires QEMU guest agent which may not be available.

## How to Run/Test

### Building
```bash
# Build everything (CLI + all plugins)
make build

# Build specific components
make build-cli
make build-plugin-proxmox
make build-plugin-k3s

# Install to ~/.openctl/plugins and PATH
make install
```

### Testing
```bash
# Run all unit tests
make test

# Run E2E tests (requires built CLI)
make test-e2e

# Run K3s E2E test (requires Proxmox access)
./plugins/k3s/test/e2e/cluster_create_test.sh

# Run tests with verbose output
go test -v ./...
cd plugins/proxmox && go test -v ./...
cd plugins/k3s && go test -v ./...

# Run specific package tests
go test -v ./internal/config/...
go test -v ./plugins/proxmox/internal/resources/...
```

### Linting & Formatting
```bash
make fmt              # Format all code
make lint             # Run golangci-lint (must be installed)
make modernize        # Apply Go modernizer fixes
make modernize-check  # Check for modernize suggestions without applying
make deps             # Download and tidy dependencies
```

### Manual Testing
```bash
# Test plugin capabilities
./bin/openctl-proxmox --capabilities
./bin/openctl-k3s --capabilities

# Test with openctl
./bin/openctl plugin list
./bin/openctl proxmox get vms
./bin/openctl k3s get clusters
```

## Architecture Map

```
openctl/
├── cmd/openctl/               # CLI entry point
├── internal/
│   ├── cli/                   # Cobra commands (root.go, provider.go, actions.go)
│   ├── config/                # Config types + loading (~/.openctl/config.yaml)
│   ├── manifest/              # YAML manifest parsing
│   ├── plugin/                # Plugin discovery & execution
│   │   ├── discovery.go       # Find openctl-* binaries
│   │   ├── executor.go        # Exec + stdin/stdout communication
│   │   └── dispatcher.go      # Cross-plugin dispatch
│   ├── output/                # Table/YAML/JSON formatting
│   ├── state/                 # State management for resources
│   └── errors/                # Error types
├── pkg/
│   ├── protocol/              # Shared types for plugins
│   │   ├── request.go         # Request structure
│   │   ├── response.go        # Response + Capabilities
│   │   ├── resource.go        # Resource definition
│   │   └── dispatch.go        # Dispatch protocol types
│   └── compute/               # Compute abstraction (planned)
└── plugins/
    ├── proxmox/               # Proxmox VE plugin
    │   ├── cmd/openctl-proxmox/
    │   └── internal/
    │       ├── handler/       # Request routing
    │       ├── client/        # Proxmox API client
    │       ├── resources/     # VM/Template converters
    │       └── compute/       # Compute interface impl
    └── k3s/                   # K3s cluster plugin
        ├── cmd/openctl-k3s/
        └── internal/
            ├── handler/       # Request routing
            ├── cluster/       # Create/delete logic
            ├── resources/     # Cluster spec parsing
            └── ssh/           # SSH client for K3s install
```

### Config Location
- Main config: `~/.openctl/config.yaml`
- Plugins: `~/.openctl/plugins/`
- Secrets: `~/.openctl/secrets/`
- State: `~/.openctl/state/<provider>/`
- K3s kubeconfigs: `~/.openctl/k3s/<cluster-name>/kubeconfig`

## Key Constraints

### Code Style
- Follow the [Google Go Style Guide](https://google.github.io/styleguide/go/)
- Run `make fmt` before committing
- Run `make lint` to catch issues

### Testing Requirements
- **Unit tests**: Write tests when implementing features. Use table-driven tests where appropriate.
- **Regression tests**: When fixing a bug, add a unit test that reproduces the issue first.
- **E2E tests**: Full feature flows should have end-to-end tests covering the happy path and key error cases.

### Proxmox API
- **Always reference the official Proxmox API documentation**: https://pve.proxmox.com/pve-docs/api-viewer/index.html
- The API can be inconsistent with parameter naming and valid values
- Test API calls against a real Proxmox instance when possible
- **Note**: The snippet upload endpoint doesn't support `snippets` content type - only `iso`, `vztmpl`, and `import`

### Security
- Never commit secrets or credentials
- Use `tokenSecretFile` instead of inline `tokenSecret` in config
- Validate input at system boundaries

### Backwards Compatibility
- Plugin protocol version is `1.0` - maintain compatibility
- Config file format should remain backwards compatible
- Resource manifest schemas (apiVersion/kind) are stable

### Documentation
- **Keep CLAUDE.md updated** when making significant changes (new features, architectural changes, bug fixes, new constraints)
- Update relevant READMEs when adding or modifying plugin functionality
- Update DESIGN.md when changing plugin protocol or architecture
- Add examples to `examples/` for new resource types or use cases

### Don't Touch
- Protocol version without coordinated changes across CLI and all plugins
- `~/.openctl/` directory structure without migration plan

## Decision Log

Major architectural decisions are documented in:
- `DESIGN.md` - Plugin architecture, protocol design, manifest format
- Git commit history for incremental decisions
- TODO: Create `docs/decisions/` for ADRs

## Completed Tasks

1. ✅ **GitHub CI workflow** - `.github/workflows/ci.yaml` with build, test, lint, vet, format, modernize checks
2. ✅ **Unit tests for Proxmox plugin** - 22 client tests, handler tests, resource tests with HTTP mocking
3. ✅ **Unit tests for K3s plugin** - 10 cluster tests, 15 handler tests, 10 resource tests
4. ✅ **E2E test framework** - 16 tests in `test/e2e/` with mock plugin compilation, output format verification
5. ✅ **K3s E2E test** - `plugins/k3s/test/e2e/cluster_create_test.sh` with full cluster creation and verification
6. ✅ **Documentation** - Updated README.md, DESIGN.md, plugins/proxmox/README.md, plugins/k3s/README.md
7. ✅ **Cross-plugin dispatch** - CLI dispatcher with continuation handling, wait conditions, state management
8. ✅ **K3s static IP support** - Network configuration with static IP allocation to avoid QEMU agent dependency
9. ✅ **K3s cluster creation** - Full end-to-end working: VM creation, K3s installation, kubeconfig retrieval

## Next Tasks (Prioritized)

1. **Add more compute providers** - AWS, Azure, GCP, or other virtualization platforms
2. **K3s cluster upgrades** - Ability to upgrade K3s version on existing clusters
3. **Watch/subscribe** - Real-time updates for long-running operations
4. **Plugin marketplace** - Registry for discovering and installing plugins
5. **Progress streaming** - Better feedback for long-running operations
