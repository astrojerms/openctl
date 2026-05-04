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
