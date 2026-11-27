package server

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/storage"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
)

func openInventoryHandler(t *testing.T) (*inventoryHandler, *sql.DB) {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "inventory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return newInventoryHandler(manifests.New(db)), db
}

func inventoryViewerContext() context.Context {
	return auth.ContextWithPrincipal(context.Background(), auth.Principal{UserID: "reader", Role: auth.RoleViewer})
}

func seedInventoryRow(t *testing.T, db *sql.DB, name, spec, metadata string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO applied_manifests
		(api_version, kind, name, spec_json, metadata_json, applied_at, source)
		VALUES ('gone.provider/v1', 'Machine', ?, ?, ?, '2026-01-02T03:04:05.123Z', 'gitops')`, name, spec, metadata)
	if err != nil {
		t.Fatal(err)
	}
}

func TestInventoryListsUnregisteredAndOwnedSavedMetadataWithoutSpec(t *testing.T) {
	h, db := openInventoryHandler(t) // No provider registration is possible here.
	seedInventoryRow(t, db, "z-owned", "invalid SECRET-spec JSON", `{"name":"z-owned","labels":{"openctl.io/owner-kind":"Cluster","openctl.io/owner-name":"parent"},"annotations":{"note":"keep"}}`)
	seedInventoryRow(t, db, "a-large", `{"payload":"`+strings.Repeat("SECRET-payload", 100000)+`"}`, `{"name":"a-large"}`)
	seedInventoryRow(t, db, "legacy", `{}`, `{"labels":{"team":"infra"}}`)
	if _, err := db.Exec(`INSERT INTO applied_manifests
		(api_version, kind, name, spec_json, metadata_json, applied_at)
		VALUES ('different/v1', 'Machine', 'hidden', '{}', '{}', '2026-01-01T00:00:00Z'),
		('gone.provider/v1', 'Other', 'hidden', '{}', '{}', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	got, err := h.ListSavedResources(inventoryViewerContext(), &apiv1.ListRequest{ApiVersion: "gone.provider/v1", Kind: "Machine"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Resources) != 3 || got.Resources[0].Metadata.Name != "a-large" || got.Resources[1].Metadata.Name != "legacy" || got.Resources[2].Metadata.Name != "z-owned" {
		t.Fatalf("inventory lost or misordered saved rows: %+v", got)
	}
	owned := got.Resources[2]
	if owned.Metadata.Labels["openctl.io/owner-name"] != "parent" || owned.Metadata.Annotations["note"] != "keep" || len(owned.Metadata.OwnerRefs) != 0 {
		t.Fatalf("saved metadata changed or became live ownership: %+v", owned)
	}
	for _, item := range got.Resources {
		if item.ApiVersion != "gone.provider/v1" || item.Kind != "Machine" || item.Source != "gitops" || item.AppliedAt != "2026-01-02T03:04:05.123Z" {
			t.Fatalf("lost saved identity/provenance: %+v", item)
		}
	}
	wire, err := protojson.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "SECRET") || strings.Contains(string(wire), `"spec"`) || strings.Contains(string(wire), `"status"`) {
		t.Fatalf("metadata list leaked saved bodies: %s", wire)
	}
}

func TestInventoryGetReturnsRawIntentWithoutObservedFields(t *testing.T) {
	h, db := openInventoryHandler(t)
	want := &protocol.Resource{
		APIVersion: "gone.provider/v1", Kind: "Machine",
		Metadata: protocol.ResourceMetadata{
			Name:        "owned",
			Labels:      map[string]string{"openctl.io/owner-kind": "Cluster", "openctl.io/owner-name": "parent", "team": "ops"},
			Annotations: map[string]string{"note": "keep exactly", "credential": "${secret:ops/token}"},
		},
		Spec: map[string]any{
			"password": "${secret:ops/password}",
			"network":  map[string]any{"$ref": "other.provider/v1/Network/main.status.id"},
			"nested":   []any{map[string]any{"$secret": "ops/token"}},
		},
		Status: map[string]any{"ready": true, "address": "observed-only"},
	}
	if err := h.store.Save(manifests.WithSource(context.Background(), manifests.SourceUI), want); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE applied_manifests SET applied_at = '2026-01-02T03:04:05Z' WHERE name = 'owned'`); err != nil {
		t.Fatal(err)
	}
	got, err := h.GetSavedResource(inventoryViewerContext(), &apiv1.GetRequest{ApiVersion: want.APIVersion, Kind: want.Kind, Name: want.Metadata.Name})
	if err != nil || got == nil || got.Resource == nil {
		t.Fatalf("get saved intent = %+v, %v", got, err)
	}
	r := got.Resource
	if r.ApiVersion != want.APIVersion || r.Kind != want.Kind || r.Metadata.Name != want.Metadata.Name || !reflect.DeepEqual(r.Metadata.Labels, want.Metadata.Labels) || !reflect.DeepEqual(r.Metadata.Annotations, want.Metadata.Annotations) || !reflect.DeepEqual(r.Spec.AsMap(), want.Spec) {
		t.Fatalf("raw desired intent changed: %+v", got)
	}
	if got.Source != manifests.SourceUI || got.AppliedAt != "2026-01-02T03:04:05Z" || r.Status != nil || len(r.Drift) != 0 || len(r.Children) != 0 || len(r.Metadata.OwnerRefs) != 0 {
		t.Fatalf("saved record lost provenance or acquired observations: %+v", got)
	}
}

func TestInventoryViewerAndUnavailableBehavior(t *testing.T) {
	h, _ := openInventoryHandler(t)
	listReq := &apiv1.ListRequest{ApiVersion: "gone.provider/v1", Kind: "Machine"}
	getReq := &apiv1.GetRequest{ApiVersion: "gone.provider/v1", Kind: "Machine", Name: "missing"}
	for _, role := range []auth.Role{auth.RoleViewer, auth.RoleEditor, auth.RoleAdmin} {
		ctx := auth.ContextWithPrincipal(context.Background(), auth.Principal{UserID: "reader", Role: role})
		got, err := h.ListSavedResources(ctx, listReq)
		if err != nil || got == nil || len(got.Resources) != 0 {
			t.Fatalf("role %q empty inventory = %+v, %v", role, got, err)
		}
		if got, err := h.GetSavedResource(ctx, getReq); got != nil || status.Code(err) != codes.NotFound {
			t.Fatalf("role %q missing saved resource = %+v, %v", role, got, err)
		}
	}
	for _, role := range []auth.Role{"", "invalid"} {
		ctx := auth.ContextWithPrincipal(context.Background(), auth.Principal{UserID: "reader", Role: role})
		if got, err := h.ListSavedResources(ctx, listReq); got != nil || status.Code(err) != codes.PermissionDenied {
			t.Fatalf("role %q list = %+v, %v", role, got, err)
		}
		if got, err := h.GetSavedResource(ctx, getReq); got != nil || status.Code(err) != codes.PermissionDenied {
			t.Fatalf("role %q get = %+v, %v", role, got, err)
		}
	}
	unavailable := newInventoryHandler(nil)
	if got, err := unavailable.ListSavedResources(inventoryViewerContext(), listReq); got != nil || status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unavailable list = %+v, %v", got, err)
	}
	if got, err := unavailable.GetSavedResource(inventoryViewerContext(), getReq); got != nil || status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unavailable get = %+v, %v", got, err)
	}
}

func TestInventoryValidatesIdentity(t *testing.T) {
	h, _ := openInventoryHandler(t)
	for _, req := range []*apiv1.ListRequest{nil, {}, {ApiVersion: "gone/v1"}, {Kind: "Machine"}, {ApiVersion: " \t", Kind: "Machine"}} {
		if got, err := h.ListSavedResources(inventoryViewerContext(), req); got != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid list identity %+v = %+v, %v", req, got, err)
		}
	}
	for _, req := range []*apiv1.GetRequest{nil, {}, {ApiVersion: "gone/v1", Kind: "Machine"}, {Kind: "Machine", Name: "x"}, {ApiVersion: "gone/v1", Name: "x"}, {ApiVersion: "gone/v1", Kind: "Machine", Name: " \t"}} {
		if got, err := h.GetSavedResource(inventoryViewerContext(), req); got != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid get identity %+v = %+v, %v", req, got, err)
		}
	}
}

func TestInventoryMalformedRecordsAreNotEmptyOrMissing(t *testing.T) {
	for _, tc := range []struct{ name, spec, metadata, detail string }{
		{"metadata", `{}`, `{"labels":{"team":42}}`, "decode metadata"},
		{"identity", `{}`, `{"name":"different"}`, "disagrees with row name"},
		{"spec", `not JSON`, `{"name":"target"}`, "decode spec"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, db := openInventoryHandler(t)
			seedInventoryRow(t, db, "target", tc.spec, tc.metadata)
			if got, err := h.GetSavedResource(inventoryViewerContext(), &apiv1.GetRequest{ApiVersion: "gone.provider/v1", Kind: "Machine", Name: "target"}); got != nil || status.Code(err) != codes.Internal || !strings.Contains(err.Error(), tc.detail) {
				t.Fatalf("malformed get = %+v, %v", got, err)
			}
			if tc.name != "spec" {
				if got, err := h.ListSavedResources(inventoryViewerContext(), &apiv1.ListRequest{ApiVersion: "gone.provider/v1", Kind: "Machine"}); got != nil || status.Code(err) != codes.Internal || !strings.Contains(err.Error(), tc.detail) {
					t.Fatalf("malformed list = %+v, %v", got, err)
				}
			}
		})
	}
}

func TestInventoryStorageAndContextFailuresRemainErrors(t *testing.T) {
	h, db := openInventoryHandler(t)
	listReq := &apiv1.ListRequest{ApiVersion: "gone.provider/v1", Kind: "Machine"}
	getReq := &apiv1.GetRequest{ApiVersion: "gone.provider/v1", Kind: "Machine", Name: "target"}
	canceled, cancel := context.WithCancel(inventoryViewerContext())
	cancel()
	expired, cancelDeadline := context.WithDeadline(inventoryViewerContext(), time.Unix(0, 0))
	defer cancelDeadline()
	for _, tc := range []struct {
		ctx  context.Context
		code codes.Code
	}{{canceled, codes.Canceled}, {expired, codes.DeadlineExceeded}} {
		if got, err := h.ListSavedResources(tc.ctx, listReq); got != nil || status.Code(err) != tc.code {
			t.Fatalf("context list = %+v, %v", got, err)
		}
		if got, err := h.GetSavedResource(tc.ctx, getReq); got != nil || status.Code(err) != tc.code {
			t.Fatalf("context get = %+v, %v", got, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := h.ListSavedResources(inventoryViewerContext(), listReq); got != nil || status.Code(err) != codes.Internal || !strings.Contains(err.Error(), "read saved inventory") {
		t.Fatalf("failed store list = %+v, %v", got, err)
	}
	if got, err := h.GetSavedResource(inventoryViewerContext(), getReq); got != nil || status.Code(err) != codes.Internal || !strings.Contains(err.Error(), "read saved inventory") {
		t.Fatalf("failed store get = %+v, %v", got, err)
	}
}
