package resources

import (
	"fmt"
	"maps"

	"github.com/openctl/openctl/pkg/protocol"
)

// ClusterSpec represents the specification for a K3s cluster
type ClusterSpec struct {
	Compute ComputeSpec `json:"compute"`
	Nodes   NodesSpec   `json:"nodes"`
	Network NetworkSpec `json:"network"`
	K3s     K3sSpec     `json:"k3s"`
	SSH     SSHSpec     `json:"ssh"`
}

// NetworkSpec defines network configuration for the cluster
type NetworkSpec struct {
	// Bridge is the network bridge to use (e.g., "vmbr0")
	Bridge string `json:"bridge,omitempty"`
	// DHCP indicates whether to use DHCP for IP allocation (default: true)
	DHCP bool `json:"dhcp"`
	// StaticIPs provides static IP configuration
	StaticIPs *StaticIPSpec `json:"staticIPs,omitempty"`
}

// StaticIPSpec defines static IP allocation
type StaticIPSpec struct {
	// StartIP is the first IP to allocate (e.g., "192.168.1.100")
	StartIP string `json:"startIP"`
	// Gateway is the default gateway
	Gateway string `json:"gateway"`
	// Netmask in CIDR notation (e.g., "24")
	Netmask string `json:"netmask"`
}

// ComputeSpec defines the compute provider configuration
type ComputeSpec struct {
	Provider string          `json:"provider"` // e.g., "proxmox"
	Context  string          `json:"context,omitempty"`
	Image    ImageSpec       `json:"image"`
	Default  DefaultSizeSpec `json:"default"`
}

// ImageSpec defines the VM image to use
type ImageSpec struct {
	URL         string `json:"url,omitempty"`         // Cloud image URL
	Template    string `json:"template,omitempty"`    // Template name
	Storage     string `json:"storage,omitempty"`     // Storage for downloading image and creating template
	DiskStorage string `json:"diskStorage,omitempty"` // Storage for VM disks (defaults to Storage)
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
	Count int              `json:"count"`
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
			if storage, ok := image["storage"].(string); ok {
				spec.Compute.Image.Storage = storage
			}
			if diskStorage, ok := image["diskStorage"].(string); ok {
				spec.Compute.Image.DiskStorage = diskStorage
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

	// Parse network section
	spec.Network.DHCP = true      // Default to DHCP
	spec.Network.Bridge = "vmbr0" // Default bridge
	if network, ok := r.Spec["network"].(map[string]any); ok {
		if bridge, ok := network["bridge"].(string); ok {
			spec.Network.Bridge = bridge
		}
		// Handle dhcp field - might be bool or string depending on how YAML was parsed
		if dhcp, ok := network["dhcp"].(bool); ok {
			spec.Network.DHCP = dhcp
		} else if dhcpStr, ok := network["dhcp"].(string); ok {
			spec.Network.DHCP = dhcpStr != "false"
		}
		if staticIPs, ok := network["staticIPs"].(map[string]any); ok {
			spec.Network.StaticIPs = &StaticIPSpec{}
			if startIP, ok := staticIPs["startIP"].(string); ok {
				spec.Network.StaticIPs.StartIP = startIP
			}
			if gateway, ok := staticIPs["gateway"].(string); ok {
				spec.Network.StaticIPs.Gateway = gateway
			}
			if netmask, ok := staticIPs["netmask"].(string); ok {
				spec.Network.StaticIPs.Netmask = netmask
			}
			// If staticIPs is provided and DHCP wasn't explicitly set, disable DHCP
			if spec.Network.StaticIPs.StartIP != "" {
				spec.Network.DHCP = false
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
	maps.Copy(status, outputs)

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

// AllocateIPs generates IP allocations for all nodes in the cluster
// Returns a map of node name -> IP address
func AllocateIPs(clusterName string, spec *ClusterSpec) (map[string]string, error) {
	if spec.Network.DHCP || spec.Network.StaticIPs == nil {
		return nil, nil // DHCP mode, no pre-allocated IPs
	}

	cpNodes, workerNodes := NodeNames(clusterName, spec)
	allNodes := append(cpNodes, workerNodes...)

	ips := make(map[string]string)
	currentIP := spec.Network.StaticIPs.StartIP

	for _, nodeName := range allNodes {
		ips[nodeName] = currentIP
		var err error
		currentIP, err = incrementIP(currentIP)
		if err != nil {
			return nil, fmt.Errorf("failed to allocate IP for %s: %w", nodeName, err)
		}
	}

	return ips, nil
}

// incrementIP returns the next IP address
func incrementIP(ip string) (string, error) {
	parts := make([]int, 4)
	_, err := fmt.Sscanf(ip, "%d.%d.%d.%d", &parts[0], &parts[1], &parts[2], &parts[3])
	if err != nil {
		return "", fmt.Errorf("invalid IP address: %s", ip)
	}

	// Increment last octet
	parts[3]++
	if parts[3] > 254 {
		parts[3] = 1
		parts[2]++
		if parts[2] > 255 {
			return "", fmt.Errorf("IP range exhausted")
		}
	}

	return fmt.Sprintf("%d.%d.%d.%d", parts[0], parts[1], parts[2], parts[3]), nil
}
