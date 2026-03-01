package handler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/openctl/openctl-k3s/internal/cluster"
	"github.com/openctl/openctl-k3s/internal/resources"
	"github.com/openctl/openctl/pkg/protocol"
)

// Handler handles requests for the K3s plugin
type Handler struct {
	config *protocol.ProviderConfig
}

// New creates a new Handler
func New(config *protocol.ProviderConfig) *Handler {
	return &Handler{
		config: config,
	}
}

// Handle handles a request and returns a response
func (h *Handler) Handle(req *protocol.Request) (*protocol.Response, error) {
	switch req.ResourceType {
	case "Cluster":
		return h.handleCluster(req)
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

func (h *Handler) handleCluster(req *protocol.Request) (*protocol.Response, error) {
	switch req.Action {
	case protocol.ActionList:
		return h.listClusters()
	case protocol.ActionGet:
		return h.getCluster(req.ResourceName)
	case protocol.ActionCreate:
		return h.createCluster(req)
	case protocol.ActionDelete:
		return h.deleteCluster(req)
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

func (h *Handler) listClusters() (*protocol.Response, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	stateDir := filepath.Join(homeDir, ".openctl", "state", "k3s")
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return &protocol.Response{
				Status:    protocol.StatusSuccess,
				Resources: []*protocol.Resource{},
			}, nil
		}
		return nil, err
	}

	var clusters []*protocol.Resource
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}

		name := entry.Name()[:len(entry.Name())-5]
		stateData, err := os.ReadFile(filepath.Join(stateDir, entry.Name()))
		if err != nil {
			continue
		}

		var state map[string]any
		if err := yaml.Unmarshal(stateData, &state); err != nil {
			continue
		}

		status := map[string]any{}
		if s, ok := state["status"].(map[string]any); ok {
			status = s
		}

		clusters = append(clusters, &protocol.Resource{
			APIVersion: "k3s.openctl.io/v1",
			Kind:       "Cluster",
			Metadata: protocol.ResourceMetadata{
				Name: name,
			},
			Status: status,
		})
	}

	return &protocol.Response{
		Status:    protocol.StatusSuccess,
		Resources: clusters,
	}, nil
}

func (h *Handler) getCluster(name string) (*protocol.Response, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	statePath := filepath.Join(homeDir, ".openctl", "state", "k3s", name+".yaml")
	stateData, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return &protocol.Response{
				Status: protocol.StatusError,
				Error: &protocol.Error{
					Code:    protocol.ErrorCodeNotFound,
					Message: fmt.Sprintf("cluster %q not found", name),
				},
			}, nil
		}
		return nil, err
	}

	var state map[string]any
	if err := yaml.Unmarshal(stateData, &state); err != nil {
		return nil, err
	}

	resource := &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       "Cluster",
		Metadata: protocol.ResourceMetadata{
			Name: name,
		},
	}

	if spec, ok := state["spec"].(map[string]any); ok {
		resource.Spec = spec
	}
	if status, ok := state["status"].(map[string]any); ok {
		resource.Status = status
	}

	return &protocol.Response{
		Status:   protocol.StatusSuccess,
		Resource: resource,
	}, nil
}

