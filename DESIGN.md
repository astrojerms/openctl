# OpenCtl Design Document

This document describes the architecture of OpenCtl and provides guidance for developing new plugins.

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────────┐
│                            openctl CLI                                   │
│  ┌─────────┐  ┌─────────┐  ┌──────────┐  ┌───────────┐  ┌───────────┐   │
│  │ Config  │  │ Manifest│  │  Output  │  │  Plugin   │  │   State   │   │
│  │ Loader  │  │ Parser  │  │Formatter │  │ Discovery │  │  Manager  │   │
│  └─────────┘  └─────────┘  └──────────┘  └───────────┘  └───────────┘   │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                          stdin/stdout JSON
                                    │
┌───────────────────────────────────┴─────────────────────────────────────┐
│                        Plugin (openctl-*)                                │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌────────────────┐  │
│  │   Handler   │  │  Provider   │  │  Resource   │  │    Dispatch    │  │
│  │   Router    │  │   Client    │  │ Converters  │  │   Generator    │  │
│  └─────────────┘  └─────────────┘  └─────────────┘  └────────────────┘  │
└─────────────────────────────────────────────────────────────────────────┘
```

## Project Structure

```
openctl/
├── cmd/openctl/main.go              # CLI entry point
├── internal/
│   ├── cli/                         # Cobra commands
│   │   ├── root.go                  # Root command + globals
│   │   ├── provider.go              # Dynamic provider subcommands
│   │   └── actions.go               # get/create/delete/apply commands
│   ├── config/
│   │   ├── config.go                # Config types + loading
│   │   └── paths.go                 # ~/.openctl paths
│   ├── manifest/
│   │   └── manifest.go              # YAML parsing
│   ├── plugin/
│   │   ├── discovery.go             # Find openctl-* binaries
│   │   ├── executor.go              # Exec + stdin/stdout communication
│   │   └── dispatcher.go            # Cross-plugin dispatch
│   ├── output/
│   │   └── formatter.go             # Table/YAML/JSON output
│   ├── state/
│   │   └── manager.go               # State persistence
│   └── errors/
│       └── errors.go                # Error types
├── pkg/protocol/                    # Shared types (for plugin authors)
│   ├── request.go                   # Request structure
│   ├── response.go                  # Response + Capabilities + State
│   ├── resource.go                  # Resource definition
│   └── dispatch.go                  # Dispatch protocol types
├── plugins/
│   ├── proxmox/                     # Proxmox VE plugin
│   │   ├── cmd/openctl-proxmox/
│   │   └── internal/
│   │       ├── handler/             # Request handlers
│   │       ├── client/              # Proxmox API client
│   │       ├── resources/           # VM/Template converters
│   │       └── compute/             # Compute interface impl
│   └── k3s/                         # K3s cluster plugin
│       ├── cmd/openctl-k3s/
│       └── internal/
│           ├── handler/             # Request handlers
│           ├── cluster/             # Create/delete logic
│           ├── resources/           # Cluster spec parsing
│           └── ssh/                 # SSH client for K3s install
└── test/
    └── e2e/                         # End-to-end tests
        ├── harness.go               # Test harness with mock plugins
        └── cli_test.go              # CLI integration tests
