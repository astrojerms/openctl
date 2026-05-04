package resources

import (
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

func TestParseClusterSpec(t *testing.T) {
	resource := &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       "Cluster",
		Metadata:   protocol.ResourceMetadata{Name: "test-cluster"},
		Spec: map[string]any{
			"compute": map[string]any{
				"provider": "proxmox",
				"context":  "homelab",
				"image": map[string]any{
					"url": "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img",
				},
				"default": map[string]any{
					"cpus":     float64(2),
					"memoryMB": float64(4096),
					"diskGB":   float64(50),
				},
			},
			"nodes": map[string]any{
				"controlPlane": map[string]any{
					"count": float64(3),
				},
				"workers": []any{
					map[string]any{
						"name":  "general",
						"count": float64(2),
					},
				},
			},
			"k3s": map[string]any{
				"version":     "v1.29.0+k3s1",
				"clusterCIDR": "10.42.0.0/16",
				"serviceCIDR": "10.43.0.0/16",
				"extraArgs":   []any{"--disable=traefik"},
			},
			"ssh": map[string]any{
				"user":           "ubuntu",
				"privateKeyPath": "~/.ssh/id_ed25519",
				"publicKeys": []any{
					"ssh-ed25519 AAAA...",
				},
			},
		},
	}

	spec, err := ParseClusterSpec(resource)
	if err != nil {
		t.Fatalf("ParseClusterSpec failed: %v", err)
	}

	// Test compute section
	if spec.Compute.Provider != "proxmox" {
		t.Errorf("expected compute.provider=proxmox, got %s", spec.Compute.Provider)
	}
	if spec.Compute.Context != "homelab" {
		t.Errorf("expected compute.context=homelab, got %s", spec.Compute.Context)
	}
	if spec.Compute.Image.URL != "https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img" {
		t.Errorf("unexpected compute.image.url: %s", spec.Compute.Image.URL)
	}
	if spec.Compute.Default.CPUs != 2 {
		t.Errorf("expected compute.default.cpus=2, got %d", spec.Compute.Default.CPUs)
	}
	if spec.Compute.Default.MemoryMB != 4096 {
		t.Errorf("expected compute.default.memoryMB=4096, got %d", spec.Compute.Default.MemoryMB)
	}
	if spec.Compute.Default.DiskGB != 50 {
		t.Errorf("expected compute.default.diskGB=50, got %d", spec.Compute.Default.DiskGB)
	}

	// Test nodes section
	if spec.Nodes.ControlPlane.Count != 3 {
		t.Errorf("expected nodes.controlPlane.count=3, got %d", spec.Nodes.ControlPlane.Count)
	}
	if len(spec.Nodes.Workers) != 1 {
		t.Fatalf("expected 1 worker pool, got %d", len(spec.Nodes.Workers))
	}
	if spec.Nodes.Workers[0].Name != "general" {
		t.Errorf("expected worker pool name=general, got %s", spec.Nodes.Workers[0].Name)
	}
	if spec.Nodes.Workers[0].Count != 2 {
		t.Errorf("expected worker pool count=2, got %d", spec.Nodes.Workers[0].Count)
	}

	// Test k3s section
	if spec.K3s.Version != "v1.29.0+k3s1" {
		t.Errorf("expected k3s.version=v1.29.0+k3s1, got %s", spec.K3s.Version)
	}
	if spec.K3s.ClusterCIDR != "10.42.0.0/16" {
		t.Errorf("expected k3s.clusterCIDR=10.42.0.0/16, got %s", spec.K3s.ClusterCIDR)
	}
	if spec.K3s.ServiceCIDR != "10.43.0.0/16" {
		t.Errorf("expected k3s.serviceCIDR=10.43.0.0/16, got %s", spec.K3s.ServiceCIDR)
	}
	if len(spec.K3s.ExtraArgs) != 1 || spec.K3s.ExtraArgs[0] != "--disable=traefik" {
		t.Errorf("expected k3s.extraArgs=[--disable=traefik], got %v", spec.K3s.ExtraArgs)
	}

	// Test ssh section
	if spec.SSH.User != "ubuntu" {
		t.Errorf("expected ssh.user=ubuntu, got %s", spec.SSH.User)
	}
	if spec.SSH.PrivateKeyPath != "~/.ssh/id_ed25519" {
		t.Errorf("expected ssh.privateKeyPath=~/.ssh/id_ed25519, got %s", spec.SSH.PrivateKeyPath)
	}
	if len(spec.SSH.PublicKeys) != 1 {
		t.Errorf("expected 1 public key, got %d", len(spec.SSH.PublicKeys))
	}
}

func TestParseClusterSpec_Empty(t *testing.T) {
	resource := &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       "Cluster",
		Metadata:   protocol.ResourceMetadata{Name: "empty-cluster"},
		Spec:       nil,
	}

	spec, err := ParseClusterSpec(resource)
	if err != nil {
		t.Fatalf("ParseClusterSpec failed: %v", err)
	}

	if spec.Compute.Provider != "" {
		t.Errorf("expected empty provider, got %s", spec.Compute.Provider)
	}
	if spec.Nodes.ControlPlane.Count != 0 {
		t.Errorf("expected 0 control plane count, got %d", spec.Nodes.ControlPlane.Count)
	}
}