func (h *Handler) createCluster(req *protocol.Request) (*protocol.Response, error) {
	name := req.Manifest.Metadata.Name
	spec, err := resources.ParseClusterSpec(req.Manifest)
	if err != nil {
		return nil, err
	}

	// Validate spec
	if spec.Compute.Provider == "" {
		return &protocol.Response{
			Status: protocol.StatusError,
			Error: &protocol.Error{
				Code:    protocol.ErrorCodeInvalidRequest,
				Message: "spec.compute.provider is required",
			},
		}, nil
	}

	if spec.Compute.Image.URL == "" && spec.Compute.Image.Template == "" {
		return &protocol.Response{
			Status: protocol.StatusError,
			Error: &protocol.Error{
				Code:    protocol.ErrorCodeInvalidRequest,
				Message: "spec.compute.image.url or spec.compute.image.template is required",
			},
		}, nil
	}

	if spec.Nodes.ControlPlane.Count < 1 {
		spec.Nodes.ControlPlane.Count = 1
	}

	if spec.SSH.User == "" {
		spec.SSH.User = "ubuntu"
	}

	if spec.SSH.PrivateKeyPath == "" {
		return &protocol.Response{
			Status: protocol.StatusError,
			Error: &protocol.Error{
				Code:    protocol.ErrorCodeInvalidRequest,
				Message: "spec.ssh.privateKeyPath is required",
			},
		}, nil
	}

	// Check if this is a continuation (VMs already created or getting IPs)
	// get-ips tokens can have a retry count suffix like "get-ips:5"
	if req.ContinuationToken == "vms-created" || strings.HasPrefix(req.ContinuationToken, "get-ips") {
		return h.handleVMsCreated(req, name, spec)
	}

	// Phase 1: Generate dispatch requests for VM creation
	creator := cluster.NewCreator(name, spec, h.config)
	dispatches := creator.GenerateDispatchRequests()

	return &protocol.Response{
		Status:           protocol.StatusSuccess,
		Message:          fmt.Sprintf("Creating %d VMs for cluster %s...", len(dispatches), name),
		DispatchRequests: dispatches,
		Continuation: &protocol.Continuation{
			Token: "vms-created",
		},
		StateUpdate: &protocol.StateUpdate{
			Operation: "save",
			Provider:  "k3s",
			Name:      name,
			State: &protocol.StateData{
				APIVersion: "k3s.openctl.io/v1",
				Kind:       "Cluster",
				Spec:       req.Manifest.Spec,
				Status: &protocol.StateStatus{
					Phase:   "Creating",
					Message: "Creating VMs",
				},
			},
		},
	}, nil
}

const maxIPRetries = 60 // ~5 minutes of retrying (5 second intervals from dispatcher)