```

## Plugin Protocol

OpenCtl uses a JSON-over-stdio protocol to communicate with plugins. This design is inspired by Terraform's plugin system but simplified for our use case.

### Plugin Discovery

Plugins are discovered by searching for executables named `openctl-<provider>` in:

1. `~/.openctl/plugins/` (user plugins, highest priority)
2. Directories in `$PATH`

### Capabilities Request

When OpenCtl starts, it queries each plugin for its capabilities:

```bash
openctl-proxmox --capabilities
```

Response:
```json
{
  "providerName": "proxmox",
  "protocolVersion": "1.0",
  "resources": [
    {
      "kind": "VirtualMachine",
      "plural": "vms",
      "actions": ["get", "list", "create", "delete", "apply"]
    },
    {
      "kind": "Template",
      "plural": "templates",
      "actions": ["get", "list"]
    }
  ],
  "computeProvider": {
    "implements": "compute.openctl.io/v1",
    "features": ["cloudImage", "cloudInit", "sshKeys"]
  },
  "supportsDispatch": false
}
```

### Request/Response Protocol

For operations, OpenCtl sends a JSON request via stdin and reads the response from stdout:

**Request Format:**
```json
{
  "version": "1.0",
  "action": "create",
  "resourceType": "VirtualMachine",
  "resourceName": "web-01",
  "manifest": {
    "apiVersion": "proxmox.openctl.io/v1",
    "kind": "VirtualMachine",
    "metadata": {
      "name": "web-01",
      "labels": {"role": "webserver"}
    },
    "spec": {
      "node": "pve1",
      "cpu": {"cores": 4},
      "memory": {"size": 8192}
    }
  },
  "config": {
    "endpoint": "https://pve.example.com:8006",
    "node": "pve1",
    "tokenId": "root@pam!openctl",
    "tokenSecret": "secret-token",
    "defaults": {"storage": "local-lvm"}
  },
  "continuationToken": "",
  "dispatchResults": []
}
```

**Success Response:**
```json
{
  "status": "success",
  "resource": {
    "apiVersion": "proxmox.openctl.io/v1",
    "kind": "VirtualMachine",
    "metadata": {"name": "web-01"},
    "spec": {},
    "status": {"state": "running", "vmid": 100}
  },
  "message": "VM web-01 created successfully"
}
```

**List Response:**
```json
{
  "status": "success",
  "resources": [
    {"apiVersion": "...", "kind": "...", "metadata": {}},
    {"apiVersion": "...", "kind": "...", "metadata": {}}
  ]
}
```

**Error Response:**
```json
{
  "status": "error",
  "error": {
    "code": "NOT_FOUND",
    "message": "VM not found",
    "details": "VM 'web-01' does not exist on node pve1"
  }
}
```

### Action Types

| Action | Description | Request Fields | Response |
|--------|-------------|----------------|----------|
| `list` | List all resources | `resourceType` | `resources[]` |
| `get` | Get single resource | `resourceType`, `resourceName` | `resource` |
| `create` | Create resource | `resourceType`, `manifest` | `resource`, `message` |
| `delete` | Delete resource | `resourceType`, `resourceName` | `message` |
| `apply` | Create or update | `resourceType`, `manifest` | `resource`, `message` |

### Error Codes

| Code | Description |
|------|-------------|
| `NOT_FOUND` | Resource does not exist |
| `ALREADY_EXISTS` | Resource already exists (for create) |
| `INVALID_REQUEST` | Invalid request format or parameters |
| `UNAUTHORIZED` | Authentication failed |
| `INTERNAL` | Internal plugin error |

## Cross-Plugin Dispatch

Plugins can delegate operations to other plugins using the dispatch protocol. This enables orchestration plugins (like K3s) that compose resources from multiple providers.

### Dispatch Flow

```
┌──────────┐     ┌──────────┐     ┌──────────┐     ┌──────────┐
│  User    │     │  CLI     │     │  K3s     │     │ Proxmox  │
│          │     │          │     │ Plugin   │     │ Plugin   │
└────┬─────┘     └────┬─────┘     └────┬─────┘     └────┬─────┘
     │                │                │                │
     │ create cluster │                │                │
     │───────────────>│                │                │
     │                │   request      │                │
     │                │───────────────>│                │
     │                │                │                │
     │                │  dispatchReqs  │                │
     │                │<───────────────│                │
     │                │                │                │
     │                │             create VM           │
     │                │────────────────────────────────>│
     │                │                │                │
     │                │             result              │
     │                │<────────────────────────────────│
     │                │                │                │
     │                │  request +     │                │
     │                │  results       │                │
     │                │───────────────>│                │
     │                │                │                │
     │                │   response     │                │
     │                │<───────────────│                │
     │  result        │                │                │
     │<───────────────│                │                │
