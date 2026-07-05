package providerstate

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl/internal/controller/storage"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := storage.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	state := []byte(`{"id":"abc"}`)
	private := []byte(`{"secret":1}`)
	if err := s.SaveState(ctx, "acme.openctl.io/v1", "Widget", "w1", state, private, 3); err != nil {
		t.Fatalf("save: %v", err)
	}

	gotState, gotPriv, gotVer, err := s.LoadState(ctx, "acme.openctl.io/v1", "Widget", "w1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if string(gotState) != string(state) {
		t.Errorf("state = %s, want %s", gotState, state)
	}
	if string(gotPriv) != string(private) {
		t.Errorf("private = %s, want %s", gotPriv, private)
	}
	if gotVer != 3 {
		t.Errorf("schemaVersion = %d, want 3", gotVer)
	}
}

func TestLoadMissingReturnsNil(t *testing.T) {
	s := newTestStore(t)
	state, private, ver, err := s.LoadState(context.Background(), "acme.openctl.io/v1", "Widget", "ghost")
	if err != nil {
		t.Fatalf("load missing: %v", err)
	}
	if state != nil || private != nil || ver != 0 {
		t.Errorf("missing row should be (nil,nil,0), got (%v,%v,%d)", state, private, ver)
	}
}

func TestSaveUpserts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SaveState(ctx, "a.openctl.io/v1", "K", "n", []byte("v1"), nil, 1); err != nil {
		t.Fatalf("save 1: %v", err)
	}
	if err := s.SaveState(ctx, "a.openctl.io/v1", "K", "n", []byte("v2"), nil, 2); err != nil {
		t.Fatalf("save 2: %v", err)
	}
	got, _, ver, _ := s.LoadState(ctx, "a.openctl.io/v1", "K", "n")
	if string(got) != "v2" || ver != 2 {
		t.Errorf("after upsert = (%s,%d), want (v2,2)", got, ver)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SaveState(ctx, "a.openctl.io/v1", "K", "n", []byte("v"), nil, 0); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.DeleteState(ctx, "a.openctl.io/v1", "K", "n"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Deleting again (now missing) must not error.
	if err := s.DeleteState(ctx, "a.openctl.io/v1", "K", "n"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	got, _, _, _ := s.LoadState(ctx, "a.openctl.io/v1", "K", "n")
	if got != nil {
		t.Errorf("state after delete = %s, want nil", got)
	}
}

func TestNilBlobsStoredAsNull(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	// A stateful provider may return nil private (no plan->apply handoff).
	if err := s.SaveState(ctx, "a.openctl.io/v1", "K", "n", []byte("state"), nil, 0); err != nil {
		t.Fatalf("save: %v", err)
	}
	state, private, _, err := s.LoadState(ctx, "a.openctl.io/v1", "K", "n")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if string(state) != "state" {
		t.Errorf("state = %s", state)
	}
	if private != nil {
		t.Errorf("private = %v, want nil", private)
	}
}

func TestSaveRejectsEmptyKey(t *testing.T) {
	s := newTestStore(t)
	if err := s.SaveState(context.Background(), "", "K", "n", []byte("v"), nil, 0); err == nil {
		t.Error("expected error saving with empty apiVersion")
	}
}
