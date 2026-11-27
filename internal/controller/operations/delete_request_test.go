package operations

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/refs"
	"github.com/openctl/openctl/internal/controller/storage"
)

func deleteRequestBinding(t *testing.T, id, actor string, target refs.Ref) RequestBinding {
	t.Helper()
	binding, err := NewDeleteRequest(id, actor, target.APIVersion, target.Kind, target.Name)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func TestDeleteRequestValidatesKeyActorAndIdentity(t *testing.T) {
	for _, input := range [][5]string{
		{"", "alice", "fake.openctl.io/v1", "FakeKind", "target"},
		{"key", "", "fake.openctl.io/v1", "FakeKind", "target"},
		{"key", "alice", "", "FakeKind", "target"},
		{"key", "alice", "fake.openctl.io/v1", "", "target"},
		{"key", "alice", "fake.openctl.io/v1", "FakeKind", ""},
		{strings.Repeat("a", 129), "alice", "fake.openctl.io/v1", "FakeKind", "target"},
		{"line\nbreak", "alice", "fake.openctl.io/v1", "FakeKind", "target"},
		{"é", "alice", "fake.openctl.io/v1", "FakeKind", "target"},
	} {
		if _, err := NewDeleteRequest(input[0], input[1], input[2], input[3], input[4]); err == nil {
			t.Fatalf("invalid binding accepted: %q", input)
		}
	}
	target := refs.Ref{APIVersion: "fake.openctl.io/v1", Kind: "FakeKind", Name: "target"}
	first := deleteRequestBinding(t, " key with spaces ", "alice", target)
	other := deleteRequestBinding(t, "another-key", "bob", target)
	if first.ID != " key with spaces " || first.IntentHash != other.IntentHash || len(first.IntentHash) != 64 {
		t.Fatal("Delete fingerprint changed the raw key or included actor/key in intent")
	}
}

func TestDeleteRequestConcurrentAdmissionsAcrossStores(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, 0)
	other := New(store.db, 0)
	target := refs.Ref{APIVersion: "fake.openctl.io/v1", Kind: "FakeKind", Name: "target"}
	binding := deleteRequestBinding(t, "concurrent-delete", "alice", target)
	const count = 24
	type result struct {
		op       *Operation
		admitted bool
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, count)
	for i := range count {
		selected := store
		if i%2 != 0 {
			selected = other
		}
		go func() {
			<-start
			op, admitted, err := selected.SubmitRequest(ctx, targetDelete(target), binding)
			results <- result{op, admitted, err}
		}()
	}
	close(start)
	var operationID string
	admissions := 0
	for range count {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent Delete admission: %v", result.err)
		}
		if operationID == "" {
			operationID = result.op.ID
		}
		if result.op.ID != operationID || result.op.RequestID != binding.ID || result.op.ParentID != "" || result.op.Type != TypeDelete {
			t.Fatalf("wrong Delete acknowledgement: %+v", result.op)
		}
		if result.admitted {
			admissions++
		}
	}
	all, err := store.List(ctx, ListFilter{})
	if err != nil || len(all) != 1 || admissions != 1 {
		t.Fatalf("duplicate Delete admission: rows=%d admissions=%d err=%v", len(all), admissions, err)
	}
}