```

### Dispatch Request

When a plugin needs to delegate work, it returns dispatch requests:

```json
{
  "status": "success",
  "message": "Creating 3 VMs for cluster dev...",
  "dispatchRequests": [
    {
      "id": "vm-dev-cp-0",
      "provider": "proxmox",
      "action": "create",
      "resourceType": "VirtualMachine",
      "manifest": {
        "apiVersion": "proxmox.openctl.io/v1",
        "kind": "VirtualMachine",
        "metadata": {"name": "dev-cp-0"},
        "spec": {}
      },
      "waitFor": {
        "field": "status.state",
        "value": "running",
        "timeout": "5m"
      }
    }
  ],
  "continuation": {
    "token": "vms-created"
  }
}
```

### Dispatch Result

The CLI executes dispatch requests and calls the plugin again with results:

```json
{
  "version": "1.0",
  "action": "create",
  "resourceType": "Cluster",
  "continuationToken": "vms-created",
  "dispatchResults": [
    {
      "id": "vm-dev-cp-0",
      "status": "success",
      "resource": {
        "apiVersion": "proxmox.openctl.io/v1",
        "kind": "VirtualMachine",
        "metadata": {"name": "dev-cp-0"},
        "status": {"state": "running", "vmid": 100, "ip": "192.168.1.50"}
      }
    }
  ]
}
```

### Wait Conditions

Dispatch requests can include wait conditions:

```json
{
  "waitFor": {
    "field": "status.state",
    "value": "running",
    "timeout": "5m"
  }
}
```

The CLI will poll the resource until the condition is met or timeout occurs.

## State Management

Plugins can request the CLI to persist state for tracking complex resources.

### State Update

Plugins return state updates to save resource state:

```json
{
  "status": "success",
  "stateUpdate": {
    "operation": "save",
    "provider": "k3s",
    "name": "dev-cluster",
    "state": {
      "apiVersion": "k3s.openctl.io/v1",
      "kind": "Cluster",
      "spec": {},
      "status": {
        "phase": "Ready",
        "message": "Cluster is ready",
        "outputs": {
          "kubeconfigPath": "/home/user/.openctl/k3s/dev-cluster/kubeconfig",
          "serverIP": "192.168.1.50"
        }
      },
      "children": [
        {"provider": "proxmox", "kind": "VirtualMachine", "name": "dev-cp-0"},
        {"provider": "proxmox", "kind": "VirtualMachine", "name": "dev-worker-0"}
      ]
    }
  }
}
```

### State Operations

| Operation | Description |
|-----------|-------------|
| `save` | Create or update state |
| `delete` | Remove state |

### State Storage

State is stored in `~/.openctl/state/<provider>/<name>.yaml`:

```yaml
apiVersion: k3s.openctl.io/v1
kind: Cluster
spec:
  compute:
    provider: proxmox
status:
  phase: Ready
  message: Cluster is ready
  outputs:
    kubeconfigPath: /home/user/.openctl/k3s/dev-cluster/kubeconfig
children:
  - provider: proxmox
    kind: VirtualMachine
    name: dev-cp-0
```

### Child References

State can track child resources for cascading operations (e.g., delete cluster → delete VMs):

```json
{
  "children": [
    {"provider": "proxmox", "kind": "VirtualMachine", "name": "dev-cp-0"},
    {"provider": "proxmox", "kind": "VirtualMachine", "name": "dev-worker-0"}
  ]
}
```

## Creating a New Plugin

### Step 1: Create Project Structure

```bash
mkdir -p plugins/myprovider/cmd/openctl-myprovider
mkdir -p plugins/myprovider/internal/{handler,client,resources}
```

### Step 2: Initialize Go Module

Create `plugins/myprovider/go.mod`:

```go
module github.com/openctl/openctl-myprovider

go 1.21

require github.com/openctl/openctl v0.0.0

replace github.com/openctl/openctl => ../..
```

### Step 3: Implement Main Entry Point

Create `plugins/myprovider/cmd/openctl-myprovider/main.go`:

```go
package main

import (
    "encoding/json"
    "os"

    "github.com/openctl/openctl-myprovider/internal/handler"
    "github.com/openctl/openctl/pkg/protocol"
)

func main() {
    // Handle capabilities request
    if len(os.Args) > 1 && os.Args[1] == "--capabilities" {
        printCapabilities()
        return
    }

    // Handle normal request
    if err := handleRequest(); err != nil {
        writeError(err)
        os.Exit(1)
    }
}

func printCapabilities() {
    caps := protocol.Capabilities{
        ProviderName:    "myprovider",
        ProtocolVersion: protocol.ProtocolVersion,
        Resources: []protocol.ResourceDefinition{
            {
                Kind:    "MyResource",
                Plural:  "myresources",
                Actions: []string{"get", "list", "create", "delete", "apply"},
            },
        },
    }
    json.NewEncoder(os.Stdout).Encode(caps)
}

