package cluster

import (
	"strings"
	"testing"

	"github.com/openctl/openctl-k3s/internal/resources"
	"github.com/openctl/openctl/pkg/protocol"
)

func TestNewCreator(t *testing.T) {
	spec := &resources.ClusterSpec{}
	config := &protocol.ProviderConfig{}

	creator := NewCreator("test-cluster", spec, config)

	if creator.name != "test-cluster" {
		t.Errorf("expected name=test-cluster, got %s", creator.name)
	}
	if creator.spec != spec {
		t.Error("expected spec to match")
	}
	if creator.config != config {
		t.Error("expected config to match")
	}
	if creator.nodeIPs == nil {
		t.Error("expected nodeIPs to be initialized")
	}
}

func TestGenerateDispatchRequests_SingleCP(t *testing.T) {
	spec := &resources.ClusterSpec{
		Compute: resources.ComputeSpec{
			Provider: "proxmox",
			Image: resources.ImageSpec{
				URL: "https://example.com/image.img",
			},
			Default: resources.DefaultSizeSpec{
				CPUs:     2,
				MemoryMB: 4096,
				DiskGB:   50,
			},
		},
		Nodes: resources.NodesSpec{
			ControlPlane: resources.ControlPlaneSpec{Count: 1},
		},
		SSH: resources.SSHSpec{
			User:       "ubuntu",
			PublicKeys: []string{"ssh-ed25519 AAAA..."},
		},
	}
	config := &protocol.ProviderConfig{
		Defaults: map[string]string{"storage": "local"},
	}

	creator := NewCreator("test", spec, config)
	requests := creator.GenerateDispatchRequests()

	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}

	req := requests[0]
	if req.ID != "vm-test-cp-0" {
		t.Errorf("expected ID=vm-test-cp-0, got %s", req.ID)
	}
	if req.Provider != "proxmox" {
		t.Errorf("expected provider=proxmox, got %s", req.Provider)
	}
	if req.Action != protocol.ActionCreate {
		t.Errorf("expected action=create, got %s", req.Action)
	}
	if req.ResourceType != "VirtualMachine" {
		t.Errorf("expected resourceType=VirtualMachine, got %s", req.ResourceType)
	}
	if req.Manifest == nil {
		t.Fatal("expected manifest to be set")
	}
	if req.Manifest.Metadata.Name != "test-cp-0" {
		t.Errorf("expected manifest.metadata.name=test-cp-0, got %s", req.Manifest.Metadata.Name)
	}

	// Check labels
	if req.Manifest.Metadata.Labels["k3s.openctl.io/cluster"] != "test" {
		t.Errorf("expected cluster label, got %v", req.Manifest.Metadata.Labels)
	}
	if req.Manifest.Metadata.Labels["k3s.openctl.io/role"] != "control-plane" {
		t.Errorf("expected role=control-plane, got %v", req.Manifest.Metadata.Labels)
	}

	// Check spec
	cpu, ok := req.Manifest.Spec["cpu"].(map[string]any)
	if !ok {
		t.Fatal("expected cpu in spec")
	}
	if cpu["cores"] != 2 {
		t.Errorf("expected cpu.cores=2, got %v", cpu["cores"])
	}

	// Check cloudImage is set
	if _, ok := req.Manifest.Spec["cloudImage"]; !ok {
		t.Error("expected cloudImage in spec")
	}

	// Check wait condition
	if req.WaitFor == nil {
		t.Fatal("expected WaitFor to be set")
	}
	if req.WaitFor.Field != "status.state" {
		t.Errorf("expected WaitFor.Field=status.state, got %s", req.WaitFor.Field)
	}
	if req.WaitFor.Value != "running" {
		t.Errorf("expected WaitFor.Value=running, got %s", req.WaitFor.Value)
	}
}

