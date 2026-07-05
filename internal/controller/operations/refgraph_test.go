package operations

import (
	"reflect"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

func ref(apiVersion, kind, name, field string) map[string]any {
	inner := map[string]any{"apiVersion": apiVersion, "kind": kind, "name": name}
	if field != "" {
		inner["field"] = field
	}
	return map[string]any{"$ref": inner}
}

func TestChildKey(t *testing.T) {
	r := &protocol.Resource{Kind: "VirtualMachine", Metadata: protocol.ResourceMetadata{Name: "dev-cp-0"}}
	if got := ChildKey(r); got != "VirtualMachine/dev-cp-0" {
		t.Errorf("ChildKey = %q", got)
	}
}

// TestRefChildEdges_DerivesFromRefs mirrors the k3s plan shape: a joining
// K3sNode refs its VM (whole-resource) and the first control-plane node (for
// the join token), so it depends on both.
func TestRefChildEdges_DerivesFromRefs(t *testing.T) {
	children := []*protocol.Resource{
		{Kind: "VirtualMachine", Metadata: protocol.ResourceMetadata{Name: "w0"}},
		{Kind: "K3sNode", Metadata: protocol.ResourceMetadata{Name: "cp-0"}},
		{
			Kind:     "K3sNode",
			Metadata: protocol.ResourceMetadata{Name: "w0"},
			Spec: map[string]any{
				"vmRef":       ref("proxmox.openctl.io/v1", "VirtualMachine", "w0", ""),
				"joinFrom":    ref("k3s.openctl.io/v1", "K3sNode", "cp-0", "status.nodeToken"),
				"joinURLFrom": ref("k3s.openctl.io/v1", "K3sNode", "cp-0", "status.vmIP"),
			},
		},
	}
	edges := RefChildEdges(children)

	// w0's K3sNode depends on its VM and cp-0 (deduped despite two refs to cp-0).
	got := edges["K3sNode/w0"]
	want := []string{"K3sNode/cp-0", "VirtualMachine/w0"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edges[K3sNode/w0] = %v, want %v", got, want)
	}
	// The VM and cp-0 have no ref deps.
	if _, ok := edges["VirtualMachine/w0"]; ok {
		t.Errorf("VM should have no deps, got %v", edges["VirtualMachine/w0"])
	}
	if _, ok := edges["K3sNode/cp-0"]; ok {
		t.Errorf("cp-0 should have no deps, got %v", edges["K3sNode/cp-0"])
	}
}

// TestRefChildEdges_IgnoresExternalAndSelf: refs to resources outside the
// child set impose no intra-plan ordering, and a self-ref is dropped.
func TestRefChildEdges_IgnoresExternalAndSelf(t *testing.T) {
	children := []*protocol.Resource{
		{
			Kind:     "K3sNode",
			Metadata: protocol.ResourceMetadata{Name: "solo"},
			Spec: map[string]any{
				"external": ref("other.openctl.io/v1", "Thing", "elsewhere", "status.x"),
				"selfish":  ref("k3s.openctl.io/v1", "K3sNode", "solo", "status.y"),
			},
		},
	}
	if edges := RefChildEdges(children); len(edges) != 0 {
		t.Errorf("want no edges (external + self ignored), got %v", edges)
	}
}
