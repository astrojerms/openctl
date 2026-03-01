package resources

import (
	"fmt"

	"github.com/openctl/openctl/pkg/protocol"
)

// ClusterSpec represents the specification for a K3s cluster
type ClusterSpec struct {
	Compute ComputeSpec `json:"compute"`
	Nodes   NodesSpec   `json:"nodes"`
	K3s     K3sSpec     `json:"k3s"`
	SSH     SSHSpec     `json:"ssh"`
}

// ComputeSpec defines the compute provider configuration
type ComputeSpec struct {
	Provider string            `json:"provider"` // e.g., "proxmox"
	Context  string            `json:"context,omitempty"`
	Image    ImageSpec         `json:"image"`
	Default  DefaultSizeSpec   `json:"default"`
}

// ImageSpec defines the VM image to use
type ImageSpec struct {
	URL      string `json:"url,omitempty"`      // Cloud image URL
	Template string `json:"template,omitempty"` // Template name
}

// DefaultSizeSpec defines default VM sizing
type DefaultSizeSpec struct {
	CPUs     int `json:"cpus"`
	MemoryMB int `json:"memoryMB"`
	DiskGB   int `json:"diskGB"`
}

// NodesSpec defines the cluster nodes
type NodesSpec struct {
	ControlPlane ControlPlaneSpec `json:"controlPlane"`
	Workers      []WorkerSpec     `json:"workers"`
}

// ControlPlaneSpec defines control plane nodes
type ControlPlaneSpec struct {
	Count int          `json:"count"`
	Size  *DefaultSizeSpec `json:"size,omitempty"`
}

// WorkerSpec defines a worker node pool
type WorkerSpec struct {
	Name  string           `json:"name"`
	Count int              `json:"count"`
	Size  *DefaultSizeSpec `json:"size,omitempty"`
}

// K3sSpec defines K3s configuration
type K3sSpec struct {
	Version     string   `json:"version,omitempty"` // e.g., "v1.29.0+k3s1"
	ClusterCIDR string   `json:"clusterCIDR,omitempty"`
	ServiceCIDR string   `json:"serviceCIDR,omitempty"`
	ExtraArgs   []string `json:"extraArgs,omitempty"`
}

// SSHSpec defines SSH configuration
type SSHSpec struct {
	User           string   `json:"user"`           // SSH user (e.g., "ubuntu")
	PrivateKeyPath string   `json:"privateKeyPath"` // Path to SSH private key
	PublicKeys     []string `json:"publicKeys"`     // Public keys to inject via cloud-init
}

