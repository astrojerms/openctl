package k3s

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/pkg/protocol"
)

// fakeVMs satisfies VMApplier for tests where we don't need real Proxmox.
type fakeVMs struct {
	deleted [][]string
}

func (f *fakeVMs) Apply(_ context.Context, _ *protocol.Resource) (*protocol.Resource, error) {
	return nil, nil
}
func (f *fakeVMs) Get(_ context.Context, _, _ string) (*protocol.Resource, error) {
	return nil, nil
}
func (f *fakeVMs) Delete(_ context.Context, kind, name string) error {
	f.deleted = append(f.deleted, []string{kind, name})
	return nil
}

func writeClusterState(t *testing.T, home, name, body string) {
	t.Helper()
	dir := filepath.Join(home, ".openctl", "state", "k3s")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProviderName(t *testing.T) {
	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	if p.Name() != "k3s" {
		t.Errorf("Name = %q, want k3s", p.Name())
	}
	if len(p.Kinds()) != 1 || p.Kinds()[0] != "Cluster" {
		t.Errorf("Kinds = %v, want [Cluster]", p.Kinds())
	}
}

func TestProviderRejectsWrongKind(t *testing.T) {
	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	if _, err := p.Apply(context.Background(), &protocol.Resource{Kind: "Other"}); err == nil {
		t.Error("Apply on wrong kind should error")
	}
	if _, err := p.Get(context.Background(), "Other", "x"); err == nil {
		t.Error("Get on wrong kind should error")
	}
}

func TestOwnerOfFindsClusterChild(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeClusterState(t, home, "dev", `apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: dev
children:
  - provider: proxmox
    kind: VirtualMachine
    name: dev-cp-0
  - provider: proxmox
    kind: VirtualMachine
    name: dev-worker-0
`)

	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	ownerKind, ownerName, owned := p.OwnerOf("VirtualMachine", "dev-cp-0")
	if !owned {
		t.Fatal("dev-cp-0 should be reported as owned")
	}
	if ownerKind != "Cluster" || ownerName != "dev" {
		t.Errorf("owner = %s/%s, want Cluster/dev", ownerKind, ownerName)
	}
}

func TestOwnerOfReturnsFalseForUnowned(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// No state files at all.
	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	if _, _, owned := p.OwnerOf("VirtualMachine", "freebird"); owned {
		t.Error("freebird VM should not be reported as owned (no state files)")
	}

	// A state file exists, but doesn't list this VM.
	writeClusterState(t, home, "other", `apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: other
children:
  - provider: proxmox
    kind: VirtualMachine
    name: other-cp-0
`)
	if _, _, owned := p.OwnerOf("VirtualMachine", "freebird"); owned {
		t.Error("freebird should not be owned by 'other' cluster")
	}
}

func TestGetReturnsNotFoundWhenStateMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	_, err := p.Get(context.Background(), "Cluster", "missing")
	if err == nil {
		t.Fatal("want providers.NotFoundError, got nil")
	}
	var nf *providers.NotFoundError
	if !errors.As(err, &nf) {
		t.Errorf("want NotFoundError, got %T: %v", err, err)
	}
}

func TestGetReturnsExistingClusterFromState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeClusterState(t, home, "dev", `apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: dev
spec:
  network:
    bridge: vmbr0
status:
  phase: Ready
`)

	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	r, err := p.Get(context.Background(), "Cluster", "dev")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if r.Status["phase"] != "Ready" {
		t.Errorf("phase = %v, want Ready", r.Status["phase"])
	}
}

func TestDeleteCascadesToChildVMs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeClusterState(t, home, "dev", `apiVersion: k3s.openctl.io/v1
kind: Cluster
metadata:
  name: dev
children:
  - provider: proxmox
    kind: VirtualMachine
    name: dev-cp-0
  - provider: proxmox
    kind: VirtualMachine
    name: dev-worker-0
`)

	vms := &fakeVMs{}
	p := New(&protocol.ProviderConfig{}, vms)
	if err := p.Delete(context.Background(), "Cluster", "dev"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(vms.deleted) != 2 {
		t.Errorf("expected 2 child VM deletes, got %d: %v", len(vms.deleted), vms.deleted)
	}
	// State file should be gone.
	if _, err := os.Stat(filepath.Join(home, ".openctl", "state", "k3s", "dev.yaml")); !os.IsNotExist(err) {
		t.Error("state file should be removed after delete")
	}
}

func TestDeleteOnMissingClusterIsIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := New(&protocol.ProviderConfig{}, &fakeVMs{})
	if err := p.Delete(context.Background(), "Cluster", "missing"); err != nil {
		t.Errorf("delete on missing should be idempotent, got %v", err)
	}
}
