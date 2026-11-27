package server

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/overview"
	"github.com/openctl/openctl/internal/controller/storage"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

func openOverviewHandler(t *testing.T) (*overviewHandler, *sql.DB) {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return newOverviewHandler(overview.New(db)), db
}

func overviewViewerContext() context.Context {
	return auth.ContextWithPrincipal(context.Background(), auth.Principal{UserID: "reader", Role: auth.RoleViewer})
}

func seedOverviewHandlerOperation(t *testing.T, db *sql.DB, id, opStatus string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO operations
		(id, type, api_version, kind, resource_name, status, submitted_at, started_at, completed_at, source,
		 manifest_json, result_json, error, label)
		VALUES (?, 'apply', 'gone.provider.io/v1', 'Disconnected', ?, ?,
		 '2025-01-01T00:00:00Z', '2025-01-01T00:01:00Z', '2025-01-01T00:02:00Z', 'ui', ?, ?, ?, ?)`,
		id, id, opStatus, "invalid JSON SECRET-manifest", "invalid JSON SECRET-result", "SECRET-error", "SECRET-label")
	if err != nil {
		t.Fatal(err)
	}
}

func TestOverviewRequiresViewerRole(t *testing.T) {
	h, _ := openOverviewHandler(t)
	for _, role := range []auth.Role{auth.RoleViewer, auth.RoleEditor, auth.RoleAdmin} {
		ctx := auth.ContextWithPrincipal(context.Background(), auth.Principal{UserID: "user", Role: role})
		got, err := h.GetOverview(ctx, &apiv1.GetOverviewRequest{})
		if err != nil || got == nil || got.AsOf == "" {
			t.Fatalf("role %q cannot read persisted overview: %+v, %v", role, got, err)
		}
	}
	for _, role := range []auth.Role{"", "unrecognized"} {
		ctx := auth.ContextWithPrincipal(context.Background(), auth.Principal{UserID: "user", Role: role})
		got, err := h.GetOverview(ctx, &apiv1.GetOverviewRequest{})
		if status.Code(err) != codes.PermissionDenied || got != nil {
			t.Fatalf("role %q received overview: %+v, %v", role, got, err)
		}
	}
}

func TestOverviewUnavailableStoreDoesNotInventZeros(t *testing.T) {
	got, err := newOverviewHandler(nil).GetOverview(overviewViewerContext(), &apiv1.GetOverviewRequest{})
	if status.Code(err) != codes.FailedPrecondition || got != nil {
		t.Fatalf("unconfigured overview = %+v, %v", got, err)
	}
	// A genuinely empty persisted snapshot is different from an unavailable
	// store and still has an actual read timestamp.
	h, _ := openOverviewHandler(t)
	got, err = h.GetOverview(overviewViewerContext(), nil)
	if err != nil || got == nil || got.AsOf == "" || got.ManagedResources != 0 || got.ReviewResources != 0 {
		t.Fatalf("empty persisted snapshot = %+v, %v", got, err)
	}
}

func TestOverviewOperationCapAndExactTotals(t *testing.T) {
	h, db := openOverviewHandler(t)
	for i := range 105 {
		seedOverviewHandlerOperation(t, db, fmt.Sprintf("active-%03d", i), "pending")
		seedOverviewHandlerOperation(t, db, fmt.Sprintf("review-%03d", i), "canceled")
	}
	for _, tc := range []struct{ limit uint32; want int }{
		{0, 10}, {1, 1}, {100, 100},
	} {
		got, err := h.GetOverview(overviewViewerContext(), &apiv1.GetOverviewRequest{OperationLimit: tc.limit})
		if err != nil {
			t.Fatal(err)
		}
		if got.PendingOperations != 105 || got.RunningOperations != 0 || got.ReviewResources != 105 {
			t.Fatalf("cap %d changed totals: %+v", tc.limit, got)
		}
		if len(got.ActiveOperations) != tc.want || len(got.ReviewOperations) != tc.want {
			t.Fatalf("cap %d returned %d/%d rows, want %d each", tc.limit, len(got.ActiveOperations), len(got.ReviewOperations), tc.want)
		}
		if got.ActiveOperations[0].Id != "active-104" || got.ReviewOperations[0].Id != "review-104" {
			t.Fatalf("unstable operation ordering: %+v", got)
		}
	}
	for _, limit := range []uint32{101, ^uint32(0)} {
		got, err := h.GetOverview(overviewViewerContext(), &apiv1.GetOverviewRequest{OperationLimit: limit})
		if status.Code(err) != codes.InvalidArgument || got != nil {
			t.Fatalf("invalid cap %d returned %+v, %v", limit, got, err)
		}
	}
}

func TestOverviewReturnsOnlySafeOperationMetadata(t *testing.T) {
	h, db := openOverviewHandler(t)
	seedOverviewHandlerOperation(t, db, "running", "running")
	seedOverviewHandlerOperation(t, db, "failed-first-apply", "failed")
	if _, err := db.Exec(`INSERT INTO applied_manifests (api_version, kind, name, spec_json, metadata_json, applied_at)
		VALUES ('gone.provider.io/v1', 'Disconnected', 'owned-child', 'invalid JSON SECRET-spec', 'invalid JSON SECRET-metadata', '2020-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	got, err := h.GetOverview(overviewViewerContext(), &apiv1.GetOverviewRequest{})
	if err != nil {
		t.Fatalf("metadata overview tried to decode a sensitive body: %v", err)
	}
	if got.ManagedResources != 1 || len(got.Kinds) != 1 || got.Kinds[0].ApiVersion != "gone.provider.io/v1" || got.Kinds[0].Kind != "Disconnected" || got.Kinds[0].Resources != 1 {
		t.Fatalf("unregistered persisted kind missing: %+v", got)
	}
	if got.RunningOperations != 1 || got.ReviewResources != 1 || len(got.ActiveOperations) != 1 || len(got.ReviewOperations) != 1 {
		t.Fatalf("safe operation lists missing: %+v", got)
	}
	for _, op := range []*apiv1.Operation{got.ActiveOperations[0], got.ReviewOperations[0]} {
		if op.Id == "" || op.Type != "apply" || op.ApiVersion != "gone.provider.io/v1" || op.Kind != "Disconnected" || op.ResourceName != op.Id || op.Source != "ui" || op.SubmittedAt != "2025-01-01T00:00:00Z" || op.StartedAt != "2025-01-01T00:01:00Z" || op.CompletedAt != "2025-01-01T00:02:00Z" {
			t.Fatalf("safe identity/timing metadata lost: %+v", op)
		}
		if op.Error != "" || op.Label != "" || op.ManifestJson != "" || op.Result != nil || len(op.Children) != 0 || op.ParentId != "" {
			t.Fatalf("overview returned detailed operation payload: %+v", op)
		}
	}
	encoded, err := protojson.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "SECRET") {
		t.Fatalf("wire response leaked sensitive persisted body: %s", encoded)
	}
}

func TestOverviewPropagatesReadAndContextErrors(t *testing.T) {
	h, db := openOverviewHandler(t)
	ctx, cancel := context.WithCancel(overviewViewerContext())
	cancel()
	if got, err := h.GetOverview(ctx, &apiv1.GetOverviewRequest{}); status.Code(err) != codes.Canceled || got != nil {
		t.Fatalf("canceled request = %+v, %v", got, err)
	}
	ctx, cancel = context.WithDeadline(overviewViewerContext(), time.Unix(1, 0))
	defer cancel()
	if got, err := h.GetOverview(ctx, &apiv1.GetOverviewRequest{}); status.Code(err) != codes.DeadlineExceeded || got != nil {
		t.Fatalf("expired request = %+v, %v", got, err)
	}
	if _, err := db.Exec(`DROP TABLE operations`); err != nil {
		t.Fatal(err)
	}
	if got, err := h.GetOverview(overviewViewerContext(), &apiv1.GetOverviewRequest{}); status.Code(err) != codes.Internal || got != nil {
		t.Fatalf("failed database read returned fabricated snapshot: %+v, %v", got, err)
	}
}
