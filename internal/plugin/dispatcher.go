package plugin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/openctl/openctl/internal/config"
	"github.com/openctl/openctl/internal/log"
	"github.com/openctl/openctl/internal/state"
	"github.com/openctl/openctl/pkg/protocol"
)

// Dispatcher handles plugin execution with dispatch support
type Dispatcher struct {
	config  *config.Config
	timeout time.Duration
	store   *state.Store
}

// NewDispatcher creates a new dispatcher
func NewDispatcher(cfg *config.Config, timeout time.Duration) (*Dispatcher, error) {
	store, err := state.NewStore()
	if err != nil {
		return nil, fmt.Errorf("failed to create state store: %w", err)
	}

	return &Dispatcher{
		config:  cfg,
		timeout: timeout,
		store:   store,
	}, nil
}

// ExecuteWithDispatch executes a plugin request, handling dispatches and state updates
func (d *Dispatcher) ExecuteWithDispatch(ctx context.Context, pluginName string, req *protocol.Request) (*protocol.Response, error) {
	plugin, err := FindPlugin(pluginName)
	if err != nil {
		return nil, err
	}
	if plugin == nil {
		return nil, fmt.Errorf("plugin %q not found", pluginName)
	}

	executor := NewExecutor(plugin, d.timeout)
	resp, err := executor.Execute(ctx, req)
	if err != nil {
		return nil, err
	}

	// Process dispatch loop
	for resp.DispatchRequests != nil && len(resp.DispatchRequests) > 0 && resp.Continuation != nil {
		log.Debug("Processing %d dispatch requests", len(resp.DispatchRequests))

		// Execute each dispatch request
		results, err := d.executeDispatches(ctx, resp.DispatchRequests)
		if err != nil {
			return nil, fmt.Errorf("dispatch execution failed: %w", err)
		}

		// Call plugin again with results
		req.DispatchResults = results
		req.ContinuationToken = resp.Continuation.Token

		resp, err = executor.Execute(ctx, req)
		if err != nil {
			return nil, err
		}
	}

	// Handle state update
	if resp.StateUpdate != nil {
		if err := d.handleStateUpdate(resp.StateUpdate); err != nil {
			log.Debug("Failed to handle state update: %v", err)
			// Don't fail the request, just log
		}
	}

	return resp, nil
}

// executeDispatches executes a list of dispatch requests
func (d *Dispatcher) executeDispatches(ctx context.Context, requests []*protocol.DispatchRequest) ([]*protocol.DispatchResult, error) {
	results := make([]*protocol.DispatchResult, len(requests))

	for i, req := range requests {
		log.Debug("Dispatching: %s %s/%s to %s", req.Action, req.ResourceType, req.ResourceName, req.Provider)

		result, err := d.executeDispatch(ctx, req)
		if err != nil {
			results[i] = &protocol.DispatchResult{
				ID:     req.ID,
				Status: protocol.StatusError,
				Error: &protocol.Error{
					Code:    protocol.ErrorCodeInternal,
					Message: err.Error(),
				},
			}
		} else {
			results[i] = result
		}
	}

	return results, nil
}

// executeDispatch executes a single dispatch request
func (d *Dispatcher) executeDispatch(ctx context.Context, req *protocol.DispatchRequest) (*protocol.DispatchResult, error) {
	plugin, err := FindPlugin(req.Provider)
	if err != nil {
		return nil, err
	}
	if plugin == nil {
		return nil, fmt.Errorf("plugin %q not found", req.Provider)
	}

	// Get provider config
	providerConfig, err := d.config.GetProviderConfig(req.Provider, "")
	if err != nil {
		return nil, fmt.Errorf("failed to get provider config: %w", err)
	}

	// Build the request
	pluginReq := &protocol.Request{
		Version:      protocol.ProtocolVersion,
		Action:       req.Action,
		ResourceType: req.ResourceType,
		ResourceName: req.ResourceName,
		Manifest:     req.Manifest,
		Config:       *providerConfig,
	}

	executor := NewExecutor(plugin, d.timeout)
	resp, err := executor.Execute(ctx, pluginReq)
	if err != nil {
		return nil, err
	}

	// Build result
	result := &protocol.DispatchResult{
		ID:     req.ID,
		Status: resp.Status,
	}

	if resp.Status == protocol.StatusError {
		result.Error = resp.Error
	} else if resp.Resource != nil {
		result.Resource = resp.Resource
	} else if len(resp.Resources) > 0 {
		result.Resource = resp.Resources[0]
	}

	// Handle wait condition if specified
	if req.WaitFor != nil && result.Status == protocol.StatusSuccess {
		err := d.waitForCondition(ctx, req.Provider, req.ResourceType, req.ResourceName, req.WaitFor)
		if err != nil {
			return &protocol.DispatchResult{
				ID:     req.ID,
				Status: protocol.StatusError,
				Error: &protocol.Error{
					Code:    protocol.ErrorCodeInternal,
					Message: fmt.Sprintf("wait condition failed: %v", err),
				},
			}, nil
		}

		// Fetch the resource again to get final state
		finalReq := &protocol.Request{
			Version:      protocol.ProtocolVersion,
			Action:       protocol.ActionGet,
			ResourceType: req.ResourceType,
			ResourceName: req.ResourceName,
			Config:       *providerConfig,
		}

		finalResp, err := executor.Execute(ctx, finalReq)
		if err == nil && finalResp.Resource != nil {
			result.Resource = finalResp.Resource
		}
	}

	return result, nil
}

