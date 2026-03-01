package compute

// InstanceSpec specifies the desired configuration for a compute instance
type InstanceSpec struct {
	Name     string            `json:"name"`
	Image    ImageSpec         `json:"image"`
	Size     SizeSpec          `json:"size"`
	Network  NetworkSpec       `json:"network,omitempty"`
	SSHKeys  []string          `json:"sshKeys,omitempty"`
	UserData string            `json:"userData,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
}

// ImageSpec specifies the image to use for an instance
type ImageSpec struct {
	URL      string `json:"url,omitempty"`      // Cloud image URL
	Template string `json:"template,omitempty"` // Template name/ID
}

// SizeSpec specifies the size of an instance
type SizeSpec struct {
	CPUs     int `json:"cpus"`
	MemoryMB int `json:"memoryMB"`
	DiskGB   int `json:"diskGB"`
}

// NetworkSpec specifies network configuration
type NetworkSpec struct {
	Bridge string `json:"bridge,omitempty"`
	VLAN   int    `json:"vlan,omitempty"`
	DHCP   bool   `json:"dhcp"`
	IP     string `json:"ip,omitempty"`
	Gateway string `json:"gateway,omitempty"`
}

// Instance represents a running compute instance
type Instance struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	State     InstanceState `json:"state"`
	PrivateIP string        `json:"privateIP,omitempty"`
	PublicIP  string        `json:"publicIP,omitempty"`
	Provider  string        `json:"provider"`
}

// InstanceState represents the state of an instance
type InstanceState string

const (
	StateCreating InstanceState = "creating"
	StateStarting InstanceState = "starting"
	StateRunning  InstanceState = "running"
	StateStopped  InstanceState = "stopped"
	StateFailed   InstanceState = "failed"
	StateUnknown  InstanceState = "unknown"
)

// SSHAccess contains SSH connection details for an instance
type SSHAccess struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	PrivateKey string `json:"privateKey,omitempty"` // Path to private key
}

// Filters for listing instances
type Filters struct {
	Labels map[string]string `json:"labels,omitempty"`
	Names  []string          `json:"names,omitempty"`
	States []InstanceState   `json:"states,omitempty"`
}
