package handler

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openctl/openctl-proxmox/internal/client"
	"github.com/openctl/openctl-proxmox/internal/resources"
	"github.com/openctl/openctl/pkg/protocol"
)

var debugEnabled = os.Getenv("OPENCTL_DEBUG") != ""

func debugf(format string, args ...any) {
	if debugEnabled {
		fmt.Fprintf(os.Stderr, "[proxmox-handler] "+format+"\n", args...)
	}
}

// Handler handles requests for the Proxmox plugin
type Handler struct {
	config *protocol.ProviderConfig
	client *client.Client
}

// New creates a new Handler
func New(config *protocol.ProviderConfig) *Handler {
	return &Handler{
		config: config,
		client: client.New(config.Endpoint, config.TokenID, config.TokenSecret),
	}
}

// Handle handles a request and returns a response
func (h *Handler) Handle(req *protocol.Request) (*protocol.Response, error) {
	switch req.ResourceType {
	case "VirtualMachine":
		return h.handleVM(req)
	case "Template":
		return h.handleTemplate(req)
	default:
		return &protocol.Response{
			Status: protocol.StatusError,
			Error: &protocol.Error{
				Code:    protocol.ErrorCodeInvalidRequest,
				Message: fmt.Sprintf("unknown resource type: %s", req.ResourceType),
			},
		}, nil
	}
}

func (h *Handler) handleVM(req *protocol.Request) (*protocol.Response, error) {
	switch req.Action {
	case protocol.ActionList:
		return h.listVMs()
	case protocol.ActionGet:
		return h.getVM(req.ResourceName)
	case protocol.ActionCreate:
		return h.createVM(req.Manifest)
	case protocol.ActionDelete:
		return h.deleteVM(req.ResourceName)
	case protocol.ActionApply:
		return h.applyVM(req.Manifest)
	default:
		return &protocol.Response{
			Status: protocol.StatusError,
			Error: &protocol.Error{
				Code:    protocol.ErrorCodeInvalidRequest,
				Message: fmt.Sprintf("unknown action: %s", req.Action),
			},
		}, nil
	}
}

func (h *Handler) handleTemplate(req *protocol.Request) (*protocol.Response, error) {
	switch req.Action {
	case protocol.ActionList:
		return h.listTemplates()
	case protocol.ActionGet:
		return h.getTemplate(req.ResourceName)
	default:
		return &protocol.Response{
			Status: protocol.StatusError,
			Error: &protocol.Error{
				Code:    protocol.ErrorCodeInvalidRequest,
				Message: fmt.Sprintf("action %s not supported for templates", req.Action),
			},
		}, nil
	}
}

func (h *Handler) listVMs() (*protocol.Response, error) {
	vms, err := h.client.ListVMs()
	if err != nil {
		return nil, err
	}

	var result []*protocol.Resource
	for _, vm := range vms {
		if vm.Template == 1 {
			continue
		}
		result = append(result, resources.VMToResource(vm, nil))
	}

	return &protocol.Response{
		Status:    protocol.StatusSuccess,
		Resources: result,
	}, nil
}

func (h *Handler) getVM(name string) (*protocol.Response, error) {
	vm, err := h.client.GetVM(name)
	if err != nil {
		return &protocol.Response{
			Status: protocol.StatusError,
			Error: &protocol.Error{
				Code:    protocol.ErrorCodeNotFound,
				Message: err.Error(),
			},
		}, nil
	}

	config, _ := h.client.GetVMConfig(vm.Node, vm.VMID)

	// Try to get IP if VM is running (non-blocking)
	var ip string
	if vm.Status == "running" {
		ip, _ = h.client.GetVMIPAddress(vm.Node, vm.VMID)
	}

	return &protocol.Response{
		Status:   protocol.StatusSuccess,
		Resource: resources.VMToResourceWithIP(vm, config, ip),
	}, nil
}

func (h *Handler) createVM(manifest *protocol.Resource) (*protocol.Response, error) {
	spec, err := resources.ParseVMSpec(manifest)
	if err != nil {
		return nil, err
	}

	node := spec.Node
	if node == "" {
		node = h.config.Node
	}
	if node == "" {
		return &protocol.Response{
			Status: protocol.StatusError,
			Error: &protocol.Error{
				Code:    protocol.ErrorCodeInvalidRequest,
				Message: "node is required (set in spec.node or config)",
			},
		}, nil
	}

	if spec.Template != nil {
		return h.createVMFromTemplate(manifest.Metadata.Name, node, spec)
	}

	if spec.CloudImage != nil {
		return h.createVMFromCloudImage(manifest.Metadata.Name, node, spec)
	}

	if spec.Image != nil {
		return h.createVMFromImage(manifest.Metadata.Name, node, spec)
	}

	return &protocol.Response{
		Status: protocol.StatusError,
		Error: &protocol.Error{
			Code:    protocol.ErrorCodeInvalidRequest,
			Message: "one of spec.template, spec.cloudImage, or spec.image is required",
		},
	}, nil
}

