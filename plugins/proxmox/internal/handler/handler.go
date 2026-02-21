package handler

import (
	"fmt"
	"time"

	"github.com/openctl/openctl-proxmox/internal/client"
	"github.com/openctl/openctl-proxmox/internal/resources"
	"github.com/openctl/openctl/pkg/protocol"
)

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

	return &protocol.Response{
		Status:   protocol.StatusSuccess,
		Resource: resources.VMToResource(vm, config),
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

	return &protocol.Response{
		Status: protocol.StatusError,
		Error: &protocol.Error{
			Code:    protocol.ErrorCodeInvalidRequest,
			Message: "creating VM without template is not yet supported",
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
