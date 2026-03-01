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

## Future Enhancements

- [ ] Progress streaming for long-running operations
- [ ] Watch/subscribe for resource changes
- [ ] Plugin versioning and compatibility checking
- [ ] Plugin marketplace/registry
- [ ] gRPC transport option for performance
- [x] Automatic retry with backoff for transient failures (implemented in dispatcher)
- [ ] Additional compute providers (AWS, Azure, GCP)
- [ ] K3s cluster upgrades
- [ ] Certificate rotation for K3s clusters