func TestDeleteRequestConflictsAcrossActorTargetAndAction(t *testing.T) {
	for _, firstAction := range []string{TypeApply, TypeDelete} {
		t.Run(firstAction, func(t *testing.T) {
			ctx := context.Background()
			store := openStore(t, 0)
			desired := requestResource()
			target := refs.Ref{APIVersion: desired.APIVersion, Kind: desired.Kind, Name: desired.Metadata.Name}
			binding := deleteRequestBinding(t, "shared-namespace", "alice", target)
			candidate := targetDelete(target)
			if firstAction == TypeApply {
				binding = requestBinding(t, binding.ID, "alice", desired)
				candidate = requestOperation(t, desired)
			}
			original, _, err := store.SubmitRequest(ctx, candidate, binding)
			if err != nil {
				t.Fatal(err)
			}
			cases := map[string]RequestBinding{
				"actor":      deleteRequestBinding(t, binding.ID, "bob", target),
				"apiVersion": deleteRequestBinding(t, binding.ID, "alice", refs.Ref{APIVersion: "other.openctl.io/v1", Kind: target.Kind, Name: target.Name}),
				"kind":       deleteRequestBinding(t, binding.ID, "alice", refs.Ref{APIVersion: target.APIVersion, Kind: "OtherKind", Name: target.Name}),
				"name":       deleteRequestBinding(t, binding.ID, "alice", refs.Ref{APIVersion: target.APIVersion, Kind: target.Kind, Name: "replacement"}),
			}
			if firstAction == TypeApply {
				cases["action"] = deleteRequestBinding(t, binding.ID, "alice", target)
			} else {
				cases["action"] = requestBinding(t, binding.ID, "alice", desired)
			}
			for name, changed := range cases {
				t.Run(name, func(t *testing.T) {
					candidate := targetDelete(target)
					if name == "action" && firstAction == TypeDelete {
						candidate = requestOperation(t, desired)
					}
					op, admitted, err := store.SubmitRequest(ctx, candidate, changed)
					var conflict *RequestConflictError
					if !errors.As(err, &conflict) || conflict.OperationID != original.ID || op != nil || admitted {
						t.Fatalf("bound key was reused: op=%+v admitted=%v err=%v", op, admitted, err)
					}
				})
			}
		})
	}
}

func TestDeleteRequestReplaySurvivesTerminalStatesAndRestart(t *testing.T) {
	for _, terminal := range []string{StatusSucceeded, StatusFailed, StatusCancelled, StatusInterrupted} {
		t.Run(terminal, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "delete.db")
			db, err := storage.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			store := New(db, 0)
			desired := requestResource()
			target := refs.Ref{APIVersion: desired.APIVersion, Kind: desired.Kind, Name: desired.Metadata.Name}
			binding := deleteRequestBinding(t, "persisted-delete", "alice", target)
			original, _, err := store.SubmitRequest(ctx, targetDelete(target), binding)
			if err != nil {
				t.Fatal(err)
			}
			if terminal == StatusInterrupted {
				if _, err := store.ClaimNextPending(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err := store.MarkRunningInterrupted(ctx); err != nil {
					t.Fatal(err)
				}
			} else if err := store.Complete(ctx, original.ID, terminal, "original result", "original failure"); err != nil {
				t.Fatal(err)
			}
			// A replacement and its new operation must not become targets of replay.
			if err := manifests.New(db).Save(ctx, desired); err != nil {
				t.Fatal(err)
			}
			later, err := store.Submit(ctx, requestOperation(t, desired))
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := storage.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reopened.Close() }()
			store = New(reopened, 0)
			replayed, admitted, err := store.SubmitRequest(ctx, targetDelete(target), binding)
			if err != nil || admitted || replayed.ID != original.ID || replayed.Status != terminal || replayed.RequestID != binding.ID {
				t.Fatalf("terminal Delete was resubmitted: %+v admitted=%v err=%v", replayed, admitted, err)
			}
			found, err := store.GetByRequest(ctx, binding.ID)
			if err != nil || found.ID != original.ID || found.Status != terminal {
				t.Fatalf("lost acknowledgement lookup changed: %+v err=%v", found, err)
			}
			current, err := store.Get(ctx, later.ID)
			if err != nil || current.Status != StatusPending {
				t.Fatalf("replay changed the replacement's work: %+v err=%v", current, err)
			}
			all, err := store.List(ctx, ListFilter{})
			if err != nil || len(all) != 2 {
				t.Fatalf("replay created another Delete: %+v err=%v", all, err)
			}
		})
	}
}

