package k3s

import (
	"context"
	"fmt"

	"github.com/openctl/openctl/internal/controller/providers"
	k3sresources "github.com/openctl/openctl/pkg/k3s/resources"
	"github.com/openctl/openctl/pkg/protocol"
)

// Plan implements providers.Planner. Expands a Cluster manifest into
// the concrete child manifests it composes: one VirtualMachine per
// node, one K3sNode per node, and one AgentInstall per node — all
// linked via $ref pointers.
//
// Phase 8 step 4: introduces the interface + implementation. The
// dispatcher does NOT consume this today; Cluster.Apply remains the
// operative path. A future step will wire the dispatcher to prefer
// Plan over Apply so composite resources become DAG-driven.
//
// Design notes:
//   - The first CP node has no joinFrom (initializes the cluster).
//     Every other K3sNode's joinFrom + joinURLFrom point at that first
//     CP via $ref, so the resolver serializes install order for us.
//   - AgentInstall's clusterName is a plain string (the Cluster's
//     metadata.name) since the CA lives on-disk keyed by that string.
//   - VM manifests here are structurally identical to what
//     pkg/k3s/cluster.Creator.GenerateDispatchRequests produces, so
//     the future switchover doesn't change VM shape.
func (p *Provider) Plan(_ context.Context, manifest *protocol.Resource) (*providers.PlanResult, error) {
	if manifest.Kind != kindCluster {
		return nil, fmt.Errorf("k3s Planner: only Cluster kind is composable (got %q)", manifest.Kind)
	}
	clusterName := manifest.Metadata.Name
	if clusterName == "" {
		return nil, fmt.Errorf("metadata.name is required")
	}
	spec, err := k3sresources.ParseClusterSpec(manifest)
	if err != nil {
		return nil, fmt.Errorf("parse cluster spec: %w", err)
	}

	cpNodes, workerNodes := k3sresources.NodeNames(clusterName, spec)
	if len(cpNodes) == 0 {
		return nil, fmt.Errorf("cluster must have at least one control plane node")
	}
	nodeIPs, _ := k3sresources.AllocateIPs(clusterName, spec)

	children := make([]*protocol.Resource, 0, (len(cpNodes)+len(workerNodes))*3)
	firstCP := cpNodes[0]

	// One VirtualMachine per node.
	allNodes := append([]string{}, cpNodes...)
	allNodes = append(allNodes, workerNodes...)
	for i, nodeName := range allNodes {
		size := sizeForNode(i, len(cpNodes), spec)
		staticIP := nodeIPs[nodeName]
		vm := buildVMManifest(clusterName, nodeName, i, len(cpNodes), size, staticIP, spec)
		children = append(children, vm)
	}

	// One K3sNode per node. First CP has no joinFrom; every other
	// node joins the first CP.
	for i, nodeName := range allNodes {
		role := "server"
		if i >= len(cpNodes) {
			role = "agent"
		}
		k3sNode := buildK3sNodeManifest(clusterName, nodeName, role, firstCP, spec)
		if i == 0 && role == "server" {
			// First server initializes — no join fields.
			delete(k3sNode.Spec, "joinFrom")
			delete(k3sNode.Spec, "joinURLFrom")
		}
		children = append(children, k3sNode)
	}

	// One AgentInstall per node — all point at the same cluster.
	for _, nodeName := range allNodes {
		children = append(children, buildAgentInstallManifest(clusterName, nodeName, spec))
	}

	return &providers.PlanResult{Children: children}, nil
}

func sizeForNode(i, cpCount int, spec *k3sresources.ClusterSpec) k3sresources.DefaultSizeSpec {
	size := spec.Compute.Default
	if i < cpCount && spec.Nodes.ControlPlane.Size != nil {
		size = *spec.Nodes.ControlPlane.Size
	}
	if i >= cpCount {
		workerIdx := i - cpCount
		for _, pool := range spec.Nodes.Workers {
			if workerIdx < pool.Count {
				if pool.Size != nil {
					size = *pool.Size
				}
				break
			}
			workerIdx -= pool.Count
		}
	}
	return size
}

