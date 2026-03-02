// Package e2e provides end-to-end testing utilities for OpenCtl
package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

// TestHarness provides utilities for E2E testing
type TestHarness struct {
	t          *testing.T
	tempDir    string
	pluginsDir string
	configDir  string
	configFile string
	binPath    string
}

// NewHarness creates a new test harness
func NewHarness(t *testing.T) *TestHarness {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "openctl-e2e-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	pluginsDir := filepath.Join(tempDir, "plugins")
	configDir := filepath.Join(tempDir, "config")

	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("failed to create plugins dir: %v", err)
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}

	configFile := filepath.Join(configDir, "config.yaml")

	// Find the openctl binary - look in common locations
	binPath := findBinary(t)

	h := &TestHarness{
		t:          t,
		tempDir:    tempDir,
		pluginsDir: pluginsDir,
		configDir:  configDir,
		configFile: configFile,
		binPath:    binPath,
	}

	// Create default config
	h.WriteConfig("")

	return h
}

// findBinary locates the openctl binary
func findBinary(t *testing.T) string {
	t.Helper()

	// Check common locations
	locations := []string{
		"./bin/openctl",
		"../../bin/openctl",
		"../../../bin/openctl",
	}

	for _, loc := range locations {
		if _, err := os.Stat(loc); err == nil {
			absPath, err := filepath.Abs(loc)
			if err == nil {
				return absPath
			}
		}
	}

	// Try to find in PATH
	path, err := exec.LookPath("openctl")
	if err == nil {
		return path
	}

	t.Skip("openctl binary not found - run 'make build' first")
	return ""
}

// Cleanup removes the temporary directory
func (h *TestHarness) Cleanup() {
	os.RemoveAll(h.tempDir)
}

// WriteConfig writes a config file
func (h *TestHarness) WriteConfig(content string) {
	if content == "" {
		content = `defaults:
  output: table
  timeout: 30
`
	}
	if err := os.WriteFile(h.configFile, []byte(content), 0o600); err != nil {
		h.t.Fatalf("failed to write config: %v", err)
	}
}

// MockPluginResponse defines what a mock plugin should return
type MockPluginResponse struct {
	Capabilities *protocol.Capabilities
	Responses    map[string]*protocol.Response // key is "action:resourceType:resourceName"
}

// InstallMockPlugin creates a mock plugin that returns predefined responses
func (h *TestHarness) InstallMockPlugin(name string, mock *MockPluginResponse) string {
	h.t.Helper()

	// Create a simple shell script that acts as a mock plugin
	pluginPath := filepath.Join(h.pluginsDir, "openctl-"+name)

	capsJSON, err := json.Marshal(mock.Capabilities)
	if err != nil {
		h.t.Fatalf("failed to marshal capabilities: %v", err)
	}
	responsesJSON, err := json.Marshal(mock.Responses)
	if err != nil {
		h.t.Fatalf("failed to marshal responses: %v", err)
	}

	// Create a Go program that will be compiled as the mock plugin
	mockCode := fmt.Sprintf(`package main

import (
	"encoding/json"
	"fmt"
	"os"
)

type Request struct {
	Action       string `+"`json:\"action\"`"+`
	ResourceType string `+"`json:\"resourceType\"`"+`
	ResourceName string `+"`json:\"resourceName\"`"+`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--capabilities" {
		fmt.Println(%q)
		return
	}

	var req Request
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		fmt.Fprintf(os.Stderr, "failed to decode request: %%v\n", err)
		os.Exit(1)
	}

	responses := make(map[string]json.RawMessage)
	if err := json.Unmarshal([]byte(%q), &responses); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse responses: %%v\n", err)
		os.Exit(1)
	}

	// Try exact match first
	key := fmt.Sprintf("%%s:%%s:%%s", req.Action, req.ResourceType, req.ResourceName)
	if resp, ok := responses[key]; ok {
		fmt.Println(string(resp))
		return
	}

	// Try without resource name
	key = fmt.Sprintf("%%s:%%s:", req.Action, req.ResourceType)
	if resp, ok := responses[key]; ok {
		fmt.Println(string(resp))
		return
	}

	// Default error
	fmt.Println("{\"status\":\"error\",\"error\":{\"code\":\"NOT_FOUND\",\"message\":\"no mock response configured\"}}")
}
`, string(capsJSON), string(responsesJSON))

	// Write and compile the mock plugin
	mockDir := filepath.Join(h.tempDir, "mock-"+name)
	if err := os.MkdirAll(mockDir, 0o755); err != nil {
		h.t.Fatalf("failed to create mock dir: %v", err)
	}

	mockFile := filepath.Join(mockDir, "main.go")
	if err := os.WriteFile(mockFile, []byte(mockCode), 0o600); err != nil {
		h.t.Fatalf("failed to write mock code: %v", err)
	}

	cmd := exec.Command("go", "build", "-o", pluginPath, mockFile) //nolint:gosec // G204: intentional for test
	if output, err := cmd.CombinedOutput(); err != nil {
		h.t.Fatalf("failed to build mock plugin: %v\n%s", err, output)
	}

	return pluginPath
}

