package compute

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/openctl/openctl-proxmox/internal/client"
	"github.com/openctl/openctl-proxmox/internal/resources"
	"github.com/openctl/openctl/pkg/compute"
	"github.com/openctl/openctl/pkg/protocol"
)

// ProxmoxProvider implements the compute.Provider interface for Proxmox
type ProxmoxProvider struct {
	client *client.Client
	config *protocol.ProviderConfig
}

// NewProvider creates a new Proxmox compute provider
func NewProvider(config *protocol.ProviderConfig) *ProxmoxProvider {
	return &ProxmoxProvider{
		client: client.New(config.Endpoint, config.TokenID, config.TokenSecret),
		config: config,
	}
}

// CreateInstance creates a new compute instance
func (p *ProxmoxProvider) CreateInstance(ctx context.Context, spec *compute.InstanceSpec) (*compute.Instance, error) {
	node := p.config.Node
	if node == "" {
		return nil, fmt.Errorf("node is required in provider config")
	}

	// Build VM spec from compute spec
	vmSpec := &resources.VMSpec{
		Node:          node,
		StartOnCreate: true,
	}

	// Configure size
	if spec.Size.CPUs > 0 {
		vmSpec.CPU = &resources.CPUSpec{
			Cores: spec.Size.CPUs,
		}
	}
	if spec.Size.MemoryMB > 0 {
		vmSpec.Memory = &resources.MemorySpec{
			Size: spec.Size.MemoryMB,
		}
	}

	// Configure disk
	if spec.Size.DiskGB > 0 {
		vmSpec.Disks = []resources.DiskSpec{
			{
				Name: "scsi0",
				Size: fmt.Sprintf("%dG", spec.Size.DiskGB),
			},
		}
	}

	// Configure image source
	if spec.Image.URL != "" {
		// Use cloud image workflow
		storage := "local"
		if s, ok := p.config.Defaults["storage"]; ok {
			storage = s
		}
		vmSpec.CloudImage = &resources.CloudImageRef{
			URL:     spec.Image.URL,
			Storage: storage,
		}
	} else if spec.Image.Template != "" {
		vmSpec.Template = &resources.TemplateRef{
			Name: spec.Image.Template,
		}
	}

	// Configure network
	if spec.Network.Bridge != "" || spec.Network.DHCP {
		bridge := spec.Network.Bridge
		if bridge == "" {
			bridge = "vmbr0"
		}
		vmSpec.Networks = []resources.NetworkSpec{
			{
				Name:   "net0",
				Bridge: bridge,
				Model:  "virtio",
			},
		}

		// Configure cloud-init for IP
		vmSpec.CloudInit = &resources.CloudInitSpec{
			IPConfig: make(map[string]resources.IPConfig),
		}

		if spec.Network.DHCP {
			vmSpec.CloudInit.IPConfig["net0"] = resources.IPConfig{
				IP: "dhcp",
			}
		} else if spec.Network.IP != "" {
			vmSpec.CloudInit.IPConfig["net0"] = resources.IPConfig{
				IP:      spec.Network.IP,
				Gateway: spec.Network.Gateway,
			}
		}
	}

	// Configure SSH keys
	if len(spec.SSHKeys) > 0 {
		if vmSpec.CloudInit == nil {
			vmSpec.CloudInit = &resources.CloudInitSpec{}
		}
		vmSpec.CloudInit.SSHKeys = spec.SSHKeys
	}

	// Enable QEMU agent
	vmSpec.Agent = &resources.AgentSpec{Enabled: true}

	// Determine creation method and execute
	var vmid int
	var err error

	if vmSpec.CloudImage != nil {
		vmid, err = p.createFromCloudImage(node, spec.Name, vmSpec)
	} else if vmSpec.Template != nil {
		vmid, err = p.createFromTemplate(node, spec.Name, vmSpec)
	} else {
		return nil, fmt.Errorf("either image.url or image.template is required")
	}

	if err != nil {
		return nil, err
	}

	// Return instance info
	return &compute.Instance{
		ID:       fmt.Sprintf("%d", vmid),
		Name:     spec.Name,
		State:    compute.StateStarting,
		Provider: "proxmox",
	}, nil
}

