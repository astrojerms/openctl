package operations

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl/internal/controller/storage"
)

func openStore(t *testing.T, retain int) *Store {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db, retain)
}

func TestSubmitInsertsPendingOpWithGeneratedID(t *testing.T) {
	s := openStore(t, 50)

	op, err := s.Submit(context.Background(), &Operation{
		Type:         TypeApply,
		APIVersion:   "proxmox.openctl.io/v1",
		Kind:         "VirtualMachine",
		ResourceName: "web-01",
		ManifestJSON: `{"foo":"bar"}`,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if op.ID == "" || op.ID[:3] != "op-" {
		t.Errorf("ID = %q, want op-... prefix", op.ID)
	}
	if op.Status != StatusPending {
		t.Errorf("Status = %q, want pending", op.Status)
	}
	if op.SubmittedAt == "" {
		t.Error("SubmittedAt is empty")
	}

	got, err := s.Get(context.Background(), op.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ManifestJSON != `{"foo":"bar"}` {
		t.Errorf("manifest mismatch: %q", got.ManifestJSON)
	}
}

func TestSubmitFailsFastOnInflightOpForSameResource(t *testing.T) {
	s := openStore(t, 50)
	ctx := context.Background()

	first, err := s.Submit(ctx, &Operation{
		Type: TypeApply, APIVersion: "p.openctl.io/v1", Kind: "VM", ResourceName: "x",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Submit(ctx, &Operation{
		Type: TypeApply, APIVersion: "p.openctl.io/v1", Kind: "VM", ResourceName: "x",
	})
	if err == nil {
		t.Fatal("want ConflictError, got nil")
	}
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("want *ConflictError, got %T: %v", err, err)
	}
	if ce.InflightID != first.ID {
		t.Errorf("inflight ID = %q, want %q", ce.InflightID, first.ID)
	}
}

func TestSubmitAllowsConcurrentOpsOnDifferentResources(t *testing.T) {
	s := openStore(t, 50)
	ctx := context.Background()

	if _, err := s.Submit(ctx, &Operation{
		Type: TypeApply, APIVersion: "p.openctl.io/v1", Kind: "VM", ResourceName: "a",
	}); err != nil {
		t.Fatal(err)
	}
	// Different resource name → should succeed.
	if _, err := s.Submit(ctx, &Operation{
		Type: TypeApply, APIVersion: "p.openctl.io/v1", Kind: "VM", ResourceName: "b",
	}); err != nil {
		t.Errorf("submit on different resource should succeed, got: %v", err)
	}
}

func TestClaimNextPendingMarksRunning(t *testing.T) {
	s := openStore(t, 50)
	ctx := context.Background()

	if _, err := s.Submit(ctx, &Operation{
		Type: TypeApply, APIVersion: "p.openctl.io/v1", Kind: "VM", ResourceName: "x",
	}); err != nil {
		t.Fatal(err)
	}

	op, err := s.ClaimNextPending(ctx)
	if err != nil {
		t.Fatalf("ClaimNextPending: %v", err)
	}
	if op.Status != StatusRunning {
		t.Errorf("Status = %q, want running", op.Status)
	}
	if op.StartedAt == "" {
		t.Error("StartedAt empty")
	}

	// A second claim should find no pending ops.
	_, err = s.ClaimNextPending(ctx)
	if err == nil {
		t.Error("second ClaimNextPending should return error (no pending)")
	}
}

func TestCompleteWritesTerminalStatus(t *testing.T) {
	s := openStore(t, 50)
	ctx := context.Background()

	op, _ := s.Submit(ctx, &Operation{
		Type: TypeApply, APIVersion: "p.openctl.io/v1", Kind: "VM", ResourceName: "x",
	})
	_, _ = s.ClaimNextPending(ctx)

	if err := s.Complete(ctx, op.ID, StatusSucceeded, "", `{"ok":true}`); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	got, _ := s.Get(ctx, op.ID)
	if got.Status != StatusSucceeded {
		t.Errorf("Status = %q, want succeeded", got.Status)
	}
	if got.ResultJSON != `{"ok":true}` {
		t.Errorf("ResultJSON = %q", got.ResultJSON)
	}
	if got.CompletedAt == "" {
		t.Error("CompletedAt empty")
	}
	if !got.IsTerminal() {
		t.Error("IsTerminal should be true")
	}
}

func TestMarkRunningInterruptedRewritesAllRunning(t *testing.T) {
	s := openStore(t, 50)
	ctx := context.Background()

	for _, name := range []string{"a", "b"} {
		if _, err := s.Submit(ctx, &Operation{
			Type: TypeApply, APIVersion: "p.openctl.io/v1", Kind: "VM", ResourceName: name,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Mark both as running.
	if _, err := s.ClaimNextPending(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNextPending(ctx); err != nil {
		t.Fatal(err)
	}

	n, err := s.MarkRunningInterrupted(ctx)
	if err != nil {
		t.Fatalf("MarkRunningInterrupted: %v", err)
	}
	if n != 2 {
		t.Errorf("rewrote %d ops, want 2", n)
	}
	ops, _ := s.List(ctx, ListFilter{Status: StatusInterrupted})
	if len(ops) != 2 {
		t.Errorf("interrupted ops = %d, want 2", len(ops))
	}
}

func TestGCKeepsLatestN(t *testing.T) {
	const retain = 3
	s := openStore(t, retain)
	ctx := context.Background()

	// Submit + complete 5 ops for the same resource. Each Submit triggers
	// GC of completed ops beyond `retain`.
	for range 5 {
		op, err := s.Submit(ctx, &Operation{
			Type: TypeApply, APIVersion: "p.openctl.io/v1", Kind: "VM", ResourceName: "x",
		})
		if err != nil {
			t.Fatal(err)
		}
		// Mark running + complete so the next Submit doesn't conflict.
		if _, err := s.ClaimNextPending(ctx); err != nil {
			t.Fatal(err)
		}
		if err := s.Complete(ctx, op.ID, StatusSucceeded, "", ""); err != nil {
			t.Fatal(err)
		}
	}

	ops, _ := s.List(ctx, ListFilter{ResourceName: "x"})
	if len(ops) > retain {
		t.Errorf("after %d ops with retain=%d, got %d in store", 5, retain, len(ops))
	}
}

func TestListFiltersAndOrders(t *testing.T) {
	s := openStore(t, 50)
	ctx := context.Background()

	for _, name := range []string{"alpha", "beta", "gamma"} {
		if _, err := s.Submit(ctx, &Operation{
			Type: TypeApply, APIVersion: "p.openctl.io/v1", Kind: "VM", ResourceName: name,
		}); err != nil {
			t.Fatal(err)
		}
	}

	all, _ := s.List(ctx, ListFilter{})
	if len(all) != 3 {
		t.Errorf("List() returned %d, want 3", len(all))
	}

	// Newest first
	if all[0].ResourceName != "gamma" {
		t.Errorf("first op resource = %q, want gamma", all[0].ResourceName)
	}

	filtered, _ := s.List(ctx, ListFilter{ResourceName: "beta"})
	if len(filtered) != 1 || filtered[0].ResourceName != "beta" {
		t.Errorf("filter by name=beta: %v", filtered)
	}
}
