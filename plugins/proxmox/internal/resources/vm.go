package resources

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/openctl/openctl-proxmox/internal/client"
	"github.com/openctl/openctl/pkg/protocol"
)

// VMToResource converts a Proxmox VM to a protocol Resource
func VMToResource(vm *client.VM, config *client.VMConfig) *protocol.Resource {
	return VMToResourceWithIP(vm, config, "")
}

// VMToResourceWithIP converts a Proxmox VM to a protocol Resource with an optional IP address
func VMToResourceWithIP(vm *client.VM, config *client.VMConfig, ip string) *protocol.Resource {
	spec := map[string]any{
		"node": vm.Node,
	}

	if config != nil {
		spec["cpu"] = map[string]any{
			"cores":   config.Cores,
			"sockets": config.Sockets,
		}
		spec["memory"] = map[string]any{
			"size": config.Memory,
		}
	} else {
		spec["cpu"] = map[string]any{
			"cores": vm.CPUs,
		}
		spec["memory"] = map[string]any{
			"size": vm.MaxMem / 1024 / 1024,
		}
	}

	status := map[string]any{
		"vmid":    vm.VMID,
		"state":   vm.Status,
		"uptime":  vm.Uptime,
		"cpuUsed": vm.CPU,
		"memUsed": vm.Mem,
	}

	if ip != "" {
		status["ip"] = ip
	}

	return &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata: protocol.ResourceMetadata{
			Name: vm.Name,
		},
		Spec:   spec,
		Status: status,
	}
}

// TemplateToResource converts a Proxmox Template to a protocol Resource
func TemplateToResource(t *client.Template) *protocol.Resource {
	return &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "Template",
		Metadata: protocol.ResourceMetadata{
			Name: t.Name,
		},
		Spec: map[string]any{
			"node": t.Node,
			"vmid": t.VMID,
		},
		Status: map[string]any{
			"state": t.Status,
		},
	}
}

// VMSpec represents the spec for a VirtualMachine
type VMSpec struct {
	Node          string         `json:"node"`
	Template      *TemplateRef   `json:"template"`
	Image         *ImageRef      `json:"image"`
	CloudImage    *CloudImageRef `json:"cloudImage"`
	CPU           *CPUSpec       `json:"cpu"`
	Memory        *MemorySpec    `json:"memory"`
	Disks         []DiskSpec     `json:"disks"`
	Networks      []NetworkSpec  `json:"networks"`
	CloudInit     *CloudInitSpec `json:"cloudInit"`
	StartOnCreate bool           `json:"startOnCreate"`
	OSType        string         `json:"osType"`
	BIOS          string         `json:"bios"`
	Machine       string         `json:"machine"`
	Agent         *AgentSpec     `json:"agent"`
}

// TemplateRef references a template
type TemplateRef struct {
	Name string `json:"name"`
	VMID int    `json:"vmid"`
}

// ImageRef references a disk image in storage (legacy - use CloudImageRef instead)
type ImageRef struct {
	// Storage is the Proxmox storage ID where the image is located (e.g., "local", "nfs-images")
	Storage string `json:"storage"`
	// File is the filename of the image (e.g., "ubuntu-22.04-cloudimg-amd64.img")
	// Can also be a full volume ID (e.g., "local:import/image.img")
	File string `json:"file"`
	// ContentType is the storage content type where the image is located.
	// Common values: "iso", "images", "import". Defaults to "images".
	ContentType string `json:"contentType"`
	// Format is the disk format (qcow2, raw, vmdk). If empty, auto-detected from extension.
	Format string `json:"format"`
	// TargetStorage is where to store the imported disk. If empty, uses Storage.
	TargetStorage string `json:"targetStorage"`
	// TargetFormat is the format for the imported disk. If empty, uses Format or defaults to raw for block storage.
	TargetFormat string `json:"targetFormat"`
}

// CloudImageRef references a cloud image to download and use
type CloudImageRef struct {
	// URL is the download URL for the cloud image (e.g., https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img)
	URL string `json:"url"`
	// Storage is where to store the downloaded image and create the template
	Storage string `json:"storage"`
	// Checksum is the expected checksum of the image (optional, format: "sha256:abc123..." or "sha512:...")
	Checksum string `json:"checksum"`
	// TemplateName is the name for the template VM. If empty, auto-generated from URL.
	TemplateName string `json:"templateName"`
	// DiskStorage is where to store the VM disk. If empty, uses Storage.
	DiskStorage string `json:"diskStorage"`
}

