package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openctl/openctl/pkg/protocol"
)

func TestExecutor_GetCapabilities(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a mock plugin that returns capabilities
	mockPlugin := filepath.Join(tmpDir, "openctl-mock")
	mockScript := `#!/bin/sh
if [ "$1" = "--capabilities" ]; then
    cat << 'EOF'
{
  "providerName": "mock",
  "protocolVersion": "1.0",
  "resources": [
    {
      "kind": "TestResource",
      "plural": "testresources",
      "actions": ["get", "list", "create", "delete"]
    }
  ]
}
EOF
fi
`
	if err := os.WriteFile(mockPlugin, []byte(mockScript), 0755); err != nil {
		t.Fatalf("failed to create mock plugin: %v", err)
	}

	plugin := &Plugin{Name: "mock", Path: mockPlugin}
	executor := NewExecutor(plugin, 10*time.Second)

	caps, err := executor.GetCapabilities(context.Background())
	if err != nil {
		t.Fatalf("GetCapabilities failed: %v", err)
	}

	if caps.ProviderName != "mock" {
		t.Errorf("expected providerName=mock, got %s", caps.ProviderName)
	}
	if caps.ProtocolVersion != "1.0" {
		t.Errorf("expected protocolVersion=1.0, got %s", caps.ProtocolVersion)
	}
	if len(caps.Resources) != 1 {
		t.Errorf("expected 1 resource, got %d", len(caps.Resources))
	}
	if caps.Resources[0].Kind != "TestResource" {
		t.Errorf("expected kind=TestResource, got %s", caps.Resources[0].Kind)
	}
}

func TestExecutor_Execute_List(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a mock plugin that handles list requests
	mockPlugin := filepath.Join(tmpDir, "openctl-mock")
	mockScript := `#!/bin/sh
# Read request from stdin
read request

# Return a list of resources
cat << 'EOF'
{
  "status": "success",
  "resources": [
    {
      "apiVersion": "mock.openctl.io/v1",
      "kind": "TestResource",
      "metadata": {"name": "resource-1"},
      "status": {"state": "active"}
    },
    {
      "apiVersion": "mock.openctl.io/v1",
      "kind": "TestResource",
      "metadata": {"name": "resource-2"},
      "status": {"state": "inactive"}
    }
  ]
}
EOF
`
	if err := os.WriteFile(mockPlugin, []byte(mockScript), 0755); err != nil {
		t.Fatalf("failed to create mock plugin: %v", err)
	}

	plugin := &Plugin{Name: "mock", Path: mockPlugin}
	executor := NewExecutor(plugin, 10*time.Second)

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionList,
		ResourceType: "TestResource",
		Config:       protocol.ProviderConfig{},
	}

	resp, err := executor.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if resp.Status != protocol.StatusSuccess {
		t.Errorf("expected status=success, got %s", resp.Status)
	}
	if len(resp.Resources) != 2 {
		t.Errorf("expected 2 resources, got %d", len(resp.Resources))
	}
	if resp.Resources[0].Metadata.Name != "resource-1" {
		t.Errorf("expected name=resource-1, got %s", resp.Resources[0].Metadata.Name)
	}
}

func TestExecutor_Execute_Get(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a mock plugin that handles get requests
	mockPlugin := filepath.Join(tmpDir, "openctl-mock")
	mockScript := `#!/bin/sh
# Read request from stdin
read request

# Return a single resource
cat << 'EOF'
{
  "status": "success",
  "resource": {
    "apiVersion": "mock.openctl.io/v1",
    "kind": "TestResource",
    "metadata": {"name": "my-resource"},
    "spec": {"field": "value"},
    "status": {"state": "active"}
  }
}
EOF
`
	if err := os.WriteFile(mockPlugin, []byte(mockScript), 0755); err != nil {
		t.Fatalf("failed to create mock plugin: %v", err)
	}

	plugin := &Plugin{Name: "mock", Path: mockPlugin}
	executor := NewExecutor(plugin, 10*time.Second)

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionGet,
		ResourceType: "TestResource",
		ResourceName: "my-resource",
		Config:       protocol.ProviderConfig{},
	}

	resp, err := executor.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if resp.Status != protocol.StatusSuccess {
		t.Errorf("expected status=success, got %s", resp.Status)
	}
	if resp.Resource == nil {
		t.Fatal("expected resource to be non-nil")
	}
	if resp.Resource.Metadata.Name != "my-resource" {
		t.Errorf("expected name=my-resource, got %s", resp.Resource.Metadata.Name)
	}
}

