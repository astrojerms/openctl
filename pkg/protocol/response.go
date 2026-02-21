package protocol

// Response represents a response from a plugin to openctl
type Response struct {
	Status    string      `json:"status"`
	Resource  *Resource   `json:"resource,omitempty"`
	Resources []*Resource `json:"resources,omitempty"`
	Message   string      `json:"message,omitempty"`
	Error     *Error      `json:"error,omitempty"`
}

// Error represents an error response
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// Capabilities represents the capabilities response from a plugin
type Capabilities struct {
	ProviderName    string               `json:"providerName"`
	ProtocolVersion string               `json:"protocolVersion"`
	Resources       []ResourceDefinition `json:"resources"`
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
