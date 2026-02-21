package protocol

// Request represents a request sent from openctl to a plugin
type Request struct {
	Version      string         `json:"version"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resourceType"`
	ResourceName string         `json:"resourceName,omitempty"`
	Manifest     *Resource      `json:"manifest,omitempty"`
	Config       ProviderConfig `json:"config"`
}

// ProviderConfig contains the configuration passed to a plugin
type ProviderConfig struct {
	Endpoint    string            `json:"endpoint,omitempty"`
	Node        string            `json:"node,omitempty"`
	TokenID     string            `json:"tokenId,omitempty"`
	TokenSecret string            `json:"tokenSecret,omitempty"`
	Defaults    map[string]string `json:"defaults,omitempty"`
}

// Action constants
const (
	ActionGet    = "get"
	ActionList   = "list"
	ActionCreate = "create"
	ActionDelete = "delete"
	ActionApply  = "apply"
)

// ProtocolVersion is the current protocol version
const ProtocolVersion = "1.0"