// waitForCondition polls until a condition is met or timeout
func (d *Dispatcher) waitForCondition(ctx context.Context, provider, resourceType, resourceName string, condition *protocol.WaitCondition) error {
	plugin, err := FindPlugin(provider)
	if err != nil {
		return err
	}

	providerConfig, err := d.config.GetProviderConfig(provider, "")
	if err != nil {
		return err
	}

	executor := NewExecutor(plugin, d.timeout)
	timeout := condition.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("timeout waiting for %s=%s", condition.Field, condition.Value)
			}

			req := &protocol.Request{
				Version:      protocol.ProtocolVersion,
				Action:       protocol.ActionGet,
				ResourceType: resourceType,
				ResourceName: resourceName,
				Config:       *providerConfig,
			}

			resp, err := executor.Execute(ctx, req)
			if err != nil {
				continue // Retry on error
			}

			if resp.Resource == nil {
				continue
			}

			// Check the condition
			if d.checkCondition(resp.Resource, condition) {
				return nil
			}
		}
	}
}

// checkCondition checks if a resource meets a wait condition
func (d *Dispatcher) checkCondition(resource *protocol.Resource, condition *protocol.WaitCondition) bool {
	// Parse field path (e.g., "status.state")
	parts := strings.Split(condition.Field, ".")
	if len(parts) == 0 {
		return false
	}

	var current any
	switch parts[0] {
	case "status":
		current = resource.Status
	case "spec":
		current = resource.Spec
	case "metadata":
		// Handle metadata fields
		switch {
		case len(parts) > 1 && parts[1] == "name":
			return resource.Metadata.Name == condition.Value
		default:
			return false
		}
	default:
		return false
	}

	// Navigate nested fields
	for i := 1; i < len(parts); i++ {
		if m, ok := current.(map[string]any); ok {
			current = m[parts[i]]
		} else {
			return false
		}
	}

	// Compare value
	switch v := current.(type) {
	case string:
		return v == condition.Value
	case bool:
		return fmt.Sprintf("%v", v) == condition.Value
	case int, int64, float64:
		return fmt.Sprintf("%v", v) == condition.Value
	default:
		return false
	}
}

// handleStateUpdate processes a state update from a plugin response
func (d *Dispatcher) handleStateUpdate(update *protocol.StateUpdate) error {
	switch update.Operation {
	case "save":
		if update.State == nil {
			return fmt.Errorf("state data is required for save operation")
		}

		st := &state.State{
			APIVersion: update.State.APIVersion,
			Kind:       update.State.Kind,
			Metadata: state.StateMetadata{
				Name:     update.Name,
				Provider: update.Provider,
			},
			Spec: update.State.Spec,
		}

		if update.State.Status != nil {
			st.Status = state.StateStatus{
				Phase:   update.State.Status.Phase,
				Message: update.State.Status.Message,
				Outputs: update.State.Status.Outputs,
			}
		}

		for _, child := range update.State.Children {
			st.Children = append(st.Children, state.ChildReference{
				Provider: child.Provider,
				Kind:     child.Kind,
				Name:     child.Name,
			})
		}

		return d.store.Save(st)

	case "delete":
		return d.store.Delete(update.Provider, update.Name)

	default:
		return fmt.Errorf("unknown state operation: %s", update.Operation)
	}
}

// Store returns the state store
func (d *Dispatcher) Store() *state.Store {
	return d.store
}