func (h *Handler) handleVMsCreated(req *protocol.Request, name string, spec *resources.ClusterSpec) (*protocol.Response, error) {
	// Parse retry count from continuation token
	retryCount := 0
	if strings.HasPrefix(req.ContinuationToken, "get-ips:") {
		fmt.Sscanf(req.ContinuationToken, "get-ips:%d", &retryCount)
	}

	// Collect node IPs from dispatch results
	nodeIPs := make(map[string]string)
	var children []protocol.ChildReference

	// Get expected node names
	cpNodes, workerNodes := resources.NodeNames(name, spec)
	allNodes := append(cpNodes, workerNodes...)

	for _, result := range req.DispatchResults {
		if result.Status != protocol.StatusSuccess {
			// VM creation failed
			return &protocol.Response{
				Status: protocol.StatusError,
				Error: &protocol.Error{
					Code:    protocol.ErrorCodeInternal,
					Message: fmt.Sprintf("VM creation failed: %v", result.Error),
				},
				StateUpdate: &protocol.StateUpdate{
					Operation: "save",
					Provider:  "k3s",
					Name:      name,
					State: &protocol.StateData{
						APIVersion: "k3s.openctl.io/v1",
						Kind:       "Cluster",
						Status: &protocol.StateStatus{
							Phase:   "Failed",
							Message: fmt.Sprintf("VM creation failed: %v", result.Error),
						},
					},
				},
			}, nil
		}

		// Extract node name from ID (handles both "vm-" and "ip-" prefixes)
		nodeName := extractNodeName(result.ID)
		if nodeName == "" {
			continue
		}

		children = append(children, protocol.ChildReference{
			Provider: spec.Compute.Provider,
			Kind:     "VirtualMachine",
			Name:     nodeName,
		})

		// Get IP from resource status
		if result.Resource != nil && result.Resource.Status != nil {
			if ip, ok := result.Resource.Status["ip"].(string); ok && ip != "" {
				nodeIPs[nodeName] = ip
			}
		}
	}

	// Deduplicate children (in case we're processing results from multiple phases)
	children = deduplicateChildren(children)

	// Check if we have IPs for all nodes
	if len(nodeIPs) < len(allNodes) {
		// Check if we've exceeded max retries
		if retryCount >= maxIPRetries {
			return &protocol.Response{
				Status: protocol.StatusError,
				Error: &protocol.Error{
					Code:    protocol.ErrorCodeInternal,
					Message: fmt.Sprintf("timed out waiting for VM IPs (got %d/%d after %d retries). Ensure QEMU guest agent is running in VMs.", len(nodeIPs), len(allNodes), retryCount),
				},
				StateUpdate: &protocol.StateUpdate{
					Operation: "save",
					Provider:  "k3s",
					Name:      name,
					State: &protocol.StateData{
						APIVersion: "k3s.openctl.io/v1",
						Kind:       "Cluster",
						Status: &protocol.StateStatus{
							Phase:   "Failed",
							Message: fmt.Sprintf("timed out waiting for VM IPs after %d retries", retryCount),
						},
						Children: children,
					},
				},
			}, nil
		}

		// Generate IP fetch dispatches
		ipDispatches := make([]*protocol.DispatchRequest, 0, len(allNodes))
		for _, nodeName := range allNodes {
			ipDispatches = append(ipDispatches, &protocol.DispatchRequest{
				ID:           fmt.Sprintf("ip-%s", nodeName),
				Provider:     spec.Compute.Provider,
				Action:       protocol.ActionGet,
				ResourceType: "VirtualMachine",
				ResourceName: nodeName,
			})
		}

		// Need to wait for IPs - return continuation with incremented retry count
		return &protocol.Response{
			Status:           protocol.StatusSuccess,
			Message:          fmt.Sprintf("Waiting for VM IPs (attempt %d/%d)...", retryCount+1, maxIPRetries),
			DispatchRequests: ipDispatches,
			Continuation: &protocol.Continuation{
				Token: fmt.Sprintf("get-ips:%d", retryCount+1),
			},
			StateUpdate: &protocol.StateUpdate{
				Operation: "save",
				Provider:  "k3s",
				Name:      name,
				State: &protocol.StateData{
					APIVersion: "k3s.openctl.io/v1",
					Kind:       "Cluster",
					Status: &protocol.StateStatus{
						Phase:   "Creating",
						Message: fmt.Sprintf("Waiting for VM IPs (attempt %d)", retryCount+1),
					},
					Children: children,
				},
			},
		}, nil
	}

	// Install K3s
	return h.installK3sOnCluster(name, spec, nodeIPs, children)
}

// extractNodeName extracts the node name from a dispatch result ID
// It handles both "vm-" and "ip-" prefixes
func extractNodeName(id string) string {
	if strings.HasPrefix(id, "vm-") {
		return id[3:]
	}
	if strings.HasPrefix(id, "ip-") {
		return id[3:]
	}
	return id
}

// deduplicateChildren removes duplicate child references
func deduplicateChildren(children []protocol.ChildReference) []protocol.ChildReference {
	seen := make(map[string]bool)
	result := make([]protocol.ChildReference, 0, len(children))
	for _, child := range children {
		key := child.Provider + "/" + child.Kind + "/" + child.Name
		if !seen[key] {
			seen[key] = true
			result = append(result, child)
		}
	}
	return result
}

func (h *Handler) installK3sOnCluster(name string, spec *resources.ClusterSpec, nodeIPs map[string]string, children []protocol.ChildReference) (*protocol.Response, error) {
	creator := cluster.NewCreator(name, spec, h.config)

	result, err := creator.InstallK3s(nodeIPs)
	if err != nil {
		return &protocol.Response{
			Status: protocol.StatusError,
			Error: &protocol.Error{
				Code:    protocol.ErrorCodeInternal,
				Message: fmt.Sprintf("K3s installation failed: %v", err),
			},
			StateUpdate: &protocol.StateUpdate{
				Operation: "save",
				Provider:  "k3s",
				Name:      name,
				State: &protocol.StateData{
					APIVersion: "k3s.openctl.io/v1",
					Kind:       "Cluster",
					Status: &protocol.StateStatus{
						Phase:   "Failed",
						Message: fmt.Sprintf("K3s installation failed: %v", err),
					},
					Children: children,
				},
			},
		}, nil
	}

	outputs := map[string]any{
		"kubeconfigPath": result.KubeconfigPath,
		"serverIP":       result.ServerIP,
	}

	return &protocol.Response{
		Status:  protocol.StatusSuccess,
		Message: fmt.Sprintf("Cluster %s created. Kubeconfig: %s", name, result.KubeconfigPath),
		StateUpdate: &protocol.StateUpdate{
			Operation: "save",
			Provider:  "k3s",
			Name:      name,
			State: &protocol.StateData{
				APIVersion: "k3s.openctl.io/v1",
				Kind:       "Cluster",
				Status: &protocol.StateStatus{
					Phase:   "Ready",
					Message: "Cluster is ready",
					Outputs: outputs,
				},
				Children: children,
			},
		},
	}, nil
}

