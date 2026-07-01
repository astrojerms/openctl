package k3s

import (
	"context"
	"encoding/json"
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
	// node joins the first CP. When staticIPs are configured, the
	// IP is baked into the K3sNode spec directly — otherwise the
	// K3sNode's applyK3sNode polls the VM provider's status.ip via
	// QGA, which requires qemu-guest-agent inside the guest.
	for i, nodeName := range allNodes {
		role := "server"
		if i >= len(cpNodes) {
			role = "agent"
		}
		k3sNode := buildK3sNodeManifest(clusterName, nodeName, role, firstCP, nodeIPs[nodeName], spec)
		if i == 0 && role == "server" {
			// First server initializes — no join fields.
			delete(k3sNode.Spec, "joinFrom")
			delete(k3sNode.Spec, "joinURLFrom")
		}
		children = append(children, k3sNode)
	}

	// One AgentInstall per node — all point at the same cluster.
	// Same static-IP pass-through as K3sNode.
	for _, nodeName := range allNodes {
		children = append(children, buildAgentInstallManifest(clusterName, nodeName, nodeIPs[nodeName], spec))
	}

	// Normalize every child's Spec through JSON round-trip. When
	// Plan returns these to a ChildDispatcher that runs in-process
	// (no wire protocol), Go's static types leak: `[]string` stays
	// `[]string`, `[]map[string]any` stays `[]map[string]any`, etc.
	// Downstream parsers (proxmox.ParseVMSpec, refs.Resolver, ...)
	// do `.([]any)` and `.(map[string]any)` assertions that silently
	// fail on those static types, dropping the field.
	//
	// This bit us on the homelab validation: the plan's VM manifest
	// carried `cloudInit.sshKeys = []string{...}` and
	// `disks = []map[string]any{...}`. ParseVMSpec's `[]any` type
	// assertions failed, so both were dropped. The cloned VM kept
	// the template's baked-in RSA key (SSH auth failed) and the
	// template's base disk size (apt install ran out of space).
	// Networks had the same shape and were similarly ignored.
	//
	// Round-tripping through JSON collapses every typed collection
	// to its `any`-flavored equivalent, matching what the standard
	// wire path (gRPC / disk mirror) produces. One normalization at
	// emission fixes every field of this shape at once, present and
	// future.
	for _, c := range children {
		if err := normalizeSpec(c); err != nil {
			return nil, fmt.Errorf("normalize plan child %s/%s: %w", c.Kind, c.Metadata.Name, err)
		}
	}

	return &providers.PlanResult{Children: children}, nil
}

// normalizeSpec JSON-round-trips r.Spec so all typed collections
// (`[]string`, `[]map[string]any`, custom structs) become the
// `map[string]any` / `[]any` shapes downstream parsers expect. Only
// meaningful when the manifest travels in-process — the wire path
// already does this via proto marshaling.
func normalizeSpec(r *protocol.Resource) error {
	if r == nil || r.Spec == nil {
		return nil
	}
	data, err := json.Marshal(r.Spec)
	if err != nil {
		return err
	}
	var normalized map[string]any
	if err := json.Unmarshal(data, &normalized); err != nil {
		return err
	}
	r.Spec = normalized
	return nil
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

// buildK3sNodeManifest emits one K3sNode child. staticIP is the
// deterministic allocation from AllocateIPs — non-empty when
// spec.network.staticIPs is set, empty when the cluster is DHCP.
// When non-empty, `spec.vmIP` is baked into the manifest so
// applyK3sNode can skip the QGA-based waitForVMIP loop that
// otherwise polls the VM provider for status.ip.
func buildK3sNodeManifest(clusterName, nodeName, role, firstCPName, staticIP string, spec *k3sresources.ClusterSpec) *protocol.Resource {
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
	if staticIP != "" {
		k3s.Spec["vmIP"] = staticIP
	}
	return k3s
}

// buildAgentInstallManifest emits one AgentInstall child. staticIP
// has the same static-IP-pass-through semantics as buildK3sNodeManifest.
func buildAgentInstallManifest(clusterName, nodeName, staticIP string, spec *k3sresources.ClusterSpec) *protocol.Resource {
	agent := &protocol.Resource{
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
	if staticIP != "" {
		agent.Spec["vmIP"] = staticIP
	}
	return agent
}

func roleForIndex(i, cpCount int) string {
	if i < cpCount {
		return "control-plane"
	}
	return "worker"
}