// buildVMManifest mirrors the shape produced by
// pkg/k3s/cluster.Creator.GenerateDispatchRequests. Kept side-by-side
// so a future refactor can switch Apply to consume Plan output
// without changing VM shape.
func buildVMManifest(clusterName, nodeName string, i, cpCount int, size k3sresources.DefaultSizeSpec, staticIP string, spec *k3sresources.ClusterSpec) *protocol.Resource {
	var ipConfig map[string]any
	if staticIP != "" && spec.Network.StaticIPs != nil {
		ipConfig = map[string]any{
			"net0": map[string]any{
				"ip":      fmt.Sprintf("%s/%s", staticIP, spec.Network.StaticIPs.Netmask),
				"gateway": spec.Network.StaticIPs.Gateway,
			},
		}
	} else {
		ipConfig = map[string]any{"net0": map[string]any{"ip": "dhcp"}}
	}
	vm := &protocol.Resource{
		APIVersion: fmt.Sprintf("%s.openctl.io/v1", spec.Compute.Provider),
		Kind:       "VirtualMachine",
		Metadata: protocol.ResourceMetadata{
			Name: nodeName,
			Labels: map[string]string{
				"k3s.openctl.io/cluster": clusterName,
				"k3s.openctl.io/role":    roleForIndex(i, cpCount),
				providers.LabelOwnerKind: kindCluster,
				providers.LabelOwnerName: clusterName,
			},
		},
		Spec: map[string]any{
			"startOnCreate": true,
			"agent":         map[string]any{"enabled": true},
			"cpu":           map[string]any{"cores": size.CPUs},
			"memory":        map[string]any{"size": size.MemoryMB},
			"disks": []map[string]any{
				{"name": "scsi0", "size": fmt.Sprintf("%dG", size.DiskGB)},
			},
			"networks": []map[string]any{
				{"name": "net0", "bridge": spec.Network.Bridge, "model": "virtio"},
			},
			"cloudInit": map[string]any{
				"user":     spec.SSH.User,
				"sshKeys":  spec.SSH.PublicKeys,
				"ipConfig": ipConfig,
			},
		},
	}
	if spec.Compute.Image.URL != "" {
		cloudImage := map[string]any{"url": spec.Compute.Image.URL}
		if spec.Compute.Image.Storage != "" {
			cloudImage["storage"] = spec.Compute.Image.Storage
		}
		if spec.Compute.Image.DiskStorage != "" {
			cloudImage["diskStorage"] = spec.Compute.Image.DiskStorage
		}
		vm.Spec["cloudImage"] = cloudImage
	} else if spec.Compute.Image.Template != "" {
		vm.Spec["template"] = map[string]any{"name": spec.Compute.Image.Template}
	}
	return vm
}

func buildK3sNodeManifest(clusterName, nodeName, role, firstCPName string, spec *k3sresources.ClusterSpec) *protocol.Resource {
	k3s := &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       kindK3sNode,
		Metadata: protocol.ResourceMetadata{
			Name: nodeName,
			Labels: map[string]string{
				providers.LabelOwnerKind: kindCluster,
				providers.LabelOwnerName: clusterName,
			},
		},
		Spec: map[string]any{
			"vmRef": map[string]any{
				"$ref": map[string]any{
					"apiVersion": spec.Compute.Provider + ".openctl.io/v1",
					"kind":       "VirtualMachine",
					"name":       nodeName,
				},
			},
			"role": role,
			// joinFrom / joinURLFrom: the first server has these
			// deleted by the caller. For every other node, they
			// point at the first CP's K3sNode. The resolver reads
			// status.nodeToken + status.vmIP from that resource, so
			// install order is naturally serialized.
			"joinFrom": map[string]any{
				"$ref": map[string]any{
					"apiVersion": "k3s.openctl.io/v1",
					"kind":       kindK3sNode,
					"name":       firstCPName,
					"field":      "status.nodeToken",
				},
			},
			"joinURLFrom": map[string]any{
				"$ref": map[string]any{
					"apiVersion": "k3s.openctl.io/v1",
					"kind":       kindK3sNode,
					"name":       firstCPName,
					"field":      "status.vmIP",
				},
			},
			"ssh": map[string]any{
				"user":           spec.SSH.User,
				"privateKeyPath": spec.SSH.PrivateKeyPath,
			},
		},
	}
	if spec.K3s.Version != "" {
		k3s.Spec["version"] = spec.K3s.Version
	}
	if len(spec.K3s.ExtraArgs) > 0 {
		extra := make([]any, len(spec.K3s.ExtraArgs))
		for i, a := range spec.K3s.ExtraArgs {
			extra[i] = a
		}
		k3s.Spec["extraArgs"] = extra
	}
	return k3s
}

func buildAgentInstallManifest(clusterName, nodeName string, spec *k3sresources.ClusterSpec) *protocol.Resource {
	return &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       kindAgentInstall,
		Metadata: protocol.ResourceMetadata{
			Name: nodeName + "-agent",
			Labels: map[string]string{
				providers.LabelOwnerKind: kindCluster,
				providers.LabelOwnerName: clusterName,
			},
		},
		Spec: map[string]any{
			"vmRef": map[string]any{
				"$ref": map[string]any{
					"apiVersion": spec.Compute.Provider + ".openctl.io/v1",
					"kind":       "VirtualMachine",
					"name":       nodeName,
				},
			},
			"clusterName": clusterName,
			"ssh": map[string]any{
				"user":           spec.SSH.User,
				"privateKeyPath": spec.SSH.PrivateKeyPath,
			},
		},
	}
}

func roleForIndex(i, cpCount int) string {
	if i < cpCount {
		return "control-plane"
	}
	return "worker"
}