func TestExecutor_Execute_Create(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a mock plugin that handles create requests and echoes back the request
	mockPlugin := filepath.Join(tmpDir, "openctl-mock")
	mockScript := `#!/bin/sh
# Read request from stdin
request=$(cat)

# Extract the name from the manifest
name=$(echo "$request" | grep -o '"name":"[^"]*"' | head -1 | cut -d'"' -f4)

# Return success with message
cat << EOF
{
  "status": "success",
  "message": "Resource $name created successfully"
}
EOF
`
	if err := os.WriteFile(mockPlugin, []byte(mockScript), 0755); err != nil {
		t.Fatalf("failed to create mock plugin: %v", err)
	}

	plugin := &Plugin{Name: "mock", Path: mockPlugin}
	executor := NewExecutor(plugin, 10*time.Second)

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionCreate,
		ResourceType: "TestResource",
		Manifest: &protocol.Resource{
			APIVersion: "mock.openctl.io/v1",
			Kind:       "TestResource",
			Metadata:   protocol.ResourceMetadata{Name: "new-resource"},
			Spec:       map[string]any{"field": "value"},
		},
		Config: protocol.ProviderConfig{},
	}

	resp, err := executor.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if resp.Status != protocol.StatusSuccess {
		t.Errorf("expected status=success, got %s", resp.Status)
	}
	if resp.Message == "" {
		t.Error("expected message to be non-empty")
	}
}

func TestExecutor_Execute_Error(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a mock plugin that returns an error
	mockPlugin := filepath.Join(tmpDir, "openctl-mock")
	mockScript := `#!/bin/sh
cat << 'EOF'
{
  "status": "error",
  "error": {
    "code": "NOT_FOUND",
    "message": "Resource not found",
    "details": "The requested resource does not exist"
  }
}
EOF
`
	if err := os.WriteFile(mockPlugin, []byte(mockScript), 0755); err != nil {
		t.Fatalf("failed to create mock plugin: %v", err)
	}

	plugin := &Plugin{Name: "mock", Path: mockPlugin}
	executor := NewExecutor(plugin, 10*time.Second)

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionGet,
		ResourceType: "TestResource",
		ResourceName: "nonexistent",
		Config:       protocol.ProviderConfig{},
	}

	resp, err := executor.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute should not error: %v", err)
	}

	if resp.Status != protocol.StatusError {
		t.Errorf("expected status=error, got %s", resp.Status)
	}
	if resp.Error == nil {
		t.Fatal("expected error to be non-nil")
	}
	if resp.Error.Code != protocol.ErrorCodeNotFound {
		t.Errorf("expected error.code=NOT_FOUND, got %s", resp.Error.Code)
	}
}

func TestExecutor_Execute_Timeout(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a mock plugin that sleeps longer than timeout
	mockPlugin := filepath.Join(tmpDir, "openctl-mock")
	mockScript := `#!/bin/sh
sleep 5
echo '{"status": "success"}'
`
	if err := os.WriteFile(mockPlugin, []byte(mockScript), 0755); err != nil {
		t.Fatalf("failed to create mock plugin: %v", err)
	}

	plugin := &Plugin{Name: "mock", Path: mockPlugin}
	executor := NewExecutor(plugin, 100*time.Millisecond)

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionGet,
		ResourceType: "TestResource",
		Config:       protocol.ProviderConfig{},
	}

	_, err := executor.Execute(context.Background(), req)
	if err == nil {
		t.Error("expected timeout error")
	}
}

func TestExecutor_Execute_PluginCrash(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a mock plugin that exits with error
	mockPlugin := filepath.Join(tmpDir, "openctl-mock")
	mockScript := `#!/bin/sh
echo "Fatal error" >&2
exit 1
`
	if err := os.WriteFile(mockPlugin, []byte(mockScript), 0755); err != nil {
		t.Fatalf("failed to create mock plugin: %v", err)
	}

	plugin := &Plugin{Name: "mock", Path: mockPlugin}
	executor := NewExecutor(plugin, 10*time.Second)

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionGet,
		ResourceType: "TestResource",
		Config:       protocol.ProviderConfig{},
	}

	_, err := executor.Execute(context.Background(), req)
	if err == nil {
		t.Error("expected error for crashed plugin")
	}
}