func TestGenerateDispatchRequests_WithWorkers(t *testing.T) {
	spec := &resources.ClusterSpec{
		Compute: resources.ComputeSpec{
			Provider: "proxmox",
			Image: resources.ImageSpec{
				Template: "ubuntu-template",
			},
			Default: resources.DefaultSizeSpec{
				CPUs:     2,
				MemoryMB: 4096,
				DiskGB:   50,
			},
		},
		Nodes: resources.NodesSpec{
			ControlPlane: resources.ControlPlaneSpec{Count: 1},
			Workers: []resources.WorkerSpec{
				{Name: "general", Count: 2},
			},
		},
		SSH: resources.SSHSpec{
			User: "ubuntu",
		},
	}
	config := &protocol.ProviderConfig{}

	creator := NewCreator("cluster", spec, config)
	requests := creator.GenerateDispatchRequests()

	if len(requests) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(requests))
	}

	// Check control plane
	if requests[0].ID != "vm-cluster-cp-0" {
		t.Errorf("expected first request ID=vm-cluster-cp-0, got %s", requests[0].ID)
	}
	if requests[0].Manifest.Metadata.Labels["k3s.openctl.io/role"] != "control-plane" {
		t.Errorf("expected first request role=control-plane")
	}

	// Check workers
	if requests[1].ID != "vm-cluster-general-0" {
		t.Errorf("expected second request ID=vm-cluster-general-0, got %s", requests[1].ID)
	}
	if requests[1].Manifest.Metadata.Labels["k3s.openctl.io/role"] != "worker" {
		t.Errorf("expected second request role=worker")
	}

	if requests[2].ID != "vm-cluster-general-1" {
		t.Errorf("expected third request ID=vm-cluster-general-1, got %s", requests[2].ID)
	}

	// Check template is used (not cloudImage)
	if _, ok := requests[0].Manifest.Spec["template"]; !ok {
		t.Error("expected template in spec")
	}
	if _, ok := requests[0].Manifest.Spec["cloudImage"]; ok {
		t.Error("expected cloudImage to NOT be in spec when using template")
	}
}

func TestGenerateDispatchRequests_CustomSizes(t *testing.T) {
	cpSize := resources.DefaultSizeSpec{CPUs: 4, MemoryMB: 8192, DiskGB: 100}
	workerSize := resources.DefaultSizeSpec{CPUs: 8, MemoryMB: 16384, DiskGB: 200}

	spec := &resources.ClusterSpec{
		Compute: resources.ComputeSpec{
			Provider: "proxmox",
			Image:    resources.ImageSpec{URL: "https://example.com/img"},
			Default:  resources.DefaultSizeSpec{CPUs: 2, MemoryMB: 4096, DiskGB: 50},
		},
		Nodes: resources.NodesSpec{
			ControlPlane: resources.ControlPlaneSpec{Count: 1, Size: &cpSize},
			Workers: []resources.WorkerSpec{
				{Name: "gpu", Count: 1, Size: &workerSize},
			},
		},
		SSH: resources.SSHSpec{User: "ubuntu"},
	}
	config := &protocol.ProviderConfig{Defaults: map[string]string{"storage": "local"}}

	creator := NewCreator("sized", spec, config)
	requests := creator.GenerateDispatchRequests()

	if len(requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(requests))
	}

	// Check CP uses custom size
	cpCPU := requests[0].Manifest.Spec["cpu"].(map[string]any)
	if cpCPU["cores"] != 4 {
		t.Errorf("expected CP cpu.cores=4, got %v", cpCPU["cores"])
	}
	cpMem := requests[0].Manifest.Spec["memory"].(map[string]any)
	if cpMem["size"] != 8192 {
		t.Errorf("expected CP memory.size=8192, got %v", cpMem["size"])
	}

	// Check worker uses custom size
	workerCPU := requests[1].Manifest.Spec["cpu"].(map[string]any)
	if workerCPU["cores"] != 8 {
		t.Errorf("expected worker cpu.cores=8, got %v", workerCPU["cores"])
	}
	workerMem := requests[1].Manifest.Spec["memory"].(map[string]any)
	if workerMem["size"] != 16384 {
		t.Errorf("expected worker memory.size=16384, got %v", workerMem["size"])
	}
}

func TestBuildServerInstallCommand(t *testing.T) {
	tests := []struct {
		name     string
		spec     *resources.ClusterSpec
		contains []string
	}{
		{
			name: "basic",
			spec: &resources.ClusterSpec{},
			contains: []string{
				"curl -sfL https://get.k3s.io",
				"sh -s -",
			},
		},
		{
			name: "with version",
			spec: &resources.ClusterSpec{
				K3s: resources.K3sSpec{
					Version: "v1.29.0+k3s1",
				},
			},
			contains: []string{
				"INSTALL_K3S_VERSION=v1.29.0+k3s1",
			},
		},
		{
			name: "with CIDRs",
			spec: &resources.ClusterSpec{
				K3s: resources.K3sSpec{
					ClusterCIDR: "10.42.0.0/16",
					ServiceCIDR: "10.43.0.0/16",
				},
			},
			contains: []string{
				"--cluster-cidr=10.42.0.0/16",
				"--service-cidr=10.43.0.0/16",
			},
		},
		{
			name: "with extra args",
			spec: &resources.ClusterSpec{
				K3s: resources.K3sSpec{
					ExtraArgs: []string{"--disable=traefik", "--disable=servicelb"},
				},
			},
			contains: []string{
				"--disable=traefik",
				"--disable=servicelb",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			creator := NewCreator("test", tt.spec, &protocol.ProviderConfig{})
			cmd := creator.buildServerInstallCommand()

			for _, expected := range tt.contains {
				if !strings.Contains(cmd, expected) {
					t.Errorf("expected command to contain %q, got: %s", expected, cmd)
				}
			}
		})
	}
}

