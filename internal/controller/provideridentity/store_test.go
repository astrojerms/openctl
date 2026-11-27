package provideridentity

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/internal/controller/storage"
)

func TestBindingSurvivesRestartAndCannotBeReplaced(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s := New(db)
	id := &Identity{Context: "site", Endpoint: "https://pve", Node: "pve1", VMID: 101, Identity: "proxmox:pve1:101:uuid"}
	if err := s.SaveIdentity(ctx, "vm", id); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveIdentity(ctx, "alias", id); err == nil {
		t.Fatal("same instance bound under another name")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s = New(db)
	got, err := s.LoadIdentity(ctx, "vm")
	if err != nil || got == nil || *got != *id {
		t.Fatalf("persisted identity=%+v, err=%v", got, err)
	}
	replacement := *id
	replacement.Identity = "different-uuid"
	if err := s.SaveIdentity(ctx, "vm", &replacement); err == nil {
		t.Fatal("immutable identity overwritten")
	}
	if err := s.SaveIdentity(ctx, "vm", id); err != nil {
		t.Fatalf("idempotent bind: %v", err)
	}
	if err := s.DeleteIdentity(ctx, "vm"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveIdentity(ctx, "vm", &replacement); err != nil {
		t.Fatalf("explicit release did not clear binding: %v", err)
	}
}

func TestConfirmationRequiresAtomicallyIdleController(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ops := operations.New(db, 50)
	op, err := ops.Submit(ctx, &operations.Operation{Type: operations.TypeApply, APIVersion: "fake.openctl.io/v1", Kind: "FakeKind", ResourceName: "busy", ManifestJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	store := New(db)
	id := &Identity{Context: "site", Endpoint: "https://pve", Node: "pve1", VMID: 101, Identity: "immutable"}
	if err := store.ConfirmIdentity(ctx, "vm", id); err == nil {
		t.Fatal("confirmation allowed while operation pending")
	}
	if bound, err := store.LoadIdentity(ctx, "vm"); err != nil || bound != nil {
		t.Fatalf("failed confirmation changed binding: %v %v", bound, err)
	}
	if err := ops.Complete(ctx, op.ID, operations.StatusSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmIdentity(ctx, "vm", id); err != nil {
		t.Fatalf("idle confirmation: %v", err)
	}
}