func TestParseClusterSpec_Template(t *testing.T) {
	resource := &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       "Cluster",
		Metadata:   protocol.ResourceMetadata{Name: "template-cluster"},
		Spec: map[string]any{
			"compute": map[string]any{
				"provider": "proxmox",
				"image": map[string]any{
					"template": "ubuntu-22.04-cloudinit",
				},
			},
		},
	}

	spec, err := ParseClusterSpec(resource)
	if err != nil {
		t.Fatalf("ParseClusterSpec failed: %v", err)
	}

	if spec.Compute.Image.Template != "ubuntu-22.04-cloudinit" {
		t.Errorf("expected compute.image.template=ubuntu-22.04-cloudinit, got %s", spec.Compute.Image.Template)
	}
	if spec.Compute.Image.URL != "" {
		t.Errorf("expected empty image URL, got %s", spec.Compute.Image.URL)
	}
}

func TestParseClusterSpec_ControlPlaneSize(t *testing.T) {
	resource := &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       "Cluster",
		Metadata:   protocol.ResourceMetadata{Name: "sized-cluster"},
		Spec: map[string]any{
			"nodes": map[string]any{
				"controlPlane": map[string]any{
					"count": float64(1),
					"size": map[string]any{
						"cpus":     float64(4),
						"memoryMB": float64(8192),
						"diskGB":   float64(100),
					},
				},
			},
		},
	}

	spec, err := ParseClusterSpec(resource)
	if err != nil {
		t.Fatalf("ParseClusterSpec failed: %v", err)
	}

	if spec.Nodes.ControlPlane.Size == nil {
		t.Fatal("expected control plane size to be set")
	}
	if spec.Nodes.ControlPlane.Size.CPUs != 4 {
		t.Errorf("expected controlPlane.size.cpus=4, got %d", spec.Nodes.ControlPlane.Size.CPUs)
	}
	if spec.Nodes.ControlPlane.Size.MemoryMB != 8192 {
		t.Errorf("expected controlPlane.size.memoryMB=8192, got %d", spec.Nodes.ControlPlane.Size.MemoryMB)
	}
	if spec.Nodes.ControlPlane.Size.DiskGB != 100 {
		t.Errorf("expected controlPlane.size.diskGB=100, got %d", spec.Nodes.ControlPlane.Size.DiskGB)
	}
}

func TestParseClusterSpec_WorkerPoolSize(t *testing.T) {
	resource := &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       "Cluster",
		Metadata:   protocol.ResourceMetadata{Name: "worker-sized-cluster"},
		Spec: map[string]any{
			"nodes": map[string]any{
				"workers": []any{
					map[string]any{
						"name":  "gpu",
						"count": float64(2),
						"size": map[string]any{
							"cpus":     float64(8),
							"memoryMB": float64(16384),
							"diskGB":   float64(200),
						},
					},
				},
			},
		},
	}

	spec, err := ParseClusterSpec(resource)
	if err != nil {
		t.Fatalf("ParseClusterSpec failed: %v", err)
	}

	if len(spec.Nodes.Workers) != 1 {
		t.Fatalf("expected 1 worker pool, got %d", len(spec.Nodes.Workers))
	}
	if spec.Nodes.Workers[0].Size == nil {
		t.Fatal("expected worker size to be set")
	}
	if spec.Nodes.Workers[0].Size.CPUs != 8 {
		t.Errorf("expected worker.size.cpus=8, got %d", spec.Nodes.Workers[0].Size.CPUs)
	}
}

