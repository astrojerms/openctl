package protocol

// Request represents a request sent from openctl to a plugin
type Request struct {
	Version      string         `json:"version"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resourceType"`
	ResourceName string         `json:"resourceName,omitempty"`
	Manifest     *Resource      `json:"manifest,omitempty"`
	Args         map[string]any `json:"args,omitempty"`
	Config       ProviderConfig `json:"config"`

	// DispatchResults contains results from previous dispatch operations
	DispatchResults []*DispatchResult `json:"dispatchResults,omitempty"`

	// ContinuationToken is provided when resuming after dispatch
	ContinuationToken string `json:"continuationToken,omitempty"`
}

// ProviderConfig contains the configuration passed to a plugin
type ProviderConfig struct {
	Endpoint    string            `json:"endpoint,omitempty"`
	Node        string            `json:"node,omitempty"`
	TokenID     string            `json:"tokenId,omitempty"`
	TokenSecret string            `json:"tokenSecret,omitempty"`
	Defaults    map[string]string `json:"defaults,omitempty"`
	SnippetSSH  *SnippetSSHConfig `json:"snippetSSH,omitempty"`
}

// SnippetSSHConfig configures SSH uploads to explicitly mapped Proxmox nodes.
type SnippetSSHConfig struct {
	Hosts        map[string]string `json:"hosts" yaml:"hosts"`
	User         string            `json:"user,omitempty" yaml:"user,omitempty"`
	IdentityFile string            `json:"identityFile,omitempty" yaml:"identityFile,omitempty"`
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
