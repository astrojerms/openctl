package overview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openctl/openctl/internal/controller/storage"
)

func openOverviewDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type overviewTestOperation struct {
	id string
	parent any
	typ string
	apiVersion string
	kind string
	name string
	status string
	submitted string
}

func insertOverviewOperation(t *testing.T, db *sql.DB, op overviewTestOperation) {
	t.Helper()
	if op.typ == "" {
		op.typ = "apply"
	}
	if op.apiVersion == "" {
		op.apiVersion = "test.openctl.io/v1"
	}
	if op.kind == "" {
		op.kind = "Widget"
	}
	if op.name == "" {
		op.name = op.id
	}
	if op.submitted == "" {
		op.submitted = "2025-01-01T00:00:00Z"
	}
	_, err := db.Exec(`INSERT INTO operations
		(id, parent_id, type, api_version, kind, resource_name, status, submitted_at,
		 manifest_json, result_json, error, label, source)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'ui')`,
		op.id, op.parent, op.typ, op.apiVersion, op.kind, op.name, op.status, op.submitted,
		`not JSON: SECRET-manifest`, `not JSON: SECRET-result`, `SECRET-error`, `SECRET-label`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotLatestRootAdmissionSupersedesByInsertionOrder(t *testing.T) {
	for _, next := range []string{"succeeded", "pending", "running", "failed", "interrupted", "canceled"} {
		t.Run(next, func(t *testing.T) {
			db := openOverviewDB(t)
			insertOverviewOperation(t, db, overviewTestOperation{id: "old", name: "same", status: "failed", submitted: "2099-01-01T00:00:00Z"})
			insertOverviewOperation(t, db, overviewTestOperation{id: "new", name: "same", typ: "delete", parent: "", status: next, submitted: "2000-01-01T00:00:00Z"})
			// Even a later child Apply and a root non-resource step must not
			// replace the latest root Apply/Delete for the same resource.
			insertOverviewOperation(t, db, overviewTestOperation{id: "child", name: "same", parent: "new", status: "failed"})
			insertOverviewOperation(t, db, overviewTestOperation{id: "step", name: "same", typ: "step", status: "failed"})
			got, err := New(db).Snapshot(context.Background(), 1)
			if err != nil {
				t.Fatal(err)
			}
			wantReview := next == "failed" || next == "interrupted" || next == "canceled"
			if wantReview {
				if got.ReviewResources != 1 || len(got.ReviewOperations) != 1 || got.ReviewOperations[0].ID != "new" {
					t.Fatalf("latest unsuccessful admission missing: %+v", got)
				}
			} else if got.ReviewResources != 0 || len(got.ReviewOperations) != 0 {
				t.Fatalf("superseded failure still needs review: %+v", got)
			}
			var pending, running uint32
			if next == "pending" {
				pending = 1
			}
			if next == "running" {
				running = 1
			}
			if got.PendingOperations != pending || got.RunningOperations != running {
				t.Fatalf("active counts = %d/%d, want %d/%d", got.PendingOperations, got.RunningOperations, pending, running)
			}
		})
	}
}

func TestSnapshotReviewIncludesFailedFirstApplyAndFullResourceIdentity(t *testing.T) {
	db := openOverviewDB(t)
	insertOverviewOperation(t, db, overviewTestOperation{id: "first-apply", name: "same", status: "failed"})
	insertOverviewOperation(t, db, overviewTestOperation{id: "different-api", name: "same", apiVersion: "other.openctl.io/v1", status: "succeeded"})
	insertOverviewOperation(t, db, overviewTestOperation{id: "different-kind", name: "same", kind: "Other", status: "succeeded"})
	insertOverviewOperation(t, db, overviewTestOperation{id: "different-name", name: "other", status: "succeeded"})
	got, err := New(db).Snapshot(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if got.ManagedResources != 0 || got.ReviewResources != 1 || len(got.ReviewOperations) != 1 || got.ReviewOperations[0].ID != "first-apply" {
		t.Fatalf("failed first Apply must survive without any saved manifest: %+v", got)
	}
}

func TestSnapshotCountsAreIndependentOfCapsAndChildren(t *testing.T) {
	db := openOverviewDB(t)
	for i := range 15 {
		for _, status := range []string{"pending", "running", "failed"} {
			insertOverviewOperation(t, db, overviewTestOperation{id: fmt.Sprintf("%s-%02d", status, i), status: status})
			insertOverviewOperation(t, db, overviewTestOperation{id: fmt.Sprintf("child-%s-%02d", status, i), parent: "pending-00", status: status})
			insertOverviewOperation(t, db, overviewTestOperation{id: fmt.Sprintf("step-%s-%02d", status, i), typ: "step", status: status})
		}
	}
	for _, tc := range []struct{ limit uint32; active, review int }{
		{1, 1, 1}, {0, 10, 10}, {100, 30, 15},
	} {
		got, err := New(db).Snapshot(context.Background(), tc.limit)
		if err != nil {
			t.Fatal(err)
		}
		if got.PendingOperations != 15 || got.RunningOperations != 15 || got.ReviewResources != 15 {
			t.Fatalf("cap %d changed exact root totals: %+v", tc.limit, got)
		}
		if len(got.ActiveOperations) != tc.active || len(got.ReviewOperations) != tc.review {
			t.Fatalf("cap %d list lengths = %d/%d, want %d/%d", tc.limit, len(got.ActiveOperations), len(got.ReviewOperations), tc.active, tc.review)
		}
		if got.ActiveOperations[0].ID != "running-14" || got.ReviewOperations[0].ID != "failed-14" {
			t.Fatalf("unstable admission order: active=%+v review=%+v", got.ActiveOperations, got.ReviewOperations)
		}
		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "SECRET") {
			t.Fatalf("sensitive operation body leaked: %s", encoded)
		}
	}
}

func TestSnapshotInventoryIsPersistedDesiredMetadataOnly(t *testing.T) {
	db := openOverviewDB(t)
	for _, row := range []struct{ apiVersion, kind, name, metadata string }{
		{"z.unregistered.io/v9", "GoneProvider", "disconnected", "not JSON: SECRET-metadata"},
		{"a.openctl.io/v1", "VM", "owned-child", `{"ownerReferences":[{"kind":"Cluster","name":"parent"}]}`},
		{"a.openctl.io/v1", "VM", "parentless", "not JSON"},
		{"a.openctl.io/v1", "Cluster", "parent", "not JSON"},
	} {
		if _, err := db.Exec(`INSERT INTO applied_manifests (api_version, kind, name, spec_json, metadata_json, applied_at) VALUES (?, ?, ?, ?, ?, ?)`,
			row.apiVersion, row.kind, row.name, "not JSON: SECRET-spec", row.metadata, "2025-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	// An operation by itself (like a discovery-only observation) must not
	// invent a desired-state inventory record.
	insertOverviewOperation(t, db, overviewTestOperation{id: "not-saved", status: "succeeded"})
	before := time.Now().UTC().Add(-time.Second)
	got, err := New(db).Snapshot(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []KindCount{
		{APIVersion: "a.openctl.io/v1", Kind: "Cluster", Resources: 1},
		{APIVersion: "a.openctl.io/v1", Kind: "VM", Resources: 2},
		{APIVersion: "z.unregistered.io/v9", Kind: "GoneProvider", Resources: 1},
	}
	if got.ManagedResources != 4 || !reflect.DeepEqual(got.Kinds, want) {
		t.Fatalf("inventory = %d %+v, want all persisted desired rows", got.ManagedResources, got.Kinds)
	}
	asOf, err := time.Parse(time.RFC3339Nano, got.AsOf)
	if err != nil || asOf.Before(before) || asOf.After(time.Now().UTC()) {
		t.Fatalf("as_of does not identify this snapshot read: %q (%v)", got.AsOf, err)
	}
}

func TestSnapshotReadsWhileWriterHasUncommittedChanges(t *testing.T) {
	db := openOverviewDB(t)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO applied_manifests (api_version, kind, name, spec_json, applied_at) VALUES ('test/v1', 'Widget', 'uncommitted', 'invalid', 'now')`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := New(db).Snapshot(ctx, 10)
	if err != nil {
		t.Fatalf("read-only snapshot must not acquire the writer lock: %v", err)
	}
	if got.ManagedResources != 0 || len(got.Kinds) != 0 {
		t.Fatalf("uncommitted write visible in snapshot: %+v", got)
	}
}

func TestSnapshotErrorsAreNotEmptySnapshots(t *testing.T) {
	db := openOverviewDB(t)
	s := New(db)
	if got, err := s.Snapshot(context.Background(), MaxOperationLimit+1); err == nil || got != nil {
		t.Fatalf("oversized cap returned snapshot: %+v, %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := s.Snapshot(ctx, 10); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("cancellation not propagated: %+v, %v", got, err)
	}
	if _, err := db.Exec(`DROP TABLE operations`); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Snapshot(context.Background(), 10); err == nil || got != nil {
		t.Fatalf("missing persisted data returned fabricated zeros: %+v, %v", got, err)
	}
}