// AgentSpec configures the QEMU guest agent
type AgentSpec struct {
	Enabled bool `json:"enabled"`
}

// CPUSpec defines CPU configuration
type CPUSpec struct {
	Cores   int `json:"cores"`
	Sockets int `json:"sockets"`
}

// MemorySpec defines memory configuration
type MemorySpec struct {
	Size int `json:"size"`
}

// DiskSpec defines disk configuration
type DiskSpec struct {
	Name    string `json:"name"`
	Storage string `json:"storage"`
	Size    string `json:"size"`
}

// NetworkSpec defines network configuration
type NetworkSpec struct {
	Name   string `json:"name"`
	Bridge string `json:"bridge"`
	Model  string `json:"model"`
}

// CloudInitSpec defines cloud-init configuration
type CloudInitSpec struct {
	User     string              `json:"user"`
	Password string              `json:"password"`
	SSHKeys  []string            `json:"sshKeys"`
	IPConfig map[string]IPConfig `json:"ipConfig"`
}

// IPConfig defines IP configuration
type IPConfig struct {
	IP      string `json:"ip"`
	Gateway string `json:"gateway"`
}

// ParseVMSpec parses the spec from a Resource into a VMSpec
func ParseVMSpec(r *protocol.Resource) (*VMSpec, error) {
	spec := &VMSpec{}

	if r.Spec == nil {
		return spec, nil
	}

	if node, ok := r.Spec["node"].(string); ok {
		spec.Node = node
	}

	if startOnCreate, ok := r.Spec["startOnCreate"].(bool); ok {
		spec.StartOnCreate = startOnCreate
	}

	if tmpl, ok := r.Spec["template"].(map[string]any); ok {
		spec.Template = &TemplateRef{}
		if name, ok := tmpl["name"].(string); ok {
			spec.Template.Name = name
		}
		if vmid, ok := tmpl["vmid"].(float64); ok {
			spec.Template.VMID = int(vmid)
		}
	}

	if img, ok := r.Spec["image"].(map[string]any); ok {
		spec.Image = &ImageRef{}
		if storage, ok := img["storage"].(string); ok {
			spec.Image.Storage = storage
		}
		if file, ok := img["file"].(string); ok {
			spec.Image.File = file
		}
		if contentType, ok := img["contentType"].(string); ok {
			spec.Image.ContentType = contentType
		}
		if format, ok := img["format"].(string); ok {
			spec.Image.Format = format
		}
		if targetStorage, ok := img["targetStorage"].(string); ok {
			spec.Image.TargetStorage = targetStorage
		}
		if targetFormat, ok := img["targetFormat"].(string); ok {
			spec.Image.TargetFormat = targetFormat
		}
	}

	if ci, ok := r.Spec["cloudImage"].(map[string]any); ok {
		spec.CloudImage = &CloudImageRef{}
		if url, ok := ci["url"].(string); ok {
			spec.CloudImage.URL = url
		}
		if storage, ok := ci["storage"].(string); ok {
			spec.CloudImage.Storage = storage
		}
		if checksum, ok := ci["checksum"].(string); ok {
			spec.CloudImage.Checksum = checksum
		}
		if templateName, ok := ci["templateName"].(string); ok {
			spec.CloudImage.TemplateName = templateName
		}
		if diskStorage, ok := ci["diskStorage"].(string); ok {
			spec.CloudImage.DiskStorage = diskStorage
		}
	}

	if osType, ok := r.Spec["osType"].(string); ok {
		spec.OSType = osType
	}
	if bios, ok := r.Spec["bios"].(string); ok {
		spec.BIOS = bios
	}
	if machine, ok := r.Spec["machine"].(string); ok {
		spec.Machine = machine
	}

	if agent, ok := r.Spec["agent"].(map[string]any); ok {
		spec.Agent = &AgentSpec{}
		if enabled, ok := agent["enabled"].(bool); ok {
			spec.Agent.Enabled = enabled
		}
	}

	if cpu, ok := r.Spec["cpu"].(map[string]any); ok {
		spec.CPU = &CPUSpec{}
		if cores, ok := cpu["cores"].(float64); ok {
			spec.CPU.Cores = int(cores)
		}
		if sockets, ok := cpu["sockets"].(float64); ok {
			spec.CPU.Sockets = int(sockets)
		}
	}

	if mem, ok := r.Spec["memory"].(map[string]any); ok {
		spec.Memory = &MemorySpec{}
		if size, ok := mem["size"].(float64); ok {
			spec.Memory.Size = int(size)
		}
	}

	if disks, ok := r.Spec["disks"].([]any); ok {
		for _, d := range disks {
			if disk, ok := d.(map[string]any); ok {
				diskSpec := DiskSpec{}
				if name, ok := disk["name"].(string); ok {
					diskSpec.Name = name
				}
				if storage, ok := disk["storage"].(string); ok {
					diskSpec.Storage = storage
				}
				if size, ok := disk["size"].(string); ok {
					diskSpec.Size = size
				}
				spec.Disks = append(spec.Disks, diskSpec)
			}
		}
	}

	if networks, ok := r.Spec["networks"].([]any); ok {
		for _, n := range networks {
			if net, ok := n.(map[string]any); ok {
				netSpec := NetworkSpec{}
				if name, ok := net["name"].(string); ok {
					netSpec.Name = name
				}
				if bridge, ok := net["bridge"].(string); ok {
					netSpec.Bridge = bridge
				}
				if model, ok := net["model"].(string); ok {
					netSpec.Model = model
				}
				spec.Networks = append(spec.Networks, netSpec)
			}
		}
	}

	if ci, ok := r.Spec["cloudInit"].(map[string]any); ok {
		spec.CloudInit = &CloudInitSpec{
			IPConfig: make(map[string]IPConfig),
		}
		if user, ok := ci["user"].(string); ok {
			spec.CloudInit.User = user
		}
		if password, ok := ci["password"].(string); ok {
			spec.CloudInit.Password = password
		}
		if sshKeys, ok := ci["sshKeys"].([]any); ok {
			for _, k := range sshKeys {
				if key, ok := k.(string); ok {
					spec.CloudInit.SSHKeys = append(spec.CloudInit.SSHKeys, key)
				}
			}
		}
		if ipConfig, ok := ci["ipConfig"].(map[string]any); ok {
			for iface, cfg := range ipConfig {
				if ipCfg, ok := cfg.(map[string]any); ok {
					ic := IPConfig{}
					if ip, ok := ipCfg["ip"].(string); ok {
						ic.IP = ip
					}
					if gw, ok := ipCfg["gateway"].(string); ok {
						ic.Gateway = gw
					}
					spec.CloudInit.IPConfig[iface] = ic
				}
			}
		}
	}

	return spec, nil
}

