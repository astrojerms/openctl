package providers

import (
	"context"
	"strings"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

type fakeProvider struct {
	name  string
	kinds []string
}

func (f *fakeProvider) Name() string    { return f.name }
func (f *fakeProvider) Kinds() []string { return f.kinds }
func (f *fakeProvider) Apply(_ context.Context, m *protocol.Resource) (*protocol.Resource, error) {
	return m, nil
}
func (f *fakeProvider) Get(_ context.Context, _, _ string) (*protocol.Resource, error) {
	return nil, nil
}
func (f *fakeProvider) List(_ context.Context, _ string) ([]*protocol.Resource, error) {
	return nil, nil
}
func (f *fakeProvider) Delete(_ context.Context, _, _ string) error { return nil }

func TestRegistryRegisterAndLookup(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeProvider{name: "proxmox", kinds: []string{"VirtualMachine"}})
	r.Register(&fakeProvider{name: "k3s", kinds: []string{"Cluster"}})

	cases := []struct {
		apiVersion string
		want       string
		wantErr    bool
	}{
		{"proxmox.openctl.io/v1", "proxmox", false},
		{"k3s.openctl.io/v1", "k3s", false},
		{"unknown.openctl.io/v1", "", true},
		{"no-dot", "", true},
		{"", "", true},
	}

	for _, c := range cases {
		t.Run(c.apiVersion, func(t *testing.T) {
			p, err := r.For(c.apiVersion)
			if c.wantErr {
				if err == nil {
					t.Errorf("want error, got %s", p.Name())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p.Name() != c.want {
				t.Errorf("Name = %q, want %q", p.Name(), c.want)
			}
		})
	}
}

func TestRegistryDuplicateRegistrationPanics(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeProvider{name: "x"})
	defer func() {
		if recover() == nil {
			t.Error("want panic on duplicate Register, got none")
		}
	}()
	r.Register(&fakeProvider{name: "x"})
}

func TestRegistryForListsKnownProviders(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeProvider{name: "proxmox"})
	r.Register(&fakeProvider{name: "k3s"})

	_, err := r.For("aws.openctl.io/v1")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "proxmox") || !strings.Contains(msg, "k3s") {
		t.Errorf("error should list known providers, got: %s", msg)
	}
}

// relationshipProvider implements OwnershipChecker + ChildrenLister so the
// Registry helpers can be exercised end-to-end.
type relationshipProvider struct {
	fakeProvider
	owns     map[string]string // "kind:name" → owner name (kind is implicit)
	children map[string][]ResourceRef
}

func (r *relationshipProvider) OwnerOf(kind, name string) (string, string, bool) {
	if owner, ok := r.owns[kind+":"+name]; ok {
		return "Cluster", owner, true
	}
	return "", "", false
}

func (r *relationshipProvider) ChildrenOf(kind, name string) []ResourceRef {
	return r.children[kind+":"+name]
}

func TestRegistryChildrenOfAggregatesAcrossProviders(t *testing.T) {
	r := NewRegistry()
	r.Register(&relationshipProvider{
		fakeProvider: fakeProvider{name: "k3s", kinds: []string{"Cluster"}},
		children: map[string][]ResourceRef{
			"Cluster:dev": {
				{APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine", Name: "dev-cp-0"},
				{APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine", Name: "dev-w-0"},
			},
		},
	})
	r.Register(&fakeProvider{name: "proxmox", kinds: []string{"VirtualMachine"}})

	got := r.ChildrenOf("Cluster", "dev")
	if len(got) != 2 {
		t.Fatalf("ChildrenOf len = %d, want 2: %+v", len(got), got)
	}
	if got[0].Name != "dev-cp-0" || got[0].APIVersion != "proxmox.openctl.io/v1" {
		t.Errorf("got[0] = %+v", got[0])
	}
	if r.ChildrenOf("Cluster", "missing") != nil {
		t.Error("ChildrenOf for unknown name should be nil")
	}
}

func TestRegistryOwnerRefOfDerivesAPIVersionFromProvider(t *testing.T) {
	r := NewRegistry()
	r.Register(&relationshipProvider{
		fakeProvider: fakeProvider{name: "k3s", kinds: []string{"Cluster"}},
		owns: map[string]string{
			"VirtualMachine:dev-cp-0": "dev",
		},
	})
	r.Register(&fakeProvider{name: "proxmox", kinds: []string{"VirtualMachine"}})

	ref, ok := r.OwnerRefOf("VirtualMachine", "dev-cp-0")
	if !ok {
		t.Fatal("OwnerRefOf returned ok=false for owned resource")
	}
	want := ResourceRef{APIVersion: "k3s.openctl.io/v1", Kind: "Cluster", Name: "dev"}
	if ref != want {
		t.Errorf("OwnerRefOf = %+v, want %+v", ref, want)
	}

	if _, ok := r.OwnerRefOf("VirtualMachine", "freebird"); ok {
		t.Error("OwnerRefOf should return ok=false for unowned resource")
	}
}