func TestNodeNames(t *testing.T) {
	tests := []struct {
		name         string
		clusterName  string
		spec         *ClusterSpec
		expectedCPs  []string
		expectedWork []string
	}{
		{
			name:        "single control plane",
			clusterName: "test",
			spec: &ClusterSpec{
				Nodes: NodesSpec{
					ControlPlane: ControlPlaneSpec{Count: 1},
				},
			},
			expectedCPs:  []string{"test-cp-0"},
			expectedWork: nil,
		},
		{
			name:        "HA control plane",
			clusterName: "prod",
			spec: &ClusterSpec{
				Nodes: NodesSpec{
					ControlPlane: ControlPlaneSpec{Count: 3},
				},
			},
			expectedCPs:  []string{"prod-cp-0", "prod-cp-1", "prod-cp-2"},
			expectedWork: nil,
		},
		{
			name:        "with workers",
			clusterName: "cluster",
			spec: &ClusterSpec{
				Nodes: NodesSpec{
					ControlPlane: ControlPlaneSpec{Count: 1},
					Workers: []WorkerSpec{
						{Name: "default", Count: 2},
					},
				},
			},
			expectedCPs:  []string{"cluster-cp-0"},
			expectedWork: []string{"cluster-default-0", "cluster-default-1"},
		},
		{
			name:        "multiple worker pools",
			clusterName: "multi",
			spec: &ClusterSpec{
				Nodes: NodesSpec{
					ControlPlane: ControlPlaneSpec{Count: 1},
					Workers: []WorkerSpec{
						{Name: "general", Count: 2},
						{Name: "gpu", Count: 1},
					},
				},
			},
			expectedCPs:  []string{"multi-cp-0"},
			expectedWork: []string{"multi-general-0", "multi-general-1", "multi-gpu-0"},
		},
		{
			name:        "worker pool without name",
			clusterName: "noname",
			spec: &ClusterSpec{
				Nodes: NodesSpec{
					ControlPlane: ControlPlaneSpec{Count: 1},
					Workers: []WorkerSpec{
						{Count: 2}, // No name specified
					},
				},
			},
			expectedCPs:  []string{"noname-cp-0"},
			expectedWork: []string{"noname-worker-0", "noname-worker-1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cps, workers := NodeNames(tt.clusterName, tt.spec)

			if len(cps) != len(tt.expectedCPs) {
				t.Fatalf("expected %d control planes, got %d", len(tt.expectedCPs), len(cps))
			}
			for i, expected := range tt.expectedCPs {
				if cps[i] != expected {
					t.Errorf("expected cp[%d]=%s, got %s", i, expected, cps[i])
				}
			}

			if len(workers) != len(tt.expectedWork) {
				t.Fatalf("expected %d workers, got %d", len(tt.expectedWork), len(workers))
			}
			for i, expected := range tt.expectedWork {
				if workers[i] != expected {
					t.Errorf("expected worker[%d]=%s, got %s", i, expected, workers[i])
				}
			}
		})
	}
}

func TestClusterToResource(t *testing.T) {
	spec := &ClusterSpec{
		Compute: ComputeSpec{
			Provider: "proxmox",
			Image: ImageSpec{
				URL: "https://example.com/image.img",
			},
			Default: DefaultSizeSpec{
				CPUs:     2,
				MemoryMB: 4096,
				DiskGB:   50,
			},
		},
		Nodes: NodesSpec{
			ControlPlane: ControlPlaneSpec{Count: 3},
		},
		K3s: K3sSpec{
			Version: "v1.29.0+k3s1",
		},
		SSH: SSHSpec{
			User: "ubuntu",
		},
	}

	outputs := map[string]any{
		"kubeconfigPath": "/home/user/.openctl/k3s/test/kubeconfig",
		"serverIP":       "192.168.1.100",
	}

	children := []protocol.ChildReference{
		{Provider: "proxmox", Kind: "VirtualMachine", Name: "test-cp-0"},
	}

	resource := ClusterToResource("test-cluster", spec, "Ready", outputs, children)

	if resource.APIVersion != "k3s.openctl.io/v1" {
		t.Errorf("expected apiVersion=k3s.openctl.io/v1, got %s", resource.APIVersion)
	}
	if resource.Kind != "Cluster" {
		t.Errorf("expected kind=Cluster, got %s", resource.Kind)
	}
	if resource.Metadata.Name != "test-cluster" {
		t.Errorf("expected name=test-cluster, got %s", resource.Metadata.Name)
	}

	// Check status
	if resource.Status["phase"] != "Ready" {
		t.Errorf("expected status.phase=Ready, got %v", resource.Status["phase"])
	}
	if resource.Status["kubeconfigPath"] != "/home/user/.openctl/k3s/test/kubeconfig" {
		t.Errorf("expected kubeconfigPath in status, got %v", resource.Status["kubeconfigPath"])
	}
	if resource.Status["serverIP"] != "192.168.1.100" {
		t.Errorf("expected serverIP in status, got %v", resource.Status["serverIP"])
	}

	// Check spec
	computeSpec, ok := resource.Spec["compute"].(map[string]any)
	if !ok {
		t.Fatal("expected compute in spec")
	}
	if computeSpec["provider"] != "proxmox" {
		t.Errorf("expected spec.compute.provider=proxmox, got %v", computeSpec["provider"])
	}
}

func TestParseSizeSpec(t *testing.T) {
	input := map[string]any{
		"cpus":     float64(4),
		"memoryMB": float64(8192),
		"diskGB":   float64(100),
	}

	size := parseSizeSpec(input)

	if size.CPUs != 4 {
		t.Errorf("expected cpus=4, got %d", size.CPUs)
	}
	if size.MemoryMB != 8192 {
		t.Errorf("expected memoryMB=8192, got %d", size.MemoryMB)
	}
	if size.DiskGB != 100 {
		t.Errorf("expected diskGB=100, got %d", size.DiskGB)
	}
}

func TestParseSizeSpec_Partial(t *testing.T) {
	input := map[string]any{
		"cpus": float64(2),
		// memoryMB and diskGB not specified
	}

	size := parseSizeSpec(input)

	if size.CPUs != 2 {
		t.Errorf("expected cpus=2, got %d", size.CPUs)
	}
	if size.MemoryMB != 0 {
		t.Errorf("expected memoryMB=0, got %d", size.MemoryMB)
	}
	if size.DiskGB != 0 {
		t.Errorf("expected diskGB=0, got %d", size.DiskGB)
	}
}
