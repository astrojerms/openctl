package operations

import (
	"context"
	"errors"
	"testing"
)

func TestBeginChildInsertsRunningRowUnderParent(t *testing.T) {
	s := openStore(t, 50)
	ctx := context.Background()

	parent, err := s.Submit(ctx, &Operation{
		Type: TypeApply, APIVersion: "k3s.openctl.io/v1", Kind: "Cluster", ResourceName: "dev",
	})
	if err != nil {
		t.Fatal(err)
	}

	child, err := s.BeginChild(ctx, parent.ID, &Operation{
		Type:         TypeApply,
		APIVersion:   "proxmox.openctl.io/v1",
		Kind:         "VirtualMachine",
		ResourceName: "dev-cp-0",
		ManifestJSON: `{"foo":"bar"}`,
	})
	if err != nil {
		t.Fatalf("BeginChild: %v", err)
	}
	if child.ID == "" || child.ID[:3] != "op-" {
		t.Errorf("child ID = %q, want op-... prefix", child.ID)
	}
	if child.ParentID != parent.ID {
		t.Errorf("ParentID = %q, want %q", child.ParentID, parent.ID)
	}
	if child.Status != StatusRunning {
		t.Errorf("Status = %q, want running", child.Status)
	}
	if child.StartedAt == "" || child.SubmittedAt == "" {
		t.Error("timestamps not populated")
	}

	got, err := s.Get(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ManifestJSON != `{"foo":"bar"}` {
		t.Errorf("ManifestJSON = %q", got.ManifestJSON)
	}
}

func TestBeginChildBypassesConflictCheck(t *testing.T) {
	s := openStore(t, 50)
	ctx := context.Background()

	// Submit a top-level apply for a VM.
	first, err := s.Submit(ctx, &Operation{
		Type: TypeApply, APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine", ResourceName: "vm-x",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Submitting a second top-level apply for the same VM should conflict.
	_, err = s.Submit(ctx, &Operation{
		Type: TypeApply, APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine", ResourceName: "vm-x",
	})
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("want ConflictError, got %v", err)
	}

	// But BeginChild for the same VM under a different parent must succeed —
	// child rows bypass the fail-fast check by design.
	parent, _ := s.Submit(ctx, &Operation{
		Type: TypeApply, APIVersion: "k3s.openctl.io/v1", Kind: "Cluster", ResourceName: "dev",
	})
	if _, err := s.BeginChild(ctx, parent.ID, &Operation{
		Type: TypeApply, APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine", ResourceName: "vm-x",
	}); err != nil {
		t.Fatalf("BeginChild should bypass conflict check, got %v", err)
	}
	_ = first
}

func TestEndChildWritesTerminal(t *testing.T) {
	s := openStore(t, 50)
	ctx := context.Background()
	parent, _ := s.Submit(ctx, &Operation{
		Type: TypeApply, APIVersion: "k3s.openctl.io/v1", Kind: "Cluster", ResourceName: "dev",
	})
	child, _ := s.BeginChild(ctx, parent.ID, &Operation{
		Type: TypeStep, APIVersion: "k3s.openctl.io/v1", Kind: "Cluster", ResourceName: "dev/install-k3s",
		Label: "Install k3s",
	})

	if err := s.EndChild(ctx, child.ID, StatusSucceeded, "", `{"result":"ok"}`); err != nil {
		t.Fatalf("EndChild: %v", err)
	}
	got, _ := s.Get(ctx, child.ID)
	if got.Status != StatusSucceeded {
		t.Errorf("Status = %q, want succeeded", got.Status)
	}
	if got.CompletedAt == "" {
		t.Error("CompletedAt empty")
	}
	if got.Label != "Install k3s" {
		t.Errorf("Label = %q", got.Label)
	}
	if got.ResultJSON != `{"result":"ok"}` {
		t.Errorf("ResultJSON = %q", got.ResultJSON)
	}

	if err := s.EndChild(ctx, child.ID, StatusPending, "", ""); err == nil {
		t.Error("EndChild with non-terminal status should error")
	}
}

func TestListChildrenReturnsOldestFirst(t *testing.T) {
	s := openStore(t, 50)
	ctx := context.Background()
	parent, _ := s.Submit(ctx, &Operation{
		Type: TypeApply, APIVersion: "k3s.openctl.io/v1", Kind: "Cluster", ResourceName: "dev",
	})
	for _, n := range []string{"vm-a", "vm-b", "vm-c"} {
		if _, err := s.BeginChild(ctx, parent.ID, &Operation{
			Type: TypeApply, APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine", ResourceName: n,
		}); err != nil {
			t.Fatal(err)
		}
	}
	children, err := s.ListChildren(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListChildren: %v", err)
	}
	if len(children) != 3 {
		t.Fatalf("got %d children, want 3", len(children))
	}
	// Submitted-asc means insertion order.
	got := []string{children[0].ResourceName, children[1].ResourceName, children[2].ResourceName}
	want := []string{"vm-a", "vm-b", "vm-c"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("children[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// Empty parent → empty slice.
	got2, _ := s.ListChildren(ctx, "")
	if len(got2) != 0 {
		t.Errorf("ListChildren(\"\") = %d, want 0", len(got2))
	}
}

func TestRecorderFromReturnsNoopWhenAbsent(t *testing.T) {
	ctx := context.Background()
	r := RecorderFrom(ctx)
	if r == nil {
		t.Fatal("RecorderFrom returned nil; should return noopRecorder")
	}
	if _, err := r.Begin(ctx, &Operation{Type: TypeStep}); err != nil {
		t.Errorf("noop Begin should not error, got %v", err)
	}
	if err := r.End(ctx, "ignored", true, "", ""); err != nil {
		t.Errorf("noop End should not error, got %v", err)
	}
}

func TestRecorderWritesChildrenViaStore(t *testing.T) {
	s := openStore(t, 50)
	ctx := context.Background()
	parent, _ := s.Submit(ctx, &Operation{
		Type: TypeApply, APIVersion: "k3s.openctl.io/v1", Kind: "Cluster", ResourceName: "dev",
	})

	ctx2 := WithRecorder(ctx, StoreRecorder{Store: s}, parent.ID)
	rec := RecorderFrom(ctx2)

	cid, err := rec.Begin(ctx2, &Operation{
		Type: TypeStep, APIVersion: "k3s.openctl.io/v1", Kind: "Cluster", ResourceName: "dev/install-k3s",
		Label: "Install k3s",
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := rec.End(ctx2, cid, true, "", ""); err != nil {
		t.Fatalf("End: %v", err)
	}

	children, _ := s.ListChildren(ctx, parent.ID)
	if len(children) != 1 {
		t.Fatalf("got %d children, want 1", len(children))
	}
	if children[0].ParentID != parent.ID {
		t.Errorf("child ParentID = %q, want %q", children[0].ParentID, parent.ID)
	}
	if children[0].Label != "Install k3s" {
		t.Errorf("Label = %q", children[0].Label)
	}
}