// createFromCloudImage creates a VM from a cloud image
func (p *ProxmoxProvider) createFromCloudImage(node, name string, spec *resources.VMSpec) (int, error) {
	storage := spec.CloudImage.Storage

	// Generate template name from URL
	templateName := generateTemplateNameFromURL(spec.CloudImage.URL)

	// Check if template already exists
	existingTemplate, _ := p.client.GetTemplate(templateName)
	var templateVMID int

	if existingTemplate != nil {
		templateVMID = existingTemplate.VMID
	} else {
		// Download and create template
		filename := extractFilenameFromURL(spec.CloudImage.URL)
		upid, err := p.client.DownloadToStorage(node, storage, spec.CloudImage.URL, filename, "iso")
		if err != nil {
			return 0, fmt.Errorf("failed to download cloud image: %w", err)
		}

		if upid != "" {
			if waitErr := p.client.WaitForTask(node, upid, 30*time.Minute); waitErr != nil {
				return 0, fmt.Errorf("download task failed: %w", waitErr)
			}
		}

		// Create base VM for template
		nextID, err := p.client.CreateVM(node, map[string]any{
			"name":   templateName,
			"ostype": "l26",
			"scsihw": "virtio-scsi-pci",
			"boot":   "order=scsi0",
			"agent":  "1",
		})
		if err != nil {
			return 0, fmt.Errorf("failed to create template VM: %w", err)
		}

		// Import disk
		importPath := fmt.Sprintf("%s:import/%s", storage, filename)
		diskConfig := fmt.Sprintf("%s:0,import-from=%s", storage, importPath)
		if err := p.client.ConfigureVM(node, nextID, map[string]any{
			"scsi0": diskConfig,
		}); err != nil {
			_ = p.client.DeleteVM(node, nextID)
			return 0, fmt.Errorf("failed to import disk: %w", err)
		}

		time.Sleep(5 * time.Second)

		// Add cloud-init drive
		if err := p.client.AddCloudInitDrive(node, nextID, storage); err != nil {
			_ = p.client.DeleteVM(node, nextID)
			return 0, fmt.Errorf("failed to add cloud-init drive: %w", err)
		}

		// Convert to template
		if err := p.client.ConvertToTemplate(node, nextID); err != nil {
			_ = p.client.DeleteVM(node, nextID)
			return 0, fmt.Errorf("failed to convert to template: %w", err)
		}

		templateVMID = nextID
	}

	// Clone from template
	cloneParams := map[string]any{
		"full":    1,
		"storage": storage,
	}

	vmid, upid, err := p.client.CloneVM(node, templateVMID, name, cloneParams)
	if err != nil {
		return 0, fmt.Errorf("failed to clone template: %w", err)
	}

	if upid != "" {
		if err := p.client.WaitForTask(node, upid, 10*time.Minute); err != nil {
			return 0, fmt.Errorf("clone task failed: %w", err)
		}
	}

	// Apply configuration
	configParams := spec.ToProxmoxConfig()
	if len(configParams) > 0 {
		if err := p.client.ConfigureVM(node, vmid, configParams); err != nil {
			return 0, fmt.Errorf("failed to configure VM: %w", err)
		}
	}

	// Resize disk if specified
	for _, disk := range spec.Disks {
		if disk.Size != "" && disk.Name != "" {
			if err := p.client.ResizeVMDisk(node, vmid, disk.Name, disk.Size); err != nil {
				return 0, fmt.Errorf("failed to resize disk: %w", err)
			}
		}
	}

	// Regenerate cloud-init
	if spec.CloudInit != nil {
		_ = p.client.RegenerateCloudInit(node, vmid)
	}

	// Start VM
	if spec.StartOnCreate {
		if _, err := p.client.StartVM(node, vmid); err != nil {
			return 0, fmt.Errorf("failed to start VM: %w", err)
		}
	}

	return vmid, nil
}

// createFromTemplate creates a VM from an existing template
func (p *ProxmoxProvider) createFromTemplate(node, name string, spec *resources.VMSpec) (int, error) {
	templateID := spec.Template.VMID
	templateNode := node

	if templateID == 0 && spec.Template.Name != "" {
		tmpl, err := p.client.GetTemplate(spec.Template.Name)
		if err != nil {
			return 0, fmt.Errorf("template not found: %s", spec.Template.Name)
		}
		templateID = tmpl.VMID
		templateNode = tmpl.Node
	}

	cloneParams := map[string]any{}
	if node != templateNode {
		cloneParams["target"] = node
	}

	vmid, upid, err := p.client.CloneVM(templateNode, templateID, name, cloneParams)
	if err != nil {
		return 0, err
	}

	if upid != "" {
		if err := p.client.WaitForTask(templateNode, upid, 5*time.Minute); err != nil {
			return 0, fmt.Errorf("clone task failed: %w", err)
		}
	}

	configParams := spec.ToProxmoxConfig()
	if len(configParams) > 0 {
		if err := p.client.ConfigureVM(node, vmid, configParams); err != nil {
			return 0, fmt.Errorf("failed to configure VM: %w", err)
		}
	}

	for _, disk := range spec.Disks {
		if disk.Size != "" {
			if err := p.client.ResizeVMDisk(node, vmid, disk.Name, disk.Size); err != nil {
				return 0, fmt.Errorf("failed to resize disk: %w", err)
			}
		}
	}

	if spec.StartOnCreate {
		if _, err := p.client.StartVM(node, vmid); err != nil {
			return 0, fmt.Errorf("failed to start VM: %w", err)
		}
	}

	return vmid, nil
}

