package refs

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

// stubGetter fakes provider.Get for tests. Store fixtures keyed by
// "apiVersion/kind/name"; missing keys return the same NotFound shape
// providers use (a bare error is fine — the resolver treats any error
// as "unresolvable").
type stubGetter struct {
	data map[string]*protocol.Resource
}

func (s *stubGetter) Get(_ context.Context, av, kind, name string) (*protocol.Resource, error) {
	if r, ok := s.data[av+"/"+kind+"/"+name]; ok {
		return r, nil
	}
	return nil, errors.New("not found")
}

func TestResolvePassesThroughMapsWithoutRefs(t *testing.T) {
	r := New(&stubGetter{})
	in := map[string]any{
		"a": 1,
		"b": "two",
		"c": []any{1, 2, 3},
		"d": map[string]any{"nested": true},
	}
	out, err := r.Resolve(context.Background(), in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("no-ref input should pass through unchanged\n got: %+v\nwant: %+v", out, in)
	}
}

// TestResolveHelmReleaseClusterKubeconfig pins the deployment-model Phase 2
// cross-layer path: a HelmRelease resolves the kubeconfig path from a k3s
// Cluster's nested status.outputs.kubeconfigPath. Confirms the k8s provider can
// target an openctl-managed cluster by $ref with no new machinery.
func TestResolveHelmReleaseClusterKubeconfig(t *testing.T) {
	g := &stubGetter{data: map[string]*protocol.Resource{
		"k3s.openctl.io/v1/Cluster/edge": {
			APIVersion: "k3s.openctl.io/v1",
			Kind:       "Cluster",
			Metadata:   protocol.ResourceMetadata{Name: "edge"},
			Status: map[string]any{
				"outputs": map[string]any{
					"kubeconfigPath": "/home/u/.openctl/k3s/edge-cp-0/kubeconfig",
				},
			},
		},
	}}
	in := map[string]any{
		"kubeconfigPath": map[string]any{
			"$ref": map[string]any{
				"apiVersion": "k3s.openctl.io/v1",
				"kind":       "Cluster",
				"name":       "edge",
				"field":      "status.outputs.kubeconfigPath",
			},
		},
		"chart": map[string]any{"repo": "https://x/charts", "name": "podinfo"},
	}
	out, err := New(g).Resolve(context.Background(), in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := out["kubeconfigPath"]; got != "/home/u/.openctl/k3s/edge-cp-0/kubeconfig" {
		t.Errorf("resolved kubeconfigPath = %v, want the cluster's status path", got)
	}
}

func TestResolveSubstitutesFieldRef(t *testing.T) {
	g := &stubGetter{data: map[string]*protocol.Resource{
		"k3s.openctl.io/v1/K3sNode/cp-0": {
			APIVersion: "k3s.openctl.io/v1",
			Kind:       "K3sNode",
			Metadata:   protocol.ResourceMetadata{Name: "cp-0"},
			Status:     map[string]any{"nodeToken": "K10::server:secret"},
		},
	}}
	r := New(g)
	in := map[string]any{
		"cluster": map[string]any{
			"joinToken": map[string]any{
				"$ref": map[string]any{
					"apiVersion": "k3s.openctl.io/v1",
					"kind":       "K3sNode",
					"name":       "cp-0",
					"field":      "status.nodeToken",
				},
			},
		},
	}
	out, err := r.Resolve(context.Background(), in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := out["cluster"].(map[string]any)["joinToken"]
	if got != "K10::server:secret" {
		t.Errorf("joinToken = %v, want K10::server:secret", got)
	}
}

func TestResolveWholeResource(t *testing.T) {
	g := &stubGetter{data: map[string]*protocol.Resource{
		"proxmox.openctl.io/v1/VirtualMachine/vm-a": {
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "vm-a"},
			Spec:       map[string]any{"node": "pve1"},
			Status:     map[string]any{"ip": "192.168.1.10"},
		},
	}}
	r := New(g)
	in := map[string]any{
		"vmRef": map[string]any{
			"$ref": map[string]any{
				"apiVersion": "proxmox.openctl.io/v1",
				"kind":       "VirtualMachine",
				"name":       "vm-a",
			},
		},
	}
	out, err := r.Resolve(context.Background(), in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	vm, ok := out["vmRef"].(map[string]any)
	if !ok {
		t.Fatalf("vmRef = %T, want map", out["vmRef"])
	}
	if vm["kind"] != "VirtualMachine" {
		t.Errorf("kind = %v, want VirtualMachine", vm["kind"])
	}
	status, _ := vm["status"].(map[string]any)
	if status["ip"] != "192.168.1.10" {
		t.Errorf("status.ip = %v, want 192.168.1.10", status["ip"])
	}
}

func TestResolveErrorsOnMissingRef(t *testing.T) {
	r := New(&stubGetter{data: map[string]*protocol.Resource{}})
	in := map[string]any{
		"joinToken": map[string]any{
			"$ref": map[string]any{
				"apiVersion": "k3s.openctl.io/v1",
				"kind":       "K3sNode",
				"name":       "does-not-exist",
				"field":      "status.nodeToken",
			},
		},
	}
	_, err := r.Resolve(context.Background(), in)
	if err == nil {
		t.Fatal("expected error for missing ref target, got nil")
	}
}

func TestResolveErrorsOnMissingField(t *testing.T) {
	g := &stubGetter{data: map[string]*protocol.Resource{
		"k3s.openctl.io/v1/K3sNode/cp-0": {
			APIVersion: "k3s.openctl.io/v1",
			Kind:       "K3sNode",
			Metadata:   protocol.ResourceMetadata{Name: "cp-0"},
			Status:     map[string]any{"ip": "1.2.3.4"}, // no nodeToken
		},
	}}
	r := New(g)
	in := map[string]any{
		"joinToken": map[string]any{
			"$ref": map[string]any{
				"apiVersion": "k3s.openctl.io/v1",
				"kind":       "K3sNode",
				"name":       "cp-0",
				"field":      "status.nodeToken",
			},
		},
	}
	_, err := r.Resolve(context.Background(), in)
	if err == nil {
		t.Fatal("expected error for missing field path, got nil")
	}
}

func TestResolveMalformedRefIsAnError(t *testing.T) {
	r := New(&stubGetter{})
	in := map[string]any{
		"broken": map[string]any{
			"$ref": map[string]any{
				"kind": "OnlyKindNoName",
			},
		},
	}
	_, err := r.Resolve(context.Background(), in)
	if err == nil {
		t.Fatal("expected error for malformed ref, got nil")
	}
}

func TestResolveTraversesArrays(t *testing.T) {
	g := &stubGetter{data: map[string]*protocol.Resource{
		"proxmox.openctl.io/v1/VirtualMachine/vm-a": {
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Metadata:   protocol.ResourceMetadata{Name: "vm-a"},
			Status:     map[string]any{"ip": "192.168.1.10"},
		},
	}}
	r := New(g)
	in := map[string]any{
		"nodes": []any{
			map[string]any{
				"ip": map[string]any{
					"$ref": map[string]any{
						"apiVersion": "proxmox.openctl.io/v1",
						"kind":       "VirtualMachine",
						"name":       "vm-a",
						"field":      "status.ip",
					},
				},
			},
		},
	}
	out, err := r.Resolve(context.Background(), in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	nodes := out["nodes"].([]any)
	first := nodes[0].(map[string]any)
	if first["ip"] != "192.168.1.10" {
		t.Errorf("nodes[0].ip = %v, want 192.168.1.10", first["ip"])
	}
}

func TestResolveNoRefsAtAll(t *testing.T) {
	// Sanity: pure-value input works without any Getter calls. Passing
	// a nil Getter proves nothing gets dereferenced when there's no ref.
	r := New(nil)
	in := map[string]any{"a": 1, "b": map[string]any{"c": "d"}}
	out, err := r.Resolve(context.Background(), in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Error("input should be unchanged")
	}
}

func TestCollectGathersAllRefsSorted(t *testing.T) {
	ref := func(av, kind, name, field string) map[string]any {
		inner := map[string]any{"apiVersion": av, "kind": kind, "name": name}
		if field != "" {
			inner["field"] = field
		}
		return map[string]any{RefMarker: inner}
	}
	spec := map[string]any{
		"joinFrom": ref("k3s.openctl.io/v1", "K3sNode", "cp-0", "status.nodeToken"),
		"vmRef":    ref("proxmox.openctl.io/v1", "VirtualMachine", "cp-1", ""),
		"nested": map[string]any{
			"list": []any{
				ref("k3s.openctl.io/v1", "K3sNode", "cp-0", "status.vmIP"),
				map[string]any{"plain": "value"},
			},
		},
	}
	got := Collect(spec)
	want := []Ref{
		{APIVersion: "k3s.openctl.io/v1", Kind: "K3sNode", Name: "cp-0", Field: "status.nodeToken"},
		{APIVersion: "k3s.openctl.io/v1", Kind: "K3sNode", Name: "cp-0", Field: "status.vmIP"},
		{APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine", Name: "cp-1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Collect() =\n  %+v\nwant\n  %+v", got, want)
	}
}

func TestCollectNilAndRefFreeSpecs(t *testing.T) {
	if got := Collect(nil); got != nil {
		t.Errorf("Collect(nil) = %+v, want nil", got)
	}
	if got := Collect(map[string]any{"a": 1, "b": map[string]any{"c": "d"}}); got != nil {
		t.Errorf("Collect(ref-free) = %+v, want nil", got)
	}
}

func TestCollectSkipsMalformedRefs(t *testing.T) {
	// A $ref missing required fields is skipped (best-effort), not an error.
	spec := map[string]any{
		"bad":  map[string]any{RefMarker: map[string]any{"kind": "K3sNode"}}, // no apiVersion/name
		"good": map[string]any{RefMarker: map[string]any{"apiVersion": "k3s.openctl.io/v1", "kind": "K3sNode", "name": "cp-0"}},
	}
	got := Collect(spec)
	want := []Ref{{APIVersion: "k3s.openctl.io/v1", Kind: "K3sNode", Name: "cp-0"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Collect() = %+v, want %+v", got, want)
	}
}