func TestPrunedDeleteRequestRemainsReserved(t *testing.T) {
	ctx := context.Background()
	store := openStore(t, 1)
	desired := requestResource()
	target := refs.Ref{APIVersion: desired.APIVersion, Kind: desired.Kind, Name: desired.Metadata.Name}
	binding := deleteRequestBinding(t, "pruned-delete", "alice", target)
	original, _, err := store.SubmitRequest(ctx, targetDelete(target), binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, original.ID, StatusSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}
	later, err := store.Submit(ctx, requestOperation(t, desired))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, later.ID, StatusSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}
	for name, lookup := range map[string]func() error{
		"lookup": func() error { _, err := store.GetByRequest(ctx, binding.ID); return err },
		"replay": func() error { _, err := store.ReplayRequest(ctx, binding); return err },
		"submit": func() error { _, _, err := store.SubmitRequest(ctx, targetDelete(target), binding); return err },
	} {
		t.Run(name, func(t *testing.T) {
			var pruned *RequestPrunedError
			if err := lookup(); !errors.As(err, &pruned) || pruned.OperationID != original.ID {
				t.Fatalf("pruned Delete key became reusable: %v", err)
			}
		})
	}
	_, _, err = store.SubmitRequest(ctx, requestOperation(t, desired), requestBinding(t, binding.ID, "alice", desired))
	var conflict *RequestConflictError
	if !errors.As(err, &conflict) || conflict.OperationID != original.ID {
		t.Fatalf("pruned Delete key lost its action fence: %v", err)
	}
}

func TestDeleteRequestRetainsTransactionalReferenceFences(t *testing.T) {
	for _, firstType := range []string{TypeApply, TypeDelete} {
		t.Run(firstType, func(t *testing.T) {
			ctx := context.Background()
			store := openStore(t, 0)
			target := refs.Ref{APIVersion: "fake.openctl.io/v1", Kind: "FakeKind", Name: "target"}
			binding := deleteRequestBinding(t, "fenced-delete", "alice", target)
			if firstType == TypeApply {
				if _, err := store.Submit(ctx, dependentApply(t, target)); err != nil {
					t.Fatal(err)
				}
				_, _, err := store.SubmitRequest(ctx, targetDelete(target), binding)
				var conflict *ConflictError
				if !errors.As(err, &conflict) || conflict.Reason == "" {
					t.Fatalf("keyed Delete bypassed active dependent: %v", err)
				}
				if _, err := store.GetByRequest(ctx, binding.ID); !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("rejected Delete reserved its key: %v", err)
				}
			} else {
				if _, _, err := store.SubmitRequest(ctx, targetDelete(target), binding); err != nil {
					t.Fatal(err)
				}
				_, err := store.Submit(ctx, dependentApply(t, target))
				var conflict *ConflictError
				if !errors.As(err, &conflict) || conflict.Reason == "" {
					t.Fatalf("dependent bypassed keyed Delete: %v", err)
				}
			}
		})
	}
}

func TestRequestBindingsRejectChildrenAndSteps(t *testing.T) {
	store := openStore(t, 0)
	target := refs.Ref{APIVersion: "fake.openctl.io/v1", Kind: "FakeKind", Name: "target"}
	binding := deleteRequestBinding(t, "root-only", "alice", target)
	for _, candidate := range []*Operation{
		{Type: TypeStep, APIVersion: target.APIVersion, Kind: target.Kind, ResourceName: target.Name},
		{Type: TypeDelete, ParentID: "parent", APIVersion: target.APIVersion, Kind: target.Kind, ResourceName: target.Name},
		{Type: TypeApply, ParentID: "parent", APIVersion: target.APIVersion, Kind: target.Kind, ResourceName: target.Name},
	} {
		if op, admitted, err := store.SubmitRequest(context.Background(), candidate, binding); err == nil || op != nil || admitted {
			t.Fatalf("invalid request root was accepted: %+v admitted=%v err=%v", op, admitted, err)
		}
	}
	if _, err := store.GetByRequest(context.Background(), binding.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("invalid operation reserved a key: %v", err)
	}
}