func (h *Handler) createVMFromTemplate(name, node string, spec *resources.VMSpec) (*protocol.Response, error) {
	templateID := spec.Template.VMID
	templateNode := node

	if templateID == 0 && spec.Template.Name != "" {
		tmpl, err := h.client.GetTemplate(spec.Template.Name)
		if err != nil {
			return &protocol.Response{
				Status: protocol.StatusError,
				Error: &protocol.Error{
					Code:    protocol.ErrorCodeNotFound,
					Message: fmt.Sprintf("template not found: %s", spec.Template.Name),
				},
			}, nil
		}
		templateID = tmpl.VMID
		templateNode = tmpl.Node
	}

	cloneParams := map[string]any{}
	if node != templateNode {
		cloneParams["target"] = node
	}

	vmid, upid, err := h.client.CloneVM(templateNode, templateID, name, cloneParams)
	if err != nil {
		return nil, err
	}

	if upid != "" {
		if err := h.client.WaitForTask(templateNode, upid, 5*time.Minute); err != nil {
			return nil, fmt.Errorf("clone task failed: %w", err)
		}
	}

	configParams := spec.ToProxmoxConfig()
	if len(configParams) > 0 {
		if err := h.client.ConfigureVM(node, vmid, configParams); err != nil {
			return nil, fmt.Errorf("failed to configure VM: %w", err)
		}
	}

	for _, disk := range spec.Disks {
		if disk.Size != "" {
			if err := h.client.ResizeVMDisk(node, vmid, disk.Name, disk.Size); err != nil {
				return nil, fmt.Errorf("failed to resize disk %s: %w", disk.Name, err)
			}
		}
	}

	// If cloud-init is configured, try to enable qemu-guest-agent via cicustom vendor data
	if spec.CloudInit != nil {
		// Use config storage if available, otherwise try "local"
		snippetStorage := "local"
		if h.config.Defaults != nil {
			if s, ok := h.config.Defaults["storage"]; ok && s != "" {
				snippetStorage = s
			}
		}
		if err := h.client.EnsureQemuAgentSnippet(node, snippetStorage); err != nil {
			debugf("createVMFromTemplate: failed to ensure snippet: %v", err)
		} else {
			cicustom := fmt.Sprintf("vendor=%s:snippets/%s", snippetStorage, client.QemuAgentSnippetName)
			debugf("createVMFromTemplate: setting cicustom=%s", cicustom)
			if err := h.client.ConfigureVM(node, vmid, map[string]any{"cicustom": cicustom}); err != nil {
				debugf("createVMFromTemplate: failed to set cicustom: %v", err)
			}
		}
	}

	if spec.StartOnCreate {
		if _, err := h.client.StartVM(node, vmid); err != nil {
			return nil, fmt.Errorf("failed to start VM: %w", err)
		}
	}

	return &protocol.Response{
		Status:  protocol.StatusSuccess,
		Message: fmt.Sprintf("VM %s created (vmid: %d)", name, vmid),
	}, nil
}