func TestBuildServerJoinCommand(t *testing.T) {
	spec := &resources.ClusterSpec{
		K3s: resources.K3sSpec{
			Version: "v1.29.0+k3s1",
		},
	}
	creator := NewCreator("test", spec, &protocol.ProviderConfig{})

	cmd := creator.buildServerJoinCommand("192.168.1.100", "mytoken123")

	expected := []string{
		"curl -sfL https://get.k3s.io",
		"INSTALL_K3S_VERSION=v1.29.0+k3s1",
		"K3S_TOKEN=mytoken123",
		"K3S_URL=https://192.168.1.100:6443",
		"server",
	}

	for _, exp := range expected {
		if !strings.Contains(cmd, exp) {
			t.Errorf("expected command to contain %q, got: %s", exp, cmd)
		}
	}
}

func TestBuildAgentInstallCommand(t *testing.T) {
	spec := &resources.ClusterSpec{
		K3s: resources.K3sSpec{
			Version: "v1.29.0+k3s1",
		},
	}
	creator := NewCreator("test", spec, &protocol.ProviderConfig{})

	cmd := creator.buildAgentInstallCommand("192.168.1.100", "mytoken123")

	expected := []string{
		"curl -sfL https://get.k3s.io",
		"INSTALL_K3S_VERSION=v1.29.0+k3s1",
		"K3S_TOKEN=mytoken123",
		"K3S_URL=https://192.168.1.100:6443",
	}

	for _, exp := range expected {
		if !strings.Contains(cmd, exp) {
			t.Errorf("expected command to contain %q, got: %s", exp, cmd)
		}
	}

	// Agent command should NOT contain "server"
	if strings.Contains(cmd, " server") {
		t.Errorf("agent command should not contain 'server', got: %s", cmd)
	}
}

func TestNodeRole(t *testing.T) {
	tests := []struct {
		index    int
		cpCount  int
		expected string
	}{
		{0, 1, "control-plane"},
		{0, 3, "control-plane"},
		{1, 3, "control-plane"},
		{2, 3, "control-plane"},
		{3, 3, "worker"},
		{1, 1, "worker"},
		{5, 3, "worker"},
	}

	for _, tt := range tests {
		result := nodeRole(tt.index, tt.cpCount)
		if result != tt.expected {
			t.Errorf("nodeRole(%d, %d) = %s, want %s", tt.index, tt.cpCount, result, tt.expected)
		}
	}
}

func TestSetNodeIPs_Success(t *testing.T) {
	creator := NewCreator("test", &resources.ClusterSpec{}, &protocol.ProviderConfig{})

	results := []*protocol.DispatchResult{
		{ID: "vm-test-cp-0", Status: protocol.StatusSuccess},
		{ID: "vm-test-worker-0", Status: protocol.StatusSuccess},
	}

	err := creator.SetNodeIPs(results)
	if err != nil {
		t.Fatalf("SetNodeIPs failed: %v", err)
	}

	// Verify nodes were added (IPs empty for now)
	if _, ok := creator.nodeIPs["test-cp-0"]; !ok {
		t.Error("expected test-cp-0 in nodeIPs")
	}
	if _, ok := creator.nodeIPs["test-worker-0"]; !ok {
		t.Error("expected test-worker-0 in nodeIPs")
	}
}

func TestSetNodeIPs_Failure(t *testing.T) {
	creator := NewCreator("test", &resources.ClusterSpec{}, &protocol.ProviderConfig{})

	results := []*protocol.DispatchResult{
		{ID: "vm-test-cp-0", Status: protocol.StatusSuccess},
		{
			ID:     "vm-test-worker-0",
			Status: protocol.StatusError,
			Error:  &protocol.Error{Message: "VM creation failed"},
		},
	}

	err := creator.SetNodeIPs(results)
	if err == nil {
		t.Fatal("expected error when VM creation failed")
	}
	if !strings.Contains(err.Error(), "VM creation failed") {
		t.Errorf("expected error to contain 'VM creation failed', got: %v", err)
	}
}
