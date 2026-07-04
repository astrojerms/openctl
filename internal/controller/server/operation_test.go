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
	h := newOperationHandler(store, nil)
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

	h := newOperationHandler(store, nil)
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

func TestCancelOperationCancelsPending(t *testing.T) {
	store := openOpStore(t)
	ctx := context.Background()
	op, err := store.Submit(ctx, &operations.Operation{
		Type:         operations.TypeApply,
		APIVersion:   "p.openctl.io/v1",
		Kind:         "VM",
		ResourceName: "x",
		ManifestJSON: `{"apiVersion":"p.openctl.io/v1","kind":"VM"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := newOperationHandler(store, nil)
	resp, err := h.CancelOperation(ctx, &apiv1.CancelOperationRequest{Id: op.ID})
	if err != nil {
		t.Fatalf("CancelOperation: %v", err)
	}
	if resp.GetStatus() != operations.StatusCancelled {
		t.Errorf("status = %q, want cancelled", resp.GetStatus())
	}

	// GetOperation also includes the submitted manifest now.
	got, err := h.GetOperation(ctx, &apiv1.GetOperationRequest{Id: op.ID})
	if err != nil {
		t.Fatalf("GetOperation: %v", err)
	}
	if got.GetManifestJson() == "" {
		t.Error("ManifestJson should be populated on GetOperation")
	}
	if got.GetStatus() != operations.StatusCancelled {
		t.Errorf("Get status = %q, want cancelled", got.GetStatus())
	}
}

func TestCancelOperationRefusesRunningOp(t *testing.T) {
	store := openOpStore(t)
	ctx := context.Background()
	op, _ := store.Submit(ctx, &operations.Operation{
		Type: operations.TypeApply, APIVersion: "p.openctl.io/v1", Kind: "VM", ResourceName: "x",
	})
	// Move to running.
	if _, err := store.ClaimNextPending(ctx); err != nil {
		t.Fatal(err)
	}
	h := newOperationHandler(store, nil)
	_, err := h.CancelOperation(ctx, &apiv1.CancelOperationRequest{Id: op.ID})
	if err == nil {
		t.Fatal("want FailedPrecondition, got nil")
	}
}

func TestCancelOperationMissingReturnsNotFound(t *testing.T) {
	store := openOpStore(t)
	h := newOperationHandler(store, nil)
	_, err := h.CancelOperation(context.Background(), &apiv1.CancelOperationRequest{Id: "op-missing"})
	if err == nil {
		t.Fatal("want NotFound, got nil")
	}
}

type fakeCanceler struct {
	called []string
	result bool
}

func (f *fakeCanceler) CancelRunning(id string) bool {
	f.called = append(f.called, id)
	return f.result
}

// With a canceler wired, canceling a running op requests cooperative
// cancellation and succeeds (the op transitions to canceled asynchronously).
func TestCancelOperationRequestsRunningCancel(t *testing.T) {
	store := openOpStore(t)
	ctx := context.Background()
	op, _ := store.Submit(ctx, &operations.Operation{
		Type: operations.TypeApply, APIVersion: "p.openctl.io/v1", Kind: "VM", ResourceName: "x",
	})
	if _, err := store.ClaimNextPending(ctx); err != nil {
		t.Fatal(err)
	}
	fc := &fakeCanceler{result: true}
	h := newOperationHandler(store, fc)
	resp, err := h.CancelOperation(ctx, &apiv1.CancelOperationRequest{Id: op.ID})
	if err != nil {
		t.Fatalf("CancelOperation on running op with canceler: %v", err)
	}
	if len(fc.called) != 1 || fc.called[0] != op.ID {
		t.Errorf("CancelRunning calls = %v, want [%s]", fc.called, op.ID)
	}
	if resp.GetStatus() != operations.StatusRunning {
		t.Errorf("status = %q, want running (async transition to canceled)", resp.GetStatus())
	}
}

// If the op is no longer running by the time we try (canceler returns false),
// fall back to FailedPrecondition rather than reporting a phantom cancel.
func TestCancelOperationRunningCancelRaceFallsBack(t *testing.T) {
	store := openOpStore(t)
	ctx := context.Background()
	op, _ := store.Submit(ctx, &operations.Operation{
		Type: operations.TypeApply, APIVersion: "p.openctl.io/v1", Kind: "VM", ResourceName: "x",
	})
	if _, err := store.ClaimNextPending(ctx); err != nil {
		t.Fatal(err)
	}
	fc := &fakeCanceler{result: false}
	h := newOperationHandler(store, fc)
	if _, err := h.CancelOperation(ctx, &apiv1.CancelOperationRequest{Id: op.ID}); err == nil {
		t.Fatal("want FailedPrecondition when canceler reports not-running")
	}
}
