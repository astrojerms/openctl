package protocol

// Resource represents a Kubernetes-style resource with apiVersion, kind, metadata, spec, and status
type Resource struct {
	APIVersion string            `json:"apiVersion" yaml:"apiVersion"`
	Kind       string            `json:"kind" yaml:"kind"`
	Metadata   ResourceMetadata  `json:"metadata" yaml:"metadata"`
	Spec       map[string]any    `json:"spec,omitempty" yaml:"spec,omitempty"`
	Status     map[string]any    `json:"status,omitempty" yaml:"status,omitempty"`
}

// ResourceMetadata contains metadata about a resource
type ResourceMetadata struct {
	Name        string            `json:"name" yaml:"name"`
	Namespace   string            `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	Labels      map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty" yaml:"annotations,omitempty"`
	UID         string            `json:"uid,omitempty" yaml:"uid,omitempty"`
	CreatedAt   string            `json:"createdAt,omitempty" yaml:"createdAt,omitempty"`
}

// ResourceDefinition describes a resource type supported by a plugin
type ResourceDefinition struct {
	Kind    string   `json:"kind"`
	Plural  string   `json:"plural"`
	Actions []string `json:"actions"`
}
