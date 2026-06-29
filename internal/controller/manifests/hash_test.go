package manifests

import (
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

func TestHashStableAcrossEqualManifests(t *testing.T) {
	a := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata: protocol.ResourceMetadata{
			Name:   "vm-x",
			Labels: map[string]string{"env": "dev", "team": "platform"},
		},
		Spec: map[string]any{
			"cpu":    map[string]any{"cores": float64(2)},
			"memory": map[string]any{"size": float64(4096)},
		},
	}
	// Same data, different map insertion order.
	b := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata: protocol.ResourceMetadata{
			Name:   "vm-x",
			Labels: map[string]string{"team": "platform", "env": "dev"},
		},
		Spec: map[string]any{
			"memory": map[string]any{"size": float64(4096)},
			"cpu":    map[string]any{"cores": float64(2)},
		},
	}
	if Hash(a) != Hash(b) {
		t.Errorf("hashes should match for semantically-equal manifests\nA=%s\nB=%s", Hash(a), Hash(b))
	}
}

func TestHashChangesWhenSpecChanges(t *testing.T) {
	base := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata:   protocol.ResourceMetadata{Name: "vm-x"},
		Spec:       map[string]any{"cpu": map[string]any{"cores": float64(2)}},
	}
	mod := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata:   protocol.ResourceMetadata{Name: "vm-x"},
		Spec:       map[string]any{"cpu": map[string]any{"cores": float64(4)}},
	}
	if Hash(base) == Hash(mod) {
		t.Error("hashes should differ when spec.cpu.cores changes")
	}
}

func TestHashIgnoresAnnotations(t *testing.T) {
	base := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata: protocol.ResourceMetadata{
			Name:        "vm-x",
			Annotations: map[string]string{"openctl.io/allow-destructive": "true"},
		},
		Spec: map[string]any{"cpu": map[string]any{"cores": float64(2)}},
	}
	noAnno := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata:   protocol.ResourceMetadata{Name: "vm-x"},
		Spec:       map[string]any{"cpu": map[string]any{"cores": float64(2)}},
	}
	if Hash(base) != Hash(noAnno) {
		t.Error("hashes should ignore annotations (runtime flags, not desired state)")
	}
}

func TestHashIncludesLabels(t *testing.T) {
	a := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata:   protocol.ResourceMetadata{Name: "vm-x", Labels: map[string]string{"env": "dev"}},
		Spec:       map[string]any{"x": float64(1)},
	}
	b := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata:   protocol.ResourceMetadata{Name: "vm-x", Labels: map[string]string{"env": "prod"}},
		Spec:       map[string]any{"x": float64(1)},
	}
	if Hash(a) == Hash(b) {
		t.Error("hashes should differ when labels change")
	}
}

func TestHashDifferentKinds(t *testing.T) {
	a := &protocol.Resource{APIVersion: "v1", Kind: "VirtualMachine", Metadata: protocol.ResourceMetadata{Name: "x"}}
	b := &protocol.Resource{APIVersion: "v1", Kind: "Cluster", Metadata: protocol.ResourceMetadata{Name: "x"}}
	if Hash(a) == Hash(b) {
		t.Error("hashes should differ when kind changes even if everything else is equal")
	}
}

func TestHashOfNilIsEmpty(t *testing.T) {
	if Hash(nil) != "" {
		t.Error("Hash(nil) should return \"\"")
	}
}