func handleRequest() error {
    var req protocol.Request
    if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
        return err
    }

    h := handler.New(&req.Config)
    resp, err := h.Handle(&req)
    if err != nil {
        return err
    }

    return json.NewEncoder(os.Stdout).Encode(resp)
}

func writeError(err error) {
    resp := protocol.Response{
        Status: protocol.StatusError,
        Error: &protocol.Error{
            Code:    protocol.ErrorCodeInternal,
            Message: err.Error(),
        },
    }
    json.NewEncoder(os.Stdout).Encode(resp)
}
```

### Step 4: Implement Request Handler

Create `plugins/myprovider/internal/handler/handler.go`:

```go
package handler

import (
    "fmt"

    "github.com/openctl/openctl-myprovider/internal/client"
    "github.com/openctl/openctl/pkg/protocol"
)

type Handler struct {
    config *protocol.ProviderConfig
    client *client.Client
}

func New(config *protocol.ProviderConfig) *Handler {
    return &Handler{
        config: config,
        client: client.New(config.Endpoint, config.TokenID, config.TokenSecret),
    }
}

func (h *Handler) Handle(req *protocol.Request) (*protocol.Response, error) {
    switch req.ResourceType {
    case "MyResource":
        return h.handleMyResource(req)
    default:
        return &protocol.Response{
            Status: protocol.StatusError,
            Error: &protocol.Error{
                Code:    protocol.ErrorCodeInvalidRequest,
                Message: fmt.Sprintf("unknown resource type: %s", req.ResourceType),
            },
        }, nil
    }
}

func (h *Handler) handleMyResource(req *protocol.Request) (*protocol.Response, error) {
    switch req.Action {
    case protocol.ActionList:
        return h.listResources()
    case protocol.ActionGet:
        return h.getResource(req.ResourceName)
    case protocol.ActionCreate:
        return h.createResource(req.Manifest)
    case protocol.ActionDelete:
        return h.deleteResource(req.ResourceName)
    case protocol.ActionApply:
        return h.applyResource(req.Manifest)
    default:
        return &protocol.Response{
            Status: protocol.StatusError,
            Error: &protocol.Error{
                Code:    protocol.ErrorCodeInvalidRequest,
                Message: fmt.Sprintf("unknown action: %s", req.Action),
            },
        }, nil
    }
}
```

### Step 5: Build and Install

Add to `Makefile`:

```makefile
build-plugin-myprovider:
    cd plugins/myprovider && go build -o ../../bin/openctl-myprovider ./cmd/openctl-myprovider

install-plugin-myprovider: build-plugin-myprovider
    mkdir -p ~/.openctl/plugins
    cp bin/openctl-myprovider ~/.openctl/plugins/
```

### Step 6: Test Your Plugin

```bash
# Test capabilities
./bin/openctl-myprovider --capabilities

# Test with openctl
openctl plugin list
openctl myprovider get myresources
```

## Resource Manifest Format

Resources follow a Kubernetes-style format:

```yaml
apiVersion: <provider>.openctl.io/v1
kind: <ResourceKind>
metadata:
  name: <resource-name>
  namespace: <optional-namespace>
  labels:
    key: value
  annotations:
    key: value
spec:
  # Resource-specific configuration
status:
  # Resource status (read-only, populated by provider)
```

### apiVersion Convention

The `apiVersion` should follow the format: `<provider>.openctl.io/<version>`

Examples:
- `proxmox.openctl.io/v1`
- `k3s.openctl.io/v1`
- `aws.openctl.io/v1beta1`

This allows OpenCtl to auto-detect the provider when using `openctl apply -f manifest.yaml`.

## Configuration

### Provider Config Structure

The `ProviderConfig` passed to plugins contains:

```go
type ProviderConfig struct {
    Endpoint    string            // API endpoint URL
    Node        string            // Optional: default node/region
    TokenID     string            // Authentication token ID
    TokenSecret string            // Authentication token secret
    Defaults    map[string]string // Provider-specific defaults
}
```

### Adding Provider-Specific Config

Users configure providers in `~/.openctl/config.yaml`:

```yaml
providers:
  myprovider:
    default-context: production
    contexts:
      production:
        endpoint: https://api.example.com
        credentials: prod-creds
      staging:
        endpoint: https://staging.example.com
        credentials: staging-creds
    credentials:
      prod-creds:
        tokenId: my-token
        tokenSecretFile: ~/.openctl/secrets/prod.token
    defaults:
      region: us-east-1
