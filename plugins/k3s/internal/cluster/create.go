package cluster

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openctl/openctl-k3s/internal/resources"
	"github.com/openctl/openctl-k3s/internal/ssh"
	"github.com/openctl/openctl/pkg/protocol"
)

// Creator handles cluster creation
type Creator struct {
	name   string
	spec   *resources.ClusterSpec
	config *protocol.ProviderConfig
	nodeIPs map[string]string // node name -> IP address
}

// NewCreator creates a new cluster creator
func NewCreator(name string, spec *resources.ClusterSpec, config *protocol.ProviderConfig) *Creator {
	return &Creator{
		name:    name,
		spec:    spec,
		config:  config,
		nodeIPs: make(map[string]string),
	}
}

// GenerateDispatchRequests generates VM creation dispatch requests
func (c *Creator) GenerateDispatchRequests() []*protocol.DispatchRequest {
	cpNodes, workerNodes := resources.NodeNames(c.name, c.spec)
	allNodes := append(cpNodes, workerNodes...)

	requests := make([]*protocol.DispatchRequest, 0, len(allNodes))

	for i, nodeName := range allNodes {
		// Determine size based on node type
		size := c.spec.Compute.Default
		if i < len(cpNodes) && c.spec.Nodes.ControlPlane.Size != nil {
			size = *c.spec.Nodes.ControlPlane.Size
		}

		// For workers, find the matching pool
		if i >= len(cpNodes) {
			workerIdx := i - len(cpNodes)
			for _, pool := range c.spec.Nodes.Workers {
				if workerIdx < pool.Count {
					if pool.Size != nil {
						size = *pool.Size
					}
					break
				}
				workerIdx -= pool.Count
			}
		}

		// Build VM manifest
		manifest := &protocol.Resource{
			APIVersion: fmt.Sprintf("%s.openctl.io/v1", c.spec.Compute.Provider),
			Kind:       "VirtualMachine",
			Metadata: protocol.ResourceMetadata{
				Name: nodeName,
				Labels: map[string]string{
					"k3s.openctl.io/cluster": c.name,
					"k3s.openctl.io/role":    nodeRole(i, len(cpNodes)),
				},
			},
			Spec: map[string]any{
				"startOnCreate": true,
				"agent": map[string]any{
					"enabled": true,
				},
				"cpu": map[string]any{
					"cores": size.CPUs,
				},
				"memory": map[string]any{
					"size": size.MemoryMB,
				},
				"disks": []map[string]any{
					{
						"name": "scsi0",
						"size": fmt.Sprintf("%dG", size.DiskGB),
					},
				},
				"networks": []map[string]any{
					{
						"name":   "net0",
						"bridge": "vmbr0",
						"model":  "virtio",
					},
				},
				"cloudInit": map[string]any{
					"user":    c.spec.SSH.User,
					"sshKeys": c.spec.SSH.PublicKeys,
					"ipConfig": map[string]any{
						"net0": map[string]any{
							"ip": "dhcp",
						},
					},
				},
			},
		}

		// Add image source
		if c.spec.Compute.Image.URL != "" {
			cloudImage := map[string]any{
				"url": c.spec.Compute.Image.URL,
			}
			// Use storage from spec, fall back to config defaults
			storage := c.spec.Compute.Image.Storage
			if storage == "" {
				storage = c.config.Defaults["storage"]
			}
			if storage != "" {
				cloudImage["storage"] = storage
			}
			// Use diskStorage from spec if specified
			diskStorage := c.spec.Compute.Image.DiskStorage
			if diskStorage != "" {
				cloudImage["diskStorage"] = diskStorage
			}
			manifest.Spec["cloudImage"] = cloudImage
		} else if c.spec.Compute.Image.Template != "" {
			manifest.Spec["template"] = map[string]any{
				"name": c.spec.Compute.Image.Template,
			}
		}

		requests = append(requests, &protocol.DispatchRequest{
			ID:           fmt.Sprintf("vm-%s", nodeName),
			Provider:     c.spec.Compute.Provider,
			Action:       protocol.ActionCreate,
			ResourceType: "VirtualMachine",
			Manifest:     manifest,
			WaitFor: &protocol.WaitCondition{
				Field:   "status.state",
				Value:   "running",
				Timeout: 10 * time.Minute,
			},
		})
	}

	return requests
}