// ParseClusterSpec parses a cluster spec from a protocol Resource
func ParseClusterSpec(r *protocol.Resource) (*ClusterSpec, error) {
	spec := &ClusterSpec{}

	if r.Spec == nil {
		return spec, nil
	}

	// Parse compute section
	if compute, ok := r.Spec["compute"].(map[string]any); ok {
		if provider, ok := compute["provider"].(string); ok {
			spec.Compute.Provider = provider
		}
		if context, ok := compute["context"].(string); ok {
			spec.Compute.Context = context
		}
		if image, ok := compute["image"].(map[string]any); ok {
			if url, ok := image["url"].(string); ok {
				spec.Compute.Image.URL = url
			}
			if template, ok := image["template"].(string); ok {
				spec.Compute.Image.Template = template
			}
		}
		if def, ok := compute["default"].(map[string]any); ok {
			if cpus, ok := def["cpus"].(float64); ok {
				spec.Compute.Default.CPUs = int(cpus)
			}
			if mem, ok := def["memoryMB"].(float64); ok {
				spec.Compute.Default.MemoryMB = int(mem)
			}
			if disk, ok := def["diskGB"].(float64); ok {
				spec.Compute.Default.DiskGB = int(disk)
			}
		}
	}

	// Parse nodes section
	if nodes, ok := r.Spec["nodes"].(map[string]any); ok {
		if cp, ok := nodes["controlPlane"].(map[string]any); ok {
			if count, ok := cp["count"].(float64); ok {
				spec.Nodes.ControlPlane.Count = int(count)
			}
			if size, ok := cp["size"].(map[string]any); ok {
				spec.Nodes.ControlPlane.Size = parseSizeSpec(size)
			}
		}
		if workers, ok := nodes["workers"].([]any); ok {
			for _, w := range workers {
				if worker, ok := w.(map[string]any); ok {
					ws := WorkerSpec{}
					if name, ok := worker["name"].(string); ok {
						ws.Name = name
					}
					if count, ok := worker["count"].(float64); ok {
						ws.Count = int(count)
					}
					if size, ok := worker["size"].(map[string]any); ok {
						ws.Size = parseSizeSpec(size)
					}
					spec.Nodes.Workers = append(spec.Nodes.Workers, ws)
				}
			}
		}
	}

	// Parse k3s section
	if k3s, ok := r.Spec["k3s"].(map[string]any); ok {
		if version, ok := k3s["version"].(string); ok {
			spec.K3s.Version = version
		}
		if clusterCIDR, ok := k3s["clusterCIDR"].(string); ok {
			spec.K3s.ClusterCIDR = clusterCIDR
		}
		if serviceCIDR, ok := k3s["serviceCIDR"].(string); ok {
			spec.K3s.ServiceCIDR = serviceCIDR
		}
		if extraArgs, ok := k3s["extraArgs"].([]any); ok {
			for _, arg := range extraArgs {
				if s, ok := arg.(string); ok {
					spec.K3s.ExtraArgs = append(spec.K3s.ExtraArgs, s)
				}
			}
		}
	}

	// Parse ssh section
	if ssh, ok := r.Spec["ssh"].(map[string]any); ok {
		if user, ok := ssh["user"].(string); ok {
			spec.SSH.User = user
		}
		if keyPath, ok := ssh["privateKeyPath"].(string); ok {
			spec.SSH.PrivateKeyPath = keyPath
		}
		if pubKeys, ok := ssh["publicKeys"].([]any); ok {
			for _, k := range pubKeys {
				if key, ok := k.(string); ok {
					spec.SSH.PublicKeys = append(spec.SSH.PublicKeys, key)
				}
			}
		}
	}

	return spec, nil
}

func parseSizeSpec(m map[string]any) *DefaultSizeSpec {
	size := &DefaultSizeSpec{}
	if cpus, ok := m["cpus"].(float64); ok {
		size.CPUs = int(cpus)
	}
	if mem, ok := m["memoryMB"].(float64); ok {
		size.MemoryMB = int(mem)
	}
	if disk, ok := m["diskGB"].(float64); ok {
		size.DiskGB = int(disk)
	}
	return size
}

// ClusterToResource converts cluster state to a protocol Resource
func ClusterToResource(name string, spec *ClusterSpec, phase string, outputs map[string]any, children []protocol.ChildReference) *protocol.Resource {
	specMap := map[string]any{
		"compute": map[string]any{
			"provider": spec.Compute.Provider,
			"image": map[string]any{
				"url":      spec.Compute.Image.URL,
				"template": spec.Compute.Image.Template,
			},
			"default": map[string]any{
				"cpus":     spec.Compute.Default.CPUs,
				"memoryMB": spec.Compute.Default.MemoryMB,
				"diskGB":   spec.Compute.Default.DiskGB,
			},
		},
		"nodes": map[string]any{
			"controlPlane": map[string]any{
				"count": spec.Nodes.ControlPlane.Count,
			},
		},
		"k3s": map[string]any{
			"version": spec.K3s.Version,
		},
		"ssh": map[string]any{
			"user": spec.SSH.User,
		},
	}

	status := map[string]any{
		"phase": phase,
	}
	for k, v := range outputs {
		status[k] = v
	}

	return &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       "Cluster",
		Metadata: protocol.ResourceMetadata{
			Name: name,
		},
		Spec:   specMap,
		Status: status,
	}
}

// NodeNames generates node names for a cluster
func NodeNames(clusterName string, spec *ClusterSpec) (controlPlanes []string, workers []string) {
	for i := 0; i < spec.Nodes.ControlPlane.Count; i++ {
		controlPlanes = append(controlPlanes, fmt.Sprintf("%s-cp-%d", clusterName, i))
	}

	for _, workerPool := range spec.Nodes.Workers {
		poolName := workerPool.Name
		if poolName == "" {
			poolName = "worker"
		}
		for i := 0; i < workerPool.Count; i++ {
			workers = append(workers, fmt.Sprintf("%s-%s-%d", clusterName, poolName, i))
		}
	}

	return
}
