package compute

import (
	"context"
	"time"
)

// Provider defines the interface for compute providers
type Provider interface {
	// CreateInstance creates a new compute instance
	CreateInstance(ctx context.Context, spec *InstanceSpec) (*Instance, error)

	// DeleteInstance deletes a compute instance
	DeleteInstance(ctx context.Context, id string) error

	// GetInstance retrieves a compute instance by ID or name
	GetInstance(ctx context.Context, id string) (*Instance, error)

	// ListInstances lists instances matching the given filters
	ListInstances(ctx context.Context, filters *Filters) ([]*Instance, error)

	// WaitForReady waits for an instance to reach ready state
	WaitForReady(ctx context.Context, id string, timeout time.Duration) error

	// GetSSHAccess returns SSH connection details for an instance
	GetSSHAccess(ctx context.Context, id string) (*SSHAccess, error)
}

// ProviderInfo contains metadata about a compute provider
type ProviderInfo struct {
	Name     string   `json:"name"`
	Features []string `json:"features"`
}

// Feature constants
const (
	FeatureCloudImage = "cloudImage"
	FeatureCloudInit  = "cloudInit"
	FeatureSSHKeys    = "sshKeys"
	FeatureUserData   = "userData"
	FeatureLabels     = "labels"
)