// SetNodeIPs stores IP addresses for nodes from dispatch results
func (c *Creator) SetNodeIPs(results []*protocol.DispatchResult) error {
	for _, result := range results {
		if result.Status != protocol.StatusSuccess {
			return fmt.Errorf("VM creation failed for %s: %v", result.ID, result.Error)
		}

		// Extract node name from ID
		nodeName := strings.TrimPrefix(result.ID, "vm-")

		// Get IP from resource status (if available from QEMU agent)
		// For now, we'll need to query the IP separately
		c.nodeIPs[nodeName] = "" // Will be populated during SSH connection
	}

	return nil
}

// InstallK3s installs K3s on all cluster nodes
func (c *Creator) InstallK3s(nodeIPs map[string]string) (*InstallResult, error) {
	c.nodeIPs = nodeIPs
	cpNodes, workerNodes := resources.NodeNames(c.name, c.spec)

	if len(cpNodes) == 0 {
		return nil, fmt.Errorf("at least one control plane node is required")
	}

	// Install K3s server on first control plane
	firstCP := cpNodes[0]
	firstCPIP := c.nodeIPs[firstCP]
	if firstCPIP == "" {
		return nil, fmt.Errorf("IP address not available for %s", firstCP)
	}

	fmt.Fprintf(os.Stderr, "Installing K3s server on %s (%s)...\n", firstCP, firstCPIP)

	client, err := ssh.WaitForSSH(firstCPIP, 22, c.spec.SSH.User, c.spec.SSH.PrivateKeyPath, 5*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", firstCP, err)
	}
	defer client.Close()

	// Install K3s server
	installCmd := c.buildServerInstallCommand()
	if _, err := client.RunSudo(installCmd); err != nil {
		return nil, fmt.Errorf("failed to install K3s server on %s: %w", firstCP, err)
	}

	// Wait for K3s to be ready
	time.Sleep(30 * time.Second)

	// Get the join token
	token, err := client.RunSudo("cat /var/lib/rancher/k3s/server/node-token")
	if err != nil {
		return nil, fmt.Errorf("failed to get K3s token: %w", err)
	}
	token = strings.TrimSpace(token)

	// Get kubeconfig
	kubeconfig, err := client.RunSudo("cat /etc/rancher/k3s/k3s.yaml")
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	// Update kubeconfig server address
	kubeconfig = strings.ReplaceAll(kubeconfig, "127.0.0.1", firstCPIP)
	kubeconfig = strings.ReplaceAll(kubeconfig, "localhost", firstCPIP)

	// Install on additional control plane nodes (if any)
	for i := 1; i < len(cpNodes); i++ {
		cpNode := cpNodes[i]
		cpIP := c.nodeIPs[cpNode]
		if cpIP == "" {
			return nil, fmt.Errorf("IP address not available for %s", cpNode)
		}

		fmt.Fprintf(os.Stderr, "Installing K3s server on %s (%s)...\n", cpNode, cpIP)

		cpClient, err := ssh.WaitForSSH(cpIP, 22, c.spec.SSH.User, c.spec.SSH.PrivateKeyPath, 5*time.Minute)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to %s: %w", cpNode, err)
		}

		joinCmd := c.buildServerJoinCommand(firstCPIP, token)
		if _, err := cpClient.RunSudo(joinCmd); err != nil {
			cpClient.Close()
			return nil, fmt.Errorf("failed to install K3s server on %s: %w", cpNode, err)
		}
		cpClient.Close()
	}

	// Install on worker nodes
	for _, workerNode := range workerNodes {
		workerIP := c.nodeIPs[workerNode]
		if workerIP == "" {
			return nil, fmt.Errorf("IP address not available for %s", workerNode)
		}

		fmt.Fprintf(os.Stderr, "Installing K3s agent on %s (%s)...\n", workerNode, workerIP)

		workerClient, err := ssh.WaitForSSH(workerIP, 22, c.spec.SSH.User, c.spec.SSH.PrivateKeyPath, 5*time.Minute)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to %s: %w", workerNode, err)
		}

		agentCmd := c.buildAgentInstallCommand(firstCPIP, token)
		if _, err := workerClient.RunSudo(agentCmd); err != nil {
			workerClient.Close()
			return nil, fmt.Errorf("failed to install K3s agent on %s: %w", workerNode, err)
		}
		workerClient.Close()
	}

	// Save kubeconfig
	kubeconfigPath, err := c.saveKubeconfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to save kubeconfig: %w", err)
	}

	return &InstallResult{
		KubeconfigPath: kubeconfigPath,
		ServerIP:       firstCPIP,
		Token:          token,
	}, nil
}