// ToProxmoxConfig converts a VMSpec to Proxmox configuration parameters
func (s *VMSpec) ToProxmoxConfig() map[string]any {
	params := make(map[string]any)

	if s.CPU != nil {
		if s.CPU.Cores > 0 {
			params["cores"] = s.CPU.Cores
		}
		if s.CPU.Sockets > 0 {
			params["sockets"] = s.CPU.Sockets
		}
	}

	if s.Memory != nil && s.Memory.Size > 0 {
		params["memory"] = s.Memory.Size
	}

	if s.OSType != "" {
		params["ostype"] = s.OSType
	}
	if s.BIOS != "" {
		params["bios"] = s.BIOS
	}
	if s.Machine != "" {
		params["machine"] = s.Machine
	}
	if s.Agent != nil && s.Agent.Enabled {
		params["agent"] = "1"
	}

	for _, net := range s.Networks {
		model := net.Model
		if model == "" {
			model = "virtio"
		}
		params[net.Name] = fmt.Sprintf("%s,bridge=%s", model, net.Bridge)
	}

	if s.CloudInit != nil {
		if s.CloudInit.User != "" {
			params["ciuser"] = s.CloudInit.User
		}
		if s.CloudInit.Password != "" {
			params["cipassword"] = s.CloudInit.Password
		}
		if len(s.CloudInit.SSHKeys) > 0 {
			// Proxmox expects sshkeys to be URL-encoded with %20 for spaces (not +).
			// QueryEscape encodes everything properly but uses + for spaces,
			// so we replace + with %20 after encoding.
			encoded := url.QueryEscape(strings.Join(s.CloudInit.SSHKeys, "\n"))
			encoded = strings.ReplaceAll(encoded, "+", "%20")
			params["sshkeys"] = encoded
		}

		for iface, cfg := range s.CloudInit.IPConfig {
			key := fmt.Sprintf("ipconfig%s", strings.TrimPrefix(iface, "net"))
			if cfg.IP == "dhcp" {
				params[key] = "ip=dhcp"
			} else if cfg.IP != "" {
				val := fmt.Sprintf("ip=%s", cfg.IP)
				if cfg.Gateway != "" {
					val += fmt.Sprintf(",gw=%s", cfg.Gateway)
				}
				params[key] = val
			}
		}
	}

	return params
}
