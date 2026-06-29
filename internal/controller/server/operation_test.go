package server

import (
	"context"
	"path/filepath"
	"testing"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"

	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/internal/controller/storage"
)

func openOpStore(t *testing.T) *operations.Store {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return operations.New(db, 50)
}

func TestGetOperationOmitsChildrenByDefault(t *testing.T) {
	store := openOpStore(t)
	ctx := context.Background()
	parent, _ := store.Submit(ctx, &operations.Operation{
		Type: operations.TypeApply, APIVersion: "k3s.openctl.io/v1", Kind: "Cluster", ResourceName: "dev",
	})
	if _, err := store.BeginChild(ctx, parent.ID, &operations.Operation{
		Type: operations.TypeApply, APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine", ResourceName: "dev-cp-0",
	}); err != nil {
		t.Fatal(err)
	}
	h := newOperationHandler(store)
	got, err := h.GetOperation(ctx, &apiv1.GetOperationRequest{Id: parent.ID})
	if err != nil {
		t.Fatalf("GetOperation: %v", err)
	}
	if len(got.GetChildren()) != 0 {
		t.Errorf("children = %d, want 0 (include_children false)", len(got.GetChildren()))
	}
}

func TestGetOperationIncludesChildrenWhenRequested(t *testing.T) {
	store := openOpStore(t)
	ctx := context.Background()
	parent, _ := store.Submit(ctx, &operations.Operation{
		Type: operations.TypeApply, APIVersion: "k3s.openctl.io/v1", Kind: "Cluster", ResourceName: "dev",
	})
	for _, name := range []string{"dev-cp-0", "dev-worker-0"} {
		ch, err := store.BeginChild(ctx, parent.ID, &operations.Operation{
			Type: operations.TypeApply, APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine", ResourceName: name,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.EndChild(ctx, ch.ID, operations.StatusSucceeded, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	step, _ := store.BeginChild(ctx, parent.ID, &operations.Operation{
		Type: operations.TypeStep, APIVersion: "k3s.openctl.io/v1", Kind: "Cluster",
		ResourceName: "dev/install-k3s", Label: "Install k3s",
	})
	_ = store.EndChild(ctx, step.ID, operations.StatusSucceeded, "", "")

	h := newOperationHandler(store)
	got, err := h.GetOperation(ctx, &apiv1.GetOperationRequest{
		Id:              parent.ID,
		IncludeChildren: true,
	})
	if err != nil {
		t.Fatalf("GetOperation: %v", err)
	}
	if len(got.GetChildren()) != 3 {
		t.Fatalf("children = %d, want 3", len(got.GetChildren()))
	}
	// Children come back oldest-first; step row carries its label.
	last := got.GetChildren()[2]
	if last.GetType() != operations.TypeStep || last.GetLabel() != "Install k3s" {
		t.Errorf("last child = type=%q label=%q, want type=step label=\"Install k3s\"", last.GetType(), last.GetLabel())
	}
}