func TestExecutor_Execute_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a mock plugin that returns invalid JSON
	mockPlugin := filepath.Join(tmpDir, "openctl-mock")
	mockScript := `#!/bin/sh
echo "not valid json"
`
	if err := os.WriteFile(mockPlugin, []byte(mockScript), 0755); err != nil {
		t.Fatalf("failed to create mock plugin: %v", err)
	}

	plugin := &Plugin{Name: "mock", Path: mockPlugin}
	executor := NewExecutor(plugin, 10*time.Second)

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionGet,
		ResourceType: "TestResource",
		Config:       protocol.ProviderConfig{},
	}

	_, err := executor.Execute(context.Background(), req)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestExecutor_Execute_RequestSerialization(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a mock plugin that echoes back the request for verification
	mockPlugin := filepath.Join(tmpDir, "openctl-mock")
	mockScript := `#!/bin/sh
# Read and echo the request as the response resource spec
request=$(cat)
cat << EOF
{
  "status": "success",
  "resource": {
    "apiVersion": "test/v1",
    "kind": "Echo",
    "metadata": {"name": "echo"},
    "spec": $request
  }
}
EOF
`
	if err := os.WriteFile(mockPlugin, []byte(mockScript), 0755); err != nil {
		t.Fatalf("failed to create mock plugin: %v", err)
	}

	plugin := &Plugin{Name: "mock", Path: mockPlugin}
	executor := NewExecutor(plugin, 10*time.Second)

	req := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       protocol.ActionCreate,
		ResourceType: "TestResource",
		ResourceName: "test-name",
		Config: protocol.ProviderConfig{
			Endpoint:    "https://example.com",
			TokenID:     "user!token",
			TokenSecret: "secret123",
		},
	}

	resp, err := executor.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// The echoed request should be in the spec
	echoedReq := resp.Resource.Spec
	if echoedReq["version"] != protocol.ProtocolVersion {
		t.Errorf("echoed request missing version")
	}
	if echoedReq["action"] != protocol.ActionCreate {
		t.Errorf("echoed request missing action")
	}

	config, ok := echoedReq["config"].(map[string]any)
	if !ok {
		t.Fatal("echoed config should be a map")
	}
	if config["endpoint"] != "https://example.com" {
		t.Errorf("echoed config missing endpoint")
	}
}

func TestExecuteRequest(t *testing.T) {
	// Save original PATH
	origPath := os.Getenv("PATH")
	defer os.Setenv("PATH", origPath)

	tmpDir := t.TempDir()

	// Create a mock plugin
	mockPlugin := filepath.Join(tmpDir, "openctl-testexec")
	mockScript := `#!/bin/sh
if [ "$1" = "--capabilities" ]; then
    echo '{"providerName": "testexec", "protocolVersion": "1.0", "resources": []}'
else
    cat << 'EOF'
{"status": "success", "message": "executed"}
EOF
fi
`
	if err := os.WriteFile(mockPlugin, []byte(mockScript), 0755); err != nil {
		t.Fatalf("failed to create mock plugin: %v", err)
	}

	os.Setenv("PATH", tmpDir+":"+origPath)

	req := &protocol.Request{
		Version: protocol.ProtocolVersion,
		Action:  protocol.ActionGet,
		Config:  protocol.ProviderConfig{},
	}

	resp, err := ExecuteRequest(context.Background(), "testexec", req, 10*time.Second)
	if err != nil {
		t.Fatalf("ExecuteRequest failed: %v", err)
	}

	if resp.Status != protocol.StatusSuccess {
		t.Errorf("expected status=success, got %s", resp.Status)
	}
}

func TestExecuteRequest_PluginNotFound(t *testing.T) {
	req := &protocol.Request{
		Version: protocol.ProtocolVersion,
		Action:  protocol.ActionGet,
		Config:  protocol.ProviderConfig{},
	}

	_, err := ExecuteRequest(context.Background(), "nonexistent-plugin-xyz", req, 10*time.Second)
	if err == nil {
		t.Error("expected error for nonexistent plugin")
	}
}

// Helper to verify request JSON structure
func verifyRequestJSON(t *testing.T, data []byte) {
	t.Helper()

	var req map[string]any
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("invalid request JSON: %v", err)
	}

	required := []string{"version", "action", "config"}
	for _, field := range required {
		if _, ok := req[field]; !ok {
			t.Errorf("request missing required field: %s", field)
		}
	}
}