func (h *Handler) createVMFromCloudImage(name, node string, spec *resources.VMSpec) (*protocol.Response, error) {
	if spec.CloudImage.URL == "" {
		return &protocol.Response{
			Status: protocol.StatusError,
			Error: &protocol.Error{
				Code:    protocol.ErrorCodeInvalidRequest,
				Message: "cloudImage.url is required",
			},
		}, nil
	}
	if spec.CloudImage.Storage == "" {
		return &protocol.Response{
			Status: protocol.StatusError,
			Error: &protocol.Error{
				Code:    protocol.ErrorCodeInvalidRequest,
				Message: "cloudImage.storage is required",
			},
		}, nil
	}

	// Determine template name from URL or explicit setting
	templateName := spec.CloudImage.TemplateName
	if templateName == "" {
		// Generate template name from URL (e.g., "ubuntu-jammy-cloudimg")
		templateName = generateTemplateNameFromURL(spec.CloudImage.URL)
	}

	// Check if template already exists
	existingTemplate, _ := h.client.GetTemplate(templateName)
	var templateVMID int

	if existingTemplate != nil {
		// Template exists, use it
		templateVMID = existingTemplate.VMID
	} else {
		// Template doesn't exist, create it
		var err error
		templateVMID, err = h.createCloudImageTemplate(node, templateName, spec)
		if err != nil {
			return nil, fmt.Errorf("failed to create cloud image template: %w", err)
		}
	}

	// Clone the template to create the new VM
	diskStorage := spec.CloudImage.DiskStorage
	if diskStorage == "" {
		diskStorage = spec.CloudImage.Storage
	}

	cloneParams := map[string]any{
		"full":    1,
		"storage": diskStorage,
	}

	vmid, upid, err := h.client.CloneVM(node, templateVMID, name, cloneParams)
	if err != nil {
		return nil, fmt.Errorf("failed to clone template: %w", err)
	}

	// Wait for clone to complete
	if upid != "" {
		if err := h.client.WaitForTask(node, upid, 10*time.Minute); err != nil {
			return nil, fmt.Errorf("clone task failed: %w", err)
		}
	}

	// Apply VM configuration (CPU, memory, etc.)
	configParams := spec.ToProxmoxConfig()
	if len(configParams) > 0 {
		if err := h.client.ConfigureVM(node, vmid, configParams); err != nil {
			return nil, fmt.Errorf("failed to configure VM: %w", err)
		}
	}

	// Resize disks if specified
	for _, disk := range spec.Disks {
		if disk.Size != "" && disk.Name != "" {
			if err := h.client.ResizeVMDisk(node, vmid, disk.Name, disk.Size); err != nil {
				return nil, fmt.Errorf("failed to resize disk %s: %w", disk.Name, err)
			}
		}
	}

	// Enable qemu-guest-agent via cicustom vendor data (for IP detection)
	// Using vendor= instead of user= so it merges with cloud-init config instead of replacing it
	snippetStorage := spec.CloudImage.Storage
	if err := h.client.EnsureQemuAgentSnippet(node, snippetStorage); err != nil {
		// Try to continue - storage might not support snippets
		debugf("createVMFromCloudImage: failed to ensure snippet: %v", err)
	} else {
		// Add cicustom vendor data to VM config
		cicustom := fmt.Sprintf("vendor=%s:snippets/%s", snippetStorage, client.QemuAgentSnippetName)
		debugf("createVMFromCloudImage: setting cicustom=%s", cicustom)
		if err := h.client.ConfigureVM(node, vmid, map[string]any{"cicustom": cicustom}); err != nil {
			debugf("createVMFromCloudImage: failed to set cicustom: %v", err)
		}
	}

	// Regenerate cloud-init with new settings (error ignored - non-fatal)
	if spec.CloudInit != nil {
		_ = h.client.RegenerateCloudInit(node, vmid)
	}

	// Start VM if requested
	if spec.StartOnCreate {
		if _, err := h.client.StartVM(node, vmid); err != nil {
			return nil, fmt.Errorf("failed to start VM: %w", err)
		}
	}

	return &protocol.Response{
		Status:  protocol.StatusSuccess,
		Message: fmt.Sprintf("VM %s created from cloud image (vmid: %d, template: %s)", name, vmid, templateName),
	}, nil
}

