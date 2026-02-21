# OpenCtl Design Document

This document describes the architecture of OpenCtl and provides guidance for developing new plugins.

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│                        openctl CLI                          │
│  ┌─────────┐  ┌─────────┐  ┌──────────┐  ┌───────────────┐ │
│  │ Config  │  │ Manifest│  │  Output  │  │Plugin Registry│ │
│  │ Loader  │  │ Parser  │  │Formatter │  │  & Discovery  │ │
│  └─────────┘  └─────────┘  └──────────┘  └───────────────┘ │
└─────────────────────────────────────────────────────────────┘
                              │
                    stdin/stdout JSON
                              │
┌─────────────────────────────┴───────────────────────────────┐
│                     Plugin (openctl-*)                       │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────┐  │
│  │  Handler    │  │ Provider    │  │ Resource Converters │  │
│  │  Router     │  │   Client    │  │                     │  │
│  └─────────────┘  └─────────────┘  └─────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
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
│   │   └── protocol.go              # Re-exported protocol types
│   ├── output/
│   │   └── formatter.go             # Table/YAML/JSON output
│   └── errors/
│       └── errors.go                # Error types
├── pkg/protocol/                    # Shared types (for plugin authors)
│   ├── request.go                   # Request structure
│   ├── response.go                  # Response + Capabilities
│   └── resource.go                  # Resource definition
└── plugins/
    └── proxmox/                     # Example plugin
        ├── cmd/openctl-proxmox/
        ├── internal/
        │   ├── handler/             # Request handlers
        │   ├── client/              # API client
        │   └── resources/           # Resource converters
        └── go.mod                   # Separate module
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
  ]
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
  }
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
    "spec": {...},
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
    {"apiVersion": "...", "kind": "...", "metadata": {...}},
    {"apiVersion": "...", "kind": "...", "metadata": {...}}
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

func (h *Handler) listResources() (*protocol.Response, error) {
    // Call your API client to list resources
    items, err := h.client.List()
    if err != nil {
        return nil, err
    }

    var resources []*protocol.Resource
    for _, item := range items {
        resources = append(resources, convertToResource(item))
    }

    return &protocol.Response{
        Status:    protocol.StatusSuccess,
        Resources: resources,
    }, nil
}

// Implement other handlers...
```

### Step 5: Implement API Client

Create `plugins/myprovider/internal/client/client.go`:

```go
package client

import (
    "net/http"
    "time"
)

type Client struct {
    endpoint   string
    token      string
    httpClient *http.Client
}

func New(endpoint, tokenID, tokenSecret string) *Client {
    return &Client{
        endpoint: endpoint,
        token:    tokenSecret,
        httpClient: &http.Client{
            Timeout: 60 * time.Second,
        },
    }
}

// Implement List, Get, Create, Delete methods...
```

### Step 6: Build and Install

Add to `Makefile`:

```makefile
build-myprovider:
    cd plugins/myprovider && go build -o ../../bin/openctl-myprovider ./cmd/openctl-myprovider

install-myprovider: build-myprovider
    cp bin/openctl-myprovider ~/.openctl/plugins/
```

Build and install:

```bash
make build-myprovider install-myprovider
```

### Step 7: Test Your Plugin

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
- `aws.openctl.io/v1`
- `docker.openctl.io/v1beta1`

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
      storage-class: fast
```

## Testing Plugins

### Unit Tests

Test your handlers without network calls:

```go
func TestHandler_List(t *testing.T) {
    // Mock client or use test fixtures
    h := &Handler{
        config: &protocol.ProviderConfig{},
        client: mockClient,
    }

    req := &protocol.Request{
        Version:      protocol.ProtocolVersion,
        Action:       protocol.ActionList,
        ResourceType: "MyResource",
    }

    resp, err := h.Handle(req)
    // Assert...
}
```

### Integration Tests

Test the full plugin binary:

```go
func TestPlugin_Capabilities(t *testing.T) {
    cmd := exec.Command("./openctl-myprovider", "--capabilities")
    output, err := cmd.Output()
    // Assert capabilities JSON...
}

func TestPlugin_Request(t *testing.T) {
    cmd := exec.Command("./openctl-myprovider")
    cmd.Stdin = strings.NewReader(`{"version":"1.0","action":"list",...}`)
    output, err := cmd.Output()
    // Assert response...
}
```

## Best Practices

1. **Error Handling**: Return protocol errors for expected failures, Go errors for unexpected ones
2. **Timeouts**: Respect the timeout passed in the request context
3. **Idempotency**: Make `create` and `apply` operations idempotent when possible
4. **Status**: Populate the `status` field with runtime information (state, IDs, etc.)
5. **Logging**: Write debug logs to stderr (stdout is reserved for protocol)
6. **Validation**: Validate manifests early and return clear error messages

## Future Enhancements

- [ ] Progress streaming for long-running operations
- [ ] Watch/subscribe for resource changes
- [ ] Plugin versioning and compatibility checking
- [ ] Plugin marketplace/registry
- [ ] gRPC transport option for performance
