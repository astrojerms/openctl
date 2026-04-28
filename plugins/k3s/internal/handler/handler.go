package handler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/openctl/openctl-k3s/internal/agent"
	agentclient "github.com/openctl/openctl-k3s/internal/agent/client"
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

		resource := &protocol.Resource{
			APIVersion: "k3s.openctl.io/v1",
			Kind:       "Cluster",
			Metadata: protocol.ResourceMetadata{
				Name: name,
			},
			Status: status,
		}
		augmentLiveStatus(resource, status)
		clusters = append(clusters, resource)
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
	augmentLiveStatus(resource, resource.Status)

	return &protocol.Response{
		Status:   protocol.StatusSuccess,
		Resource: resource,
	}, nil
}

// augmentLiveStatus probes each node's agent and folds per-node health into
// the resource's Status. Side-effect-free with respect to disk: never mutates
// the saved state file. If the saved state has no agent block (e.g. older
// cluster created before the agent shipped), it's a no-op.
func augmentLiveStatus(resource *protocol.Resource, status map[string]any) {
	endpoints, opts, ok := extractAgentProbeConfig(status)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	results, err := agentclient.ProbeNodes(ctx, opts, endpoints)
	if err != nil {
		// Couldn't load cert material — surface as a single warning, no node detail.
		setStatusField(resource, "health", "unknown")
		setStatusField(resource, "healthMessage", "agent probe failed: "+err.Error())
		return
	}

	nodes := make([]map[string]any, 0, len(results))
	healthy := 0
	var skews []string
	for _, r := range results {
		entry := map[string]any{
			"name":      r.Name,
			"ip":        r.IP,
			"reachable": r.Reachable,
		}
		if r.Reachable {
			healthy++
			entry["k3sStatus"] = r.Info.K3sStatus
			entry["agentVersion"] = r.Info.AgentVersion
			entry["arch"] = r.Info.Arch
			entry["init"] = r.Info.Init
			entry["distro"] = r.Info.Distro
			entry["kernel"] = r.Info.Kernel
			entry["capabilities"] = r.Info.Capabilities
			if r.Info.AgentVersion != agent.Version {
				skews = append(skews, fmt.Sprintf("%s (%s)", r.Name, r.Info.AgentVersion))
			}
		} else {
			entry["error"] = r.Error
		}
		nodes = append(nodes, entry)
	}

	health := "healthy"
	switch {
	case healthy == 0:
		health = "down"
	case healthy < len(results):
		health = "degraded"
	}

	setStatusField(resource, "nodes", nodes)
	setStatusField(resource, "health", health)
	setStatusField(resource, "healthMessage", fmt.Sprintf("%d/%d nodes reachable", healthy, len(results)))

	if len(skews) > 0 {
		setStatusField(resource, "agentVersionSkew", skews)
		setStatusField(resource, "expectedAgentVersion", agent.Version)
		fmt.Fprintf(os.Stderr,
			"warning: agent version skew on %d node(s) (plugin expects %q): %s\n",
			len(skews), agent.Version, strings.Join(skews, ", "))
	}
}

// extractAgentProbeConfig pulls the agent block out of a saved status map and
// returns the inputs ProbeNodes needs. Returns ok=false if the cluster
// doesn't have an agent block (pre-agent clusters, or partial create).
func extractAgentProbeConfig(status map[string]any) (map[string]string, agentclient.ProbeOptions, bool) {
	outputs, ok := status["outputs"].(map[string]any)
	if !ok {
		return nil, agentclient.ProbeOptions{}, false
	}
	agentBlock, ok := outputs["agent"].(map[string]any)
	if !ok {
		return nil, agentclient.ProbeOptions{}, false
	}

	opts := agentclient.ProbeOptions{
		CAPath:         stringField(agentBlock, "caPath"),
		ClientCertPath: stringField(agentBlock, "clientCertPath"),
		ClientKeyPath:  stringField(agentBlock, "clientKeyPath"),
		Port:           intField(agentBlock, "port"),
	}
	if opts.CAPath == "" || opts.ClientCertPath == "" || opts.ClientKeyPath == "" {
		return nil, agentclient.ProbeOptions{}, false
	}

	endpointsRaw, ok := agentBlock["endpoints"].(map[string]any)
	if !ok {
		return nil, agentclient.ProbeOptions{}, false
	}
	endpoints := make(map[string]string, len(endpointsRaw))
	for name, ip := range endpointsRaw {
		if s, ok := ip.(string); ok {
			endpoints[name] = s
		}
	}
	if len(endpoints) == 0 {
		return nil, agentclient.ProbeOptions{}, false
	}

	return endpoints, opts, true
}

func stringField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func intField(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

func setStatusField(resource *protocol.Resource, key string, value any) {
	if resource.Status == nil {
		resource.Status = map[string]any{}
	}
	resource.Status[key] = value
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
	// Parse retry count from continuation token (ignore parse errors, default to 0)
	retryCount := 0
	if strings.HasPrefix(req.ContinuationToken, "get-ips:") {
		_, _ = fmt.Sscanf(req.ContinuationToken, "get-ips:%d", &retryCount)
	}

	// Check if we have pre-allocated static IPs
	staticIPs, _ := resources.AllocateIPs(name, spec)

	// Collect node IPs from dispatch results (or use static IPs)
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

		// If we have static IPs, use them; otherwise try to get from resource status
		if staticIP, ok := staticIPs[nodeName]; ok && staticIP != "" {
			nodeIPs[nodeName] = staticIP
		} else if result.Resource != nil && result.Resource.Status != nil {
			if ip, ok := result.Resource.Status["ip"].(string); ok && ip != "" {
				nodeIPs[nodeName] = ip
			}
		}
	}

	// Deduplicate children (in case we're processing results from multiple phases)
	children = deduplicateChildren(children)

	// Check if we have IPs for all nodes
	if len(nodeIPs) < len(allNodes) {
		// If using static IPs, we should have them all already
		if len(staticIPs) > 0 {
			// Fill in any missing IPs from static allocation
			for _, nodeName := range allNodes {
				if _, ok := nodeIPs[nodeName]; !ok {
					if staticIP, ok := staticIPs[nodeName]; ok {
						nodeIPs[nodeName] = staticIP
					}
				}
			}
		}
	}

	// Re-check if we have IPs for all nodes after static IP fill
	if len(nodeIPs) < len(allNodes) {
		// Check if we've exceeded max retries
		if retryCount >= maxIPRetries {
			return &protocol.Response{
				Status: protocol.StatusError,
				Error: &protocol.Error{
					Code:    protocol.ErrorCodeInternal,
					Message: fmt.Sprintf("timed out waiting for VM IPs (got %d/%d after %d retries). Consider using static IPs via spec.network.staticIPs, or ensure QEMU guest agent is running in VMs.", len(nodeIPs), len(allNodes), retryCount),
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
	if result.AgentBundleDir != "" {
		outputs["agent"] = map[string]any{
			"bundleDir":      result.AgentBundleDir,
			"caPath":         filepath.Join(result.AgentBundleDir, "ca.pem"),
			"clientCertPath": filepath.Join(result.AgentBundleDir, "client.pem"),
			"clientKeyPath":  filepath.Join(result.AgentBundleDir, "client.key"),
			"port":           result.AgentPort,
			"endpoints":      result.AgentEndpoints,
		}
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
