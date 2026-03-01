package protocol

// Response represents a response from a plugin to openctl
type Response struct {
	Status    string      `json:"status"`
	Resource  *Resource   `json:"resource,omitempty"`
	Resources []*Resource `json:"resources,omitempty"`
	Message   string      `json:"message,omitempty"`
	Error     *Error      `json:"error,omitempty"`

	// StateUpdate contains state changes to persist
	StateUpdate *StateUpdate `json:"stateUpdate,omitempty"`

	// DispatchRequests contains requests to dispatch to other plugins
	DispatchRequests []*DispatchRequest `json:"dispatchRequests,omitempty"`

	// Continuation indicates the plugin expects to be called again with results
	Continuation *Continuation `json:"continuation,omitempty"`
}

// StateUpdate represents a state change to persist
type StateUpdate struct {
	Operation string                 `json:"operation"` // save, delete
	Provider  string                 `json:"provider"`
	Name      string                 `json:"name"`
	State     *StateData             `json:"state,omitempty"`
}

// StateData represents the state data to persist
type StateData struct {
	APIVersion string                 `json:"apiVersion"`
	Kind       string                 `json:"kind"`
	Spec       map[string]any         `json:"spec,omitempty"`
	Status     *StateStatus           `json:"status"`
	Children   []ChildReference       `json:"children,omitempty"`
}

// StateStatus represents the status portion of state
type StateStatus struct {
	Phase   string         `json:"phase"`
	Message string         `json:"message,omitempty"`
	Outputs map[string]any `json:"outputs,omitempty"`
}

// ChildReference references a child resource
type ChildReference struct {
	Provider string `json:"provider"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
}

// Continuation indicates the plugin should be called again
type Continuation struct {
	Token string `json:"token"` // Opaque token for plugin to resume
}

// Error represents an error response
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// Capabilities represents the capabilities response from a plugin
type Capabilities struct {
	ProviderName      string               `json:"providerName"`
	ProtocolVersion   string               `json:"protocolVersion"`
	Resources         []ResourceDefinition `json:"resources"`
	ComputeProvider   *ComputeCapability   `json:"computeProvider,omitempty"`
	SupportsDispatch  bool                 `json:"supportsDispatch,omitempty"`
}

// ComputeCapability describes compute provider capabilities
type ComputeCapability struct {
	Implements string   `json:"implements"` // e.g., "compute.openctl.io/v1"
	Features   []string `json:"features"`   // e.g., ["cloudImage", "cloudInit", "sshKeys"]
}

// Status constants
const (
	StatusSuccess = "success"
	StatusError   = "error"
)

// Error codes
const (
	ErrorCodeNotFound       = "NOT_FOUND"
	ErrorCodeAlreadyExists  = "ALREADY_EXISTS"
	ErrorCodeInvalidRequest = "INVALID_REQUEST"
	ErrorCodeUnauthorized   = "UNAUTHORIZED"
	ErrorCodeInternal       = "INTERNAL"
)
