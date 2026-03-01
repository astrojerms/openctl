package state

import "time"

// State represents the persistent state of a resource
type State struct {
	APIVersion string           `yaml:"apiVersion" json:"apiVersion"`
	Kind       string           `yaml:"kind" json:"kind"`
	Metadata   StateMetadata    `yaml:"metadata" json:"metadata"`
	Spec       map[string]any   `yaml:"spec,omitempty" json:"spec,omitempty"`
	Status     StateStatus      `yaml:"status" json:"status"`
	Children   []ChildReference `yaml:"children,omitempty" json:"children,omitempty"`
}

// StateMetadata contains metadata about the state
type StateMetadata struct {
	Name      string    `yaml:"name" json:"name"`
	Provider  string    `yaml:"provider" json:"provider"`
	CreatedAt time.Time `yaml:"createdAt" json:"createdAt"`
	UpdatedAt time.Time `yaml:"updatedAt" json:"updatedAt"`
}

// StateStatus represents the current status of a resource
type StateStatus struct {
	Phase   string         `yaml:"phase" json:"phase"`                     // Pending, Creating, Ready, Failed, Deleting
	Message string         `yaml:"message,omitempty" json:"message,omitempty"`
	Outputs map[string]any `yaml:"outputs,omitempty" json:"outputs,omitempty"` // e.g., kubeconfig path
}

// ChildReference references a child resource managed by this state
type ChildReference struct {
	Provider string `yaml:"provider" json:"provider"` // e.g., "proxmox"
	Kind     string `yaml:"kind" json:"kind"`         // e.g., "VirtualMachine"
	Name     string `yaml:"name" json:"name"`
}

// Phase constants
const (
	PhasePending  = "Pending"
	PhaseCreating = "Creating"
	PhaseReady    = "Ready"
	PhaseFailed   = "Failed"
	PhaseDeleting = "Deleting"
)
