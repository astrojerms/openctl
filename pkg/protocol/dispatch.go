package protocol

import "time"

// DispatchRequest represents a request to dispatch to another plugin
type DispatchRequest struct {
	ID           string         `json:"id"`                     // Unique ID for correlation
	Provider     string         `json:"provider"`               // Target plugin
	Action       string         `json:"action"`                 // get, list, create, delete, apply
	ResourceType string         `json:"resourceType"`           // e.g., VirtualMachine
	ResourceName string         `json:"resourceName,omitempty"` // For get/delete operations
	Manifest     *Resource      `json:"manifest,omitempty"`     // For create/apply operations
	WaitFor      *WaitCondition `json:"waitFor,omitempty"`      // Optional wait condition
}

// WaitCondition specifies a condition to wait for after dispatch
type WaitCondition struct {
	Field   string        `json:"field"`   // e.g., "status.state"
	Value   string        `json:"value"`   // e.g., "running"
	Timeout time.Duration `json:"timeout"` // Maximum wait time
}

// DispatchResult represents the result of a dispatch operation
type DispatchResult struct {
	ID       string    `json:"id"`                 // Correlation ID from request
	Status   string    `json:"status"`             // success, error
	Resource *Resource `json:"resource,omitempty"` // Result resource if applicable
	Error    *Error    `json:"error,omitempty"`    // Error details if failed
}