// RunResult contains the result of running a command
type RunResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
}

// Run executes the openctl CLI with the given arguments
func (h *TestHarness) Run(args ...string) *RunResult {
	h.t.Helper()

	// Set up environment to use our test directories
	env := os.Environ()
	env = append(env,
		fmt.Sprintf("HOME=%s", h.configDir),
		fmt.Sprintf("XDG_CONFIG_HOME=%s", h.configDir),
	)

	// Prepend plugins dir to PATH so our mock plugins are found
	for i, e := range env {
		if after, ok := strings.CutPrefix(e, "PATH="); ok {
			env[i] = fmt.Sprintf("PATH=%s:%s", h.pluginsDir, after)
			break
		}
	}

	// Add config flag
	fullArgs := append([]string{"--config", h.configFile}, args...)

	cmd := exec.Command(h.binPath, fullArgs...) //nolint:gosec // G204: intentional for E2E test
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}

	return &RunResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		Err:      err,
	}
}

// AssertSuccess checks that the command succeeded
func (r *RunResult) AssertSuccess(t *testing.T) {
	t.Helper()
	if r.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d\nstdout: %s\nstderr: %s", r.ExitCode, r.Stdout, r.Stderr)
	}
}

// AssertFailure checks that the command failed
func (r *RunResult) AssertFailure(t *testing.T) {
	t.Helper()
	if r.ExitCode == 0 {
		t.Errorf("expected non-zero exit code\nstdout: %s\nstderr: %s", r.Stdout, r.Stderr)
	}
}

// AssertOutputContains checks that stdout contains the given string
func (r *RunResult) AssertOutputContains(t *testing.T, expected string) {
	t.Helper()
	if !strings.Contains(r.Stdout, expected) {
		t.Errorf("expected stdout to contain %q\nstdout: %s", expected, r.Stdout)
	}
}

// AssertOutputNotContains checks that stdout does not contain the given string
func (r *RunResult) AssertOutputNotContains(t *testing.T, notExpected string) {
	t.Helper()
	if strings.Contains(r.Stdout, notExpected) {
		t.Errorf("expected stdout to NOT contain %q\nstdout: %s", notExpected, r.Stdout)
	}
}

// AssertErrorContains checks that stderr contains the given string
func (r *RunResult) AssertErrorContains(t *testing.T, expected string) {
	t.Helper()
	if !strings.Contains(r.Stderr, expected) {
		t.Errorf("expected stderr to contain %q\nstderr: %s", expected, r.Stderr)
	}
}

// AssertJSONOutput parses stdout as JSON and returns it
func (r *RunResult) AssertJSONOutput(t *testing.T) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal([]byte(r.Stdout), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v\nstdout: %s", err, r.Stdout)
	}
	return result
}

// AssertJSONArrayOutput parses stdout as a JSON array and returns it
func (r *RunResult) AssertJSONArrayOutput(t *testing.T) []map[string]any {
	t.Helper()
	var result []map[string]any
	if err := json.Unmarshal([]byte(r.Stdout), &result); err != nil {
		t.Fatalf("failed to parse JSON array output: %v\nstdout: %s", err, r.Stdout)
	}
	return result
}

// AssertTableOutput checks that the output looks like a table (has headers)
func (r *RunResult) AssertTableOutput(t *testing.T, expectedHeaders ...string) {
	t.Helper()
	lines := strings.Split(r.Stdout, "\n")
	if len(lines) == 0 {
		t.Fatal("expected table output but got empty")
	}

	headerLine := strings.ToUpper(lines[0])
	for _, header := range expectedHeaders {
		if !strings.Contains(headerLine, strings.ToUpper(header)) {
			t.Errorf("expected table header to contain %q\nheader: %s", header, lines[0])
		}
	}
}
