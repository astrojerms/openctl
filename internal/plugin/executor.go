package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/openctl/openctl/internal/log"
	"github.com/openctl/openctl/pkg/protocol"
)

// Executor handles communication with plugins
type Executor struct {
	plugin  *Plugin
	timeout time.Duration
}

// NewExecutor creates a new plugin executor
func NewExecutor(plugin *Plugin, timeout time.Duration) *Executor {
	return &Executor{
		plugin:  plugin,
		timeout: timeout,
	}
}

// GetCapabilities retrieves the capabilities of a plugin
func (e *Executor) GetCapabilities(ctx context.Context) (*protocol.Capabilities, error) {
	log.Debug("Getting capabilities from plugin: %s", e.plugin.Path)

	cmd := exec.CommandContext(ctx, e.plugin.Path, "--capabilities") //nolint:gosec // G204: plugin path is from trusted discovery

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		log.Debug("Plugin capabilities error: %v, stderr: %s", err, stderr.String())
		return nil, fmt.Errorf("failed to get plugin capabilities: %w: %s", err, stderr.String())
	}

	var caps protocol.Capabilities
	if err := json.Unmarshal(stdout.Bytes(), &caps); err != nil {
		log.Debug("Failed to parse capabilities JSON: %s", stdout.String())
		return nil, fmt.Errorf("failed to parse plugin capabilities: %w", err)
	}

	log.Debug("Plugin capabilities: provider=%s, resources=%d", caps.ProviderName, len(caps.Resources))
	return &caps, nil
}

// Execute sends a request to the plugin and returns the response
func (e *Executor) Execute(ctx context.Context, req *protocol.Request) (*protocol.Response, error) {
	log.Verbose("Executing plugin: %s", e.plugin.Path)
	log.Verbose("Action: %s, ResourceType: %s, ResourceName: %s", req.Action, req.ResourceType, req.ResourceName)

	if e.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, e.plugin.Path) //nolint:gosec // G204: plugin path is from trusted discovery

	// Pass debug environment to plugin
	cmd.Env = os.Environ()
	if log.IsDebug() {
		cmd.Env = append(cmd.Env, "OPENCTL_DEBUG=1")
	}

	reqData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	log.Debug("Plugin request:")
	log.DebugJSON("Request", req)

	cmd.Stdin = bytes.NewReader(reqData)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Debug("Executing: %s", e.plugin.Path)
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			log.Debug("Plugin timed out")
			return nil, fmt.Errorf("plugin execution timed out")
		}
		stderrStr := stderr.String()
		log.Debug("Plugin error: %v", err)
		if stderrStr != "" {
			log.Debug("Plugin stderr: %s", stderrStr)
			return nil, fmt.Errorf("plugin execution failed: %w: %s", err, stderrStr)
		}
		return nil, fmt.Errorf("plugin execution failed: %w", err)
	}

	// Log stderr if present (might contain warnings)
	if stderrStr := stderr.String(); stderrStr != "" {
		log.Verbose("Plugin stderr: %s", stderrStr)
	}

	log.Debug("Plugin raw response: %s", stdout.String())

	var resp protocol.Response
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		log.Debug("Failed to parse response JSON")
		return nil, fmt.Errorf("failed to parse plugin response: %w: %s", err, stdout.String())
	}

	log.Verbose("Plugin response status: %s", resp.Status)
	if resp.Error != nil {
		log.Verbose("Plugin error: [%s] %s", resp.Error.Code, resp.Error.Message)
	}
	if resp.Resource != nil {
		log.Verbose("Response contains 1 resource")
	}
	if len(resp.Resources) > 0 {
		log.Verbose("Response contains %d resources", len(resp.Resources))
	}

	log.DebugJSON("Response", resp)

	return &resp, nil
}

// ExecuteRequest is a convenience function to find a plugin and execute a request
func ExecuteRequest(ctx context.Context, pluginName string, req *protocol.Request, timeout time.Duration) (*protocol.Response, error) {
	plugin, err := FindPlugin(pluginName)
	if err != nil {
		return nil, err
	}
	if plugin == nil {
		return nil, fmt.Errorf("plugin %q not found", pluginName)
	}

	executor := NewExecutor(plugin, timeout)
	return executor.Execute(ctx, req)
}