// InstallResult contains the result of K3s installation
type InstallResult struct {
	KubeconfigPath string
	ServerIP       string
	Token          string
}

func (c *Creator) buildServerInstallCommand() string {
	cmd := "curl -sfL https://get.k3s.io | "

	var env []string
	if c.spec.K3s.Version != "" {
		env = append(env, fmt.Sprintf("INSTALL_K3S_VERSION=%s", c.spec.K3s.Version))
	}

	var args []string
	if c.spec.K3s.ClusterCIDR != "" {
		args = append(args, fmt.Sprintf("--cluster-cidr=%s", c.spec.K3s.ClusterCIDR))
	}
	if c.spec.K3s.ServiceCIDR != "" {
		args = append(args, fmt.Sprintf("--service-cidr=%s", c.spec.K3s.ServiceCIDR))
	}
	args = append(args, c.spec.K3s.ExtraArgs...)

	if len(env) > 0 {
		cmd += strings.Join(env, " ") + " "
	}

	cmd += "sh -s -"

	if len(args) > 0 {
		cmd += " " + strings.Join(args, " ")
	}

	return cmd
}

func (c *Creator) buildServerJoinCommand(serverIP, token string) string {
	cmd := "curl -sfL https://get.k3s.io | "

	var env []string
	if c.spec.K3s.Version != "" {
		env = append(env, fmt.Sprintf("INSTALL_K3S_VERSION=%s", c.spec.K3s.Version))
	}
	env = append(env, fmt.Sprintf("K3S_TOKEN=%s", token))
	env = append(env, fmt.Sprintf("K3S_URL=https://%s:6443", serverIP))

	cmd += strings.Join(env, " ") + " sh -s - server"

	return cmd
}

func (c *Creator) buildAgentInstallCommand(serverIP, token string) string {
	cmd := "curl -sfL https://get.k3s.io | "

	var env []string
	if c.spec.K3s.Version != "" {
		env = append(env, fmt.Sprintf("INSTALL_K3S_VERSION=%s", c.spec.K3s.Version))
	}
	env = append(env, fmt.Sprintf("K3S_TOKEN=%s", token))
	env = append(env, fmt.Sprintf("K3S_URL=https://%s:6443", serverIP))

	cmd += strings.Join(env, " ") + " sh -s -"

	return cmd
}

func (c *Creator) saveKubeconfig(kubeconfig string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	kubeconfigDir := filepath.Join(homeDir, ".openctl", "k3s", c.name)
	if err := os.MkdirAll(kubeconfigDir, 0700); err != nil {
		return "", err
	}

	kubeconfigPath := filepath.Join(kubeconfigDir, "kubeconfig")
	if err := os.WriteFile(kubeconfigPath, []byte(kubeconfig), 0600); err != nil {
		return "", err
	}

	return kubeconfigPath, nil
}

func nodeRole(index, cpCount int) string {
	if index < cpCount {
		return "control-plane"
	}
	return "worker"
}