```

## Testing Plugins

### Unit Tests

Test handlers without network calls using HTTP mocking:

```go
func TestHandler_List(t *testing.T) {
    // Create mock server
    server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        json.NewEncoder(w).Encode(mockResponse)
    }))
    defer server.Close()

    h := New(&protocol.ProviderConfig{
        Endpoint: server.URL,
    })

    req := &protocol.Request{
        Version:      protocol.ProtocolVersion,
        Action:       protocol.ActionList,
        ResourceType: "MyResource",
    }

    resp, err := h.Handle(req)
    // Assert...
}
```

### E2E Tests

Use the test harness to test full CLI flows:

```go
func TestPlugin_ListResources(t *testing.T) {
    h := NewHarness(t)
    defer h.Cleanup()

    h.InstallMockPlugin("mock", &MockPluginResponse{
        Capabilities: &protocol.Capabilities{
            ProviderName: "mock",
            Resources: []protocol.ResourceDefinition{
                {Kind: "MyResource", Plural: "myresources", Actions: []string{"list"}},
            },
        },
        Responses: map[string]*protocol.Response{
            "list:MyResource:": {
                Status: protocol.StatusSuccess,
                Resources: []*protocol.Resource{
                    {Metadata: protocol.ResourceMetadata{Name: "test"}},
                },
            },
        },
    })

    result := h.Run("mock", "get", "myresources")
    result.AssertSuccess(t)
    result.AssertOutputContains(t, "test")
}
```

## Best Practices

1. **Error Handling**: Return protocol errors for expected failures, Go errors for unexpected ones
2. **Timeouts**: Respect the timeout passed in the request config
3. **Idempotency**: Make `create` and `apply` operations idempotent when possible
4. **Status**: Populate the `status` field with runtime information (state, IDs, IPs, etc.)
5. **Logging**: Write debug logs to stderr (stdout is reserved for protocol)
6. **Validation**: Validate manifests early and return clear error messages
7. **Testing**: Write unit tests with HTTP mocking; avoid network calls in tests

## K3s Plugin Agent

The K3s plugin ships a per-node agent (`openctl-k3s-agent`) installed on every cluster node at `create` time. It exists so post-create operations can do host-level things the Kubernetes API can't see (systemd state, journald, k3s binary lifecycle) without paying SSH-handshake cost on every command and without depending on a healthy API server.

### Design principles

1. **The agent only does what kubectl can't.** If a healthy kubectl from the user's laptop could do it, the plugin calls the Kubernetes API directly — the agent does not expose passthrough endpoints (no `GET /pods`, no exec, no kubeconfig proxy).
2. **Stateless.** No caching of cluster state. Every request live-reads from systemd/proc/the local k3s API.
3. **Per-node, not in-cluster.** A pod-based service is unreachable exactly when out-of-band tooling is most needed. Per-node systemd/OpenRC units survive a broken k3s.
4. **mTLS, per-cluster CA.** A CA is generated at `create` time. Each node gets its own server certificate; the controller (the laptop running `openctl`) holds a single client certificate for the cluster. Cert material lives under `~/.openctl/state/k3s/<cluster>/`.
5. **Bootstrap stays SSH.** SSH installs k3s and drops the agent. Everything post-install goes through the agent.

### Architecture

```
┌──────────────────┐                 ┌─────────────────────────────────────┐
│  openctl k3s     │                 │  k3s node                           │
│  (plugin)        │                 │  ┌───────────────────────────────┐  │
│                  │   mTLS HTTPS    │  │ openctl-k3s-agent (systemd or │  │
│  • client cert   │ ───────────────>│  │  OpenRC service, :9443)       │  │
│  • per-cluster   │                 │  │                               │  │
│    CA            │                 │  │  /v1/info                     │  │
│  • node endpoint │                 │  │  /v1/logs/k3s                 │  │
│    map (state)   │                 │  │  /v1/service/k3s/{start,...}  │  │
│                  │                 │  └───────────────────────────────┘  │
│                  │                 │                                     │
│                  │  k8s API (in-   │  ┌───────────────────────────────┐  │
│  • kubeconfig    │ ───────────────>│  │ k3s (server or agent)         │  │
│                  │  band; agent    │  │                               │  │
│                  │  not involved)  │  └───────────────────────────────┘  │
└──────────────────┘                 └─────────────────────────────────────┘
```

### OS heterogeneity

Nodes may run different distros, init systems, and architectures. The agent absorbs these differences so the plugin doesn't have to:

| Concern | Strategy |
|---|---|
| CPU architecture | Static Go binary, multi-arch builds (linux/amd64, linux/arm64, linux/arm). SSH bootstrap picks the right artifact via `uname -m`. |
| Init system | Detected at SSH bootstrap: probe `systemctl --version`, fall back to `rc-service --version` (OpenRC). Fail clearly if neither. Two unit-file templates. |
| Logs | Agent's `/v1/logs/k3s` chooses `journalctl -u k3s` or tails `/var/log/k3s.log` based on its own startup detection. Plugin doesn't know or care. |
| Service control | Agent's `/v1/service/k3s/*` runs `systemctl ...` or `rc-service ...` based on detected init. |
| k3s file paths | `/var/lib/rancher/k3s/` and `/etc/rancher/k3s/` are k3s-managed and consistent across distros — assumed by the agent. |

The agent reports its detected environment in `/v1/info` (`os`, `arch`, `init`, capability flags). The plugin uses these for operator-facing diagnostics, not for routing — routing is the agent's responsibility.

### Bootstrap flow

`create` runs SSH-based steps in this order:

1. Existing: install k3s on the first control plane, then additional CPs and workers.
2. New: generate the per-cluster CA (if not already cached), generate per-node server cert + key, generate the controller's client cert + key (once per cluster).
3. New: for each node, detect init system, upload `openctl-k3s-agent`, server cert, server key, and CA cert to `/etc/openctl-k3s-agent/`. Drop the unit file. Enable and start.
4. New: poll each node's `/v1/info` until reachable (with a bounded timeout). Mark cluster `Ready` only after every agent responds.

Cert material on the node lives at `/etc/openctl-k3s-agent/{ca.pem,server.pem,server.key}` (mode `0600`, owned by root).

### State file additions

```yaml
status:
  agent:
    caPath: ~/.openctl/state/k3s/dev/ca.pem
    clientCertPath: ~/.openctl/state/k3s/dev/client.pem
    clientKeyPath: ~/.openctl/state/k3s/dev/client.key
    port: 9443
    endpoints:
      dev-cp-0: 192.168.1.50
      dev-worker-0: 192.168.1.51
```

### RPC surface (initial)

| Endpoint | Purpose |
|---|---|
| `GET /v1/info` | Host facts, agent version, init system, k3s service status, supported capabilities |
| `GET /v1/logs/k3s?lines=N` | Recent k3s logs (abstracted over journald/files) |
| `POST /v1/service/k3s/{start,stop,restart}` | Init-system-agnostic service control |

Explicitly out of scope until proven needed: pod listing, exec, file uploads, binary upgrades, cert rotation. These can be added later as the agent only grows where the kubectl-equivalent path can't reach.

### Version skew

The agent reports its version in `/v1/info`. If it differs from what the plugin was built against, the plugin prints a warning to stderr and proceeds with the call. There is no hard refusal — operators upgrading a fleet often have nodes at mixed versions for short windows.

### What the agent must not become

- A kubectl substitute. If a feature request can be satisfied by `kubectl <verb>` from the user's laptop, the plugin should do that, not add an endpoint.
- A stateful daemon. No background polling, no caching, no reconciliation loops. Each request is a fresh read.
- A general-purpose remote shell. Endpoints are narrow, named, and audit-friendly — not `POST /v1/exec`.

## Future Enhancements

- [ ] Progress streaming for long-running operations
- [ ] Watch/subscribe for resource changes
- [ ] Plugin versioning and compatibility checking
- [ ] Plugin marketplace/registry
- [ ] gRPC transport option for performance
- [x] Automatic retry with backoff for transient failures (implemented in dispatcher)
- [ ] Additional compute providers (AWS, Azure, GCP)
- [ ] K3s cluster upgrades (will use the agent's service control + future binary-swap endpoint)
- [ ] Certificate rotation for K3s clusters (agent + new endpoint)
- [x] **Plugin-defined CLI subcommands** (generic protocol + CLI surface,
      plus the k3s `logs`/`restart` handlers — see below)

## Plugin-defined CLI subcommands

**Status: shipped.** The generic CLI capability layer landed first
(`protocol.Capabilities.Subcommands`, `protocol.Request.Args`, and
`internal/cli/provider.go` registering plugin-defined Cobra commands alongside
`get`/`create`/`delete`/`apply`), and the k3s plugin now advertises and
implements the first two agent-backed subcommands:

- `openctl k3s logs <cluster> [--node <name>] [--lines N]` — fetches the k3s
  journal from a node's agent. Single-node clusters pick the node
  automatically; multi-node clusters require `--node`.
- `openctl k3s restart <cluster> --node <name>` — restarts the k3s service on
  a node via its agent.
- `openctl k3s upgrade <cluster> --to <version>` (future — needs a
  binary-swap agent endpoint).

Handler dispatch lives in `pkg/k3s/handler/handler.go` (`handleLogs`,
`handleRestart`), which load the cluster state, reuse `extractAgentProbeConfig`
to locate the agent bundle, build a per-node `agentclient.Client` via
`client.NewFromProbeOptions`, and call the typed `Logs`/`RestartK3s` methods.
Subcommand requests arrive with an agent `Action` and no `ResourceType`, so
`Handle` routes on the action name before the resource-kind switch.

**Original design (option 3 from the rollout discussion):** extend `protocol.Capabilities` with a list of plugin-defined subcommands, and have `internal/cli/provider.go` register them as cobra commands alongside `get`/`create`/`delete`/`apply`.

**Implemented approach (option 3 from the rollout discussion):** extend `protocol.Capabilities` with a list of plugin-defined subcommands, and have `internal/cli/provider.go` register them as cobra commands alongside `get`/`create`/`delete`/`apply`.

```go
// pkg/protocol/response.go
type Capabilities struct {
    // ... existing fields ...
    Subcommands []SubcommandDefinition `json:"subcommands,omitempty"`
}

type SubcommandDefinition struct {
    Name        string         `json:"name"`        // e.g. "logs"
    Short       string         `json:"short"`       // one-line help
    Long        string         `json:"long,omitempty"`
    Action      string         `json:"action"`      // value sent in Request.Action
    PositionalArgs []ArgSpec   `json:"positionalArgs,omitempty"` // e.g. [{Name:"cluster", Required:true}]
    Flags       []FlagSpec     `json:"flags,omitempty"`
}

type FlagSpec struct {
    Name      string `json:"name"`              // long form, e.g. "node"
    Short     string `json:"short,omitempty"`   // single-char, e.g. "n"
    Type      string `json:"type"`              // "string" | "int" | "bool"
    Default   string `json:"default,omitempty"`
    Required  bool   `json:"required,omitempty"`
    Help      string `json:"help,omitempty"`
}
```

**CLI side:** in `internal/cli/provider.go`, after registering the standard commands, iterate `caps.Subcommands` and register a cobra command per entry. The command's `RunE` builds a `protocol.Request` with `Action: subcmd.Action`, packs positional args + flag values into `Request.Args map[string]any`, and dispatches via the existing executor. Structured responses go through the existing `formatter`; message-only responses print the message.

**Plugin side (implemented):** the k3s plugin's `handler.Handle` dispatches new
action names (`"logs"`, `"restart"`) that:
1. Load the cluster's saved state file (`loadClusterStatus`).
2. Pull the agent block from `status.outputs.agent` (`extractAgentProbeConfig`).
3. Build an `agentclient.Client` for the selected node (`agentClientForNode` →
   `client.NewFromProbeOptions`).
4. Call the typed method (`c.Logs(ctx, lines)` / `c.RestartK3s(ctx)`) and return
   the result as a `Message`.

**Follow-ups still open:**
- `upgrade` subcommand — blocked on a binary-swap agent endpoint.
- Streaming logs — the current path buffers the whole body; large journals
  could stream (chunked transfer + line-by-line print) later.

**Design considerations addressed:**
- Authentication carryover — subcommands inherit `--context` via the same
  dispatch path (provider config is resolved from `contextName`).
- Sub-resource help in `openctl k3s --help` — cobra registers cleanly from the
  advertised `Short`/`Long`.
- Error response format — agent text bodies (e.g. `/v1/logs/k3s`) are wrapped
  into a structured `protocol.Error` by the handler.