// createCloudImageTemplate downloads a cloud image and creates a template from it
func (h *Handler) createCloudImageTemplate(node, templateName string, spec *resources.VMSpec) (int, error) {
	storage := spec.CloudImage.Storage

	// Step 1: Download the cloud image to storage
	// Use "import" content type so the image can be imported into VMs
	// (import-from requires source to be "images" or "import" type)
	filename := extractFilenameFromURL(spec.CloudImage.URL)
	// Proxmox import content type only accepts .qcow2 or .raw extensions
	// Cloud images often use .img extension but are qcow2 format internally
	filename = normalizeImageExtension(filename)
	upid, err := h.client.DownloadToStorage(node, storage, spec.CloudImage.URL, filename, "import")
	if err != nil {
		return 0, fmt.Errorf("failed to download cloud image: %w", err)
	}

	// Wait for download to complete
	if upid != "" {
		if waitErr := h.client.WaitForTask(node, upid, 30*time.Minute); waitErr != nil {
			return 0, fmt.Errorf("download task failed: %w", waitErr)
		}
	}

	// Step 2: Create base VM
	nextID, err := h.client.CreateVM(node, map[string]any{
		"name":   templateName,
		"ostype": "l26",
		"scsihw": "virtio-scsi-pci",
		"boot":   "order=scsi0",
		"agent":  "1",
	})
	if err != nil {
		return 0, fmt.Errorf("failed to create template VM: %w", err)
	}

	// Step 3: Import the disk to the VM
	// The image was downloaded to the "import" content directory
	importPath := fmt.Sprintf("%s:import/%s", storage, filename)
	diskStorage := spec.CloudImage.DiskStorage
	if diskStorage == "" {
		diskStorage = storage
	}

	diskConfig := fmt.Sprintf("%s:0,import-from=%s", diskStorage, importPath)
	if err := h.client.ConfigureVM(node, nextID, map[string]any{
		"scsi0": diskConfig,
	}); err != nil {
		_ = h.client.DeleteVM(node, nextID)
		return 0, fmt.Errorf("failed to import disk: %w", err)
	}

	// Wait a moment for disk import
	time.Sleep(5 * time.Second)

	// Step 4: Add cloud-init drive
	if err := h.client.AddCloudInitDrive(node, nextID, diskStorage); err != nil {
		_ = h.client.DeleteVM(node, nextID)
		return 0, fmt.Errorf("failed to add cloud-init drive: %w", err)
	}

	// Step 5: Convert to template
	if err := h.client.ConvertToTemplate(node, nextID); err != nil {
		_ = h.client.DeleteVM(node, nextID)
		return 0, fmt.Errorf("failed to convert to template: %w", err)
	}

	return nextID, nil
}

// generateTemplateNameFromURL creates a template name from a cloud image URL
func generateTemplateNameFromURL(url string) string {
	// Extract filename from URL
	filename := extractFilenameFromURL(url)

	// Remove extension
	name := strings.TrimSuffix(filename, ".img")
	name = strings.TrimSuffix(name, ".qcow2")
	name = strings.TrimSuffix(name, ".raw")

	// Clean up the name
	name = strings.ReplaceAll(name, "_", "-")

	// Add template prefix
	return "tpl-" + name
}