func (h *Handler) deleteCluster(req *protocol.Request) (*protocol.Response, error) {
	name := req.ResourceName

	// Load existing state to get spec
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	statePath := filepath.Join(homeDir, ".openctl", "state", "k3s", name+".yaml")
	stateData, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return &protocol.Response{
				Status: protocol.StatusError,
				Error: &protocol.Error{
					Code:    protocol.ErrorCodeNotFound,
					Message: fmt.Sprintf("cluster %q not found", name),
				},
			}, nil
		}
		return nil, err
	}

	var state struct {
		Spec     map[string]any `yaml:"spec"`
		Children []struct {
			Provider string `yaml:"provider"`
			Kind     string `yaml:"kind"`
			Name     string `yaml:"name"`
		} `yaml:"children"`
	}
	if err := yaml.Unmarshal(stateData, &state); err != nil {
		return nil, err
	}

	// Parse spec from state
	specResource := &protocol.Resource{
		Spec: state.Spec,
	}
	spec, err := resources.ParseClusterSpec(specResource)
	if err != nil {
		return nil, err
	}

	// Check if this is a continuation (VMs already deleted)
	if req.ContinuationToken == "vms-deleted" {
		return h.handleVMsDeleted(req, name, spec)
	}

	// Generate dispatch requests for VM deletion
	deleter := cluster.NewDeleter(name, spec)
	dispatches := deleter.GenerateDispatchRequests()

	return &protocol.Response{
		Status:           protocol.StatusSuccess,
		Message:          fmt.Sprintf("Deleting %d VMs for cluster %s...", len(dispatches), name),
		DispatchRequests: dispatches,
		Continuation: &protocol.Continuation{
			Token: "vms-deleted",
		},
		StateUpdate: &protocol.StateUpdate{
			Operation: "save",
			Provider:  "k3s",
			Name:      name,
			State: &protocol.StateData{
				APIVersion: "k3s.openctl.io/v1",
				Kind:       "Cluster",
				Status: &protocol.StateStatus{
					Phase:   "Deleting",
					Message: "Deleting VMs",
				},
			},
		},
	}, nil
}

func (h *Handler) handleVMsDeleted(req *protocol.Request, name string, spec *resources.ClusterSpec) (*protocol.Response, error) {
	deleter := cluster.NewDeleter(name, spec)

	// Check for errors (ignoring NOT_FOUND which is expected)
	errors := deleter.ValidateResults(req.DispatchResults)
	if len(errors) > 0 {
		return &protocol.Response{
			Status: protocol.StatusError,
			Error: &protocol.Error{
				Code:    protocol.ErrorCodeInternal,
				Message: fmt.Sprintf("Failed to delete some VMs: %v", errors),
			},
		}, nil
	}

	// Cleanup local files
	if err := deleter.Cleanup(); err != nil {
		// Non-fatal, just log
		fmt.Fprintf(os.Stderr, "Warning: failed to cleanup local files: %v\n", err)
	}

	return &protocol.Response{
		Status:  protocol.StatusSuccess,
		Message: fmt.Sprintf("Cluster %s deleted", name),
		StateUpdate: &protocol.StateUpdate{
			Operation: "delete",
			Provider:  "k3s",
			Name:      name,
		},
	}, nil
}

func init() {
	// Set default timeout
	_ = time.Minute
}