// DeleteInstance deletes a compute instance
func (p *ProxmoxProvider) DeleteInstance(ctx context.Context, id string) error {
	vm, err := p.client.GetVM(id)
	if err != nil {
		return err
	}

	if vm.Status == "running" {
		if _, err := p.client.StopVM(vm.Node, vm.VMID); err != nil {
			return fmt.Errorf("failed to stop VM: %w", err)
		}
		time.Sleep(5 * time.Second)
	}

	return p.client.DeleteVM(vm.Node, vm.VMID)
}

// GetInstance retrieves a compute instance
func (p *ProxmoxProvider) GetInstance(ctx context.Context, id string) (*compute.Instance, error) {
	vm, err := p.client.GetVM(id)
	if err != nil {
		return nil, err
	}

	return p.vmToInstance(vm), nil
}

// ListInstances lists instances matching filters
func (p *ProxmoxProvider) ListInstances(ctx context.Context, filters *compute.Filters) ([]*compute.Instance, error) {
	vms, err := p.client.ListVMs()
	if err != nil {
		return nil, err
	}

	var instances []*compute.Instance
	for _, vm := range vms {
		if vm.Template == 1 {
			continue
		}

		instance := p.vmToInstance(vm)

		// Apply filters
		if filters != nil {
			if len(filters.Names) > 0 && !contains(filters.Names, vm.Name) {
				continue
			}
			if len(filters.States) > 0 && !containsState(filters.States, instance.State) {
				continue
			}
		}

		instances = append(instances, instance)
	}

	return instances, nil
}

// WaitForReady waits for an instance to be ready
func (p *ProxmoxProvider) WaitForReady(ctx context.Context, id string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return fmt.Errorf("timeout waiting for instance %s to be ready", id)
			}

			instance, err := p.GetInstance(ctx, id)
			if err != nil {
				continue
			}

			if instance.State == compute.StateRunning {
				// Wait a bit more for networking
				time.Sleep(10 * time.Second)
				return nil
			}
		}
	}
}

// GetSSHAccess returns SSH connection details
func (p *ProxmoxProvider) GetSSHAccess(ctx context.Context, id string) (*compute.SSHAccess, error) {
	vm, err := p.client.GetVM(id)
	if err != nil {
		return nil, err
	}

	// Get IP from QEMU agent
	ip, err := p.client.GetVMIPAddress(vm.Node, vm.VMID)
	if err != nil {
		return nil, fmt.Errorf("failed to get VM IP: %w", err)
	}

	return &compute.SSHAccess{
		Host: ip,
		Port: 22,
		User: "ubuntu", // Default for cloud images, should be configurable
	}, nil
}

// vmToInstance converts a Proxmox VM to a compute Instance
func (p *ProxmoxProvider) vmToInstance(vm *client.VM) *compute.Instance {
	state := compute.StateUnknown
	switch vm.Status {
	case "running":
		state = compute.StateRunning
	case "stopped":
		state = compute.StateStopped
	}

	return &compute.Instance{
		ID:       fmt.Sprintf("%d", vm.VMID),
		Name:     vm.Name,
		State:    state,
		Provider: "proxmox",
	}
}

// generateTemplateNameFromURL creates a template name from a cloud image URL
func generateTemplateNameFromURL(url string) string {
	filename := extractFilenameFromURL(url)
	name := strings.TrimSuffix(filename, ".img")
	name = strings.TrimSuffix(name, ".qcow2")
	name = strings.TrimSuffix(name, ".raw")
	name = strings.ReplaceAll(name, "_", "-")
	return "tpl-" + name
}

// extractFilenameFromURL extracts the filename from a URL
func extractFilenameFromURL(urlStr string) string {
	parts := strings.Split(urlStr, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return "cloud-image.img"
}

func contains(slice []string, s string) bool {
	return slices.Contains(slice, s)
}

func containsState(slice []compute.InstanceState, s compute.InstanceState) bool {
	return slices.Contains(slice, s)
}