// extractFilenameFromURL extracts the filename from a URL
func extractFilenameFromURL(urlStr string) string {
	// Parse the URL
	parts := strings.Split(urlStr, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return "cloud-image.img"
}

// normalizeImageExtension ensures the filename has a valid extension for Proxmox import
// The Proxmox import content type only accepts .qcow2 or .raw extensions
// Cloud images often use .img extension but are qcow2 format internally
func normalizeImageExtension(filename string) string {
	// Already has a valid import extension
	if strings.HasSuffix(filename, ".qcow2") || strings.HasSuffix(filename, ".raw") {
		return filename
	}
	// Convert .img to .qcow2 (most cloud images with .img are qcow2 format)
	if before, ok := strings.CutSuffix(filename, ".img"); ok {
		return before + ".qcow2"
	}
	// For other extensions, append .qcow2
	return filename + ".qcow2"
}

func (h *Handler) createVMFromImage(name, node string, spec *resources.VMSpec) (*protocol.Response, error) {
	if spec.Image.Storage == "" {
		return &protocol.Response{
			Status: protocol.StatusError,
			Error: &protocol.Error{
				Code:    protocol.ErrorCodeInvalidRequest,
				Message: "image.storage is required",
			},
		}, nil
	}
	if spec.Image.File == "" {
		return &protocol.Response{
			Status: protocol.StatusError,
			Error: &protocol.Error{
				Code:    protocol.ErrorCodeInvalidRequest,
				Message: "image.file is required",
			},
		}, nil
	}

	// Check if the source storage supports importing (unless using absolute path or full volume ID)
	if !strings.HasPrefix(spec.Image.File, "/") && !strings.Contains(spec.Image.File, ":") {
		storageInfo, err := h.client.GetStorageInfo(spec.Image.Storage)
		if err == nil && storageInfo != nil {
			// Check if storage supports images content type
			if !strings.Contains(storageInfo.Content, "images") && !strings.Contains(storageInfo.Content, "import") {
				return &protocol.Response{
					Status: protocol.StatusError,
					Error: &protocol.Error{
						Code:    protocol.ErrorCodeInvalidRequest,
						Message: fmt.Sprintf("storage '%s' does not support disk images (content types: %s). To import from this storage, add 'Disk image' content type in Proxmox UI: Datacenter > Storage > %s > Edit > Content", spec.Image.Storage, storageInfo.Content, spec.Image.Storage),
					},
				}, nil
			}
		}
	}

	// Build VM creation parameters from spec
	configParams := spec.ToProxmoxConfig()

	// Set sensible defaults for cloud images if not specified
	if _, hasOSType := configParams["ostype"]; !hasOSType {
		configParams["ostype"] = "l26" // Linux 2.6+ kernel
	}

	// Create VM from image
	vmid, upid, err := h.client.CreateVMFromImage(
		node,
		name,
		spec.Image.Storage,
		spec.Image.File,
		spec.Image.ContentType,
		spec.Image.TargetStorage,
		configParams,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create VM from image: %w", err)
	}

	// Wait for the import task to complete if there's a task ID
	if upid != "" {
		if err := h.client.WaitForTask(node, upid, 10*time.Minute); err != nil {
			return nil, fmt.Errorf("import task failed: %w", err)
		}
	}

	// Apply additional disk configuration (resize if specified)
	for _, disk := range spec.Disks {
		if disk.Size != "" && disk.Name != "" {
			if err := h.client.ResizeVMDisk(node, vmid, disk.Name, disk.Size); err != nil {
				return nil, fmt.Errorf("failed to resize disk %s: %w", disk.Name, err)
			}
		}
	}

	// Start VM if requested
	if spec.StartOnCreate {
		if _, err := h.client.StartVM(node, vmid); err != nil {
			return nil, fmt.Errorf("failed to start VM: %w", err)
		}
	}

	return &protocol.Response{
		Status:  protocol.StatusSuccess,
		Message: fmt.Sprintf("VM %s created from image %s (vmid: %d)", name, spec.Image.File, vmid),
	}, nil
}

func (h *Handler) deleteVM(name string) (*protocol.Response, error) {
	vm, err := h.client.GetVM(name)
	if err != nil {
		return &protocol.Response{
			Status: protocol.StatusError,
			Error: &protocol.Error{
				Code:    protocol.ErrorCodeNotFound,
				Message: err.Error(),
			},
		}, nil
	}

	if vm.Status == "running" {
		if _, err := h.client.StopVM(vm.Node, vm.VMID); err != nil {
			return nil, fmt.Errorf("failed to stop VM: %w", err)
		}
		time.Sleep(5 * time.Second)
	}

	if err := h.client.DeleteVM(vm.Node, vm.VMID); err != nil {
		return nil, err
	}

	return &protocol.Response{
		Status:  protocol.StatusSuccess,
		Message: fmt.Sprintf("VM %s deleted", name),
	}, nil
}

func (h *Handler) applyVM(manifest *protocol.Resource) (*protocol.Response, error) {
	vm, err := h.client.GetVM(manifest.Metadata.Name)
	if err != nil {
		return h.createVM(manifest)
	}

	spec, err := resources.ParseVMSpec(manifest)
	if err != nil {
		return nil, err
	}

	configParams := spec.ToProxmoxConfig()
	if len(configParams) > 0 {
		if err := h.client.ConfigureVM(vm.Node, vm.VMID, configParams); err != nil {
			return nil, fmt.Errorf("failed to configure VM: %w", err)
		}
	}

	for _, disk := range spec.Disks {
		if disk.Size != "" {
			if err := h.client.ResizeVMDisk(vm.Node, vm.VMID, disk.Name, disk.Size); err != nil {
				return nil, fmt.Errorf("failed to resize disk %s: %w", disk.Name, err)
			}
		}
	}

	return &protocol.Response{
		Status:  protocol.StatusSuccess,
		Message: fmt.Sprintf("VM %s updated", manifest.Metadata.Name),
	}, nil
}

func (h *Handler) listTemplates() (*protocol.Response, error) {
	templates, err := h.client.ListTemplates()
	if err != nil {
		return nil, err
	}

	var result []*protocol.Resource
	for _, t := range templates {
		result = append(result, resources.TemplateToResource(t))
	}

	return &protocol.Response{
		Status:    protocol.StatusSuccess,
		Resources: result,
	}, nil
}

func (h *Handler) getTemplate(name string) (*protocol.Response, error) {
	tmpl, err := h.client.GetTemplate(name)
	if err != nil {
		return &protocol.Response{
			Status: protocol.StatusError,
			Error: &protocol.Error{
				Code:    protocol.ErrorCodeNotFound,
				Message: err.Error(),
			},
		}, nil
	}

	return &protocol.Response{
		Status:   protocol.StatusSuccess,
		Resource: resources.TemplateToResource(tmpl),
	}, nil
}
