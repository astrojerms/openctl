package manifests

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

func seedSavedRow(t *testing.T, s *Store, name, spec, metadata, appliedAt string) {
	t.Helper()
	_, err := s.db.Exec(`INSERT INTO applied_manifests
		(api_version, kind, name, spec_json, metadata_json, applied_at, source)
		VALUES ('missing.provider/v1', 'Machine', ?, ?, ?, ?, 'gitops')`, name, spec, metadata, appliedAt)
	if err != nil {
		t.Fatal(err)
	}
}

func TestSavedMetadataIgnoresInvalidAndLargeSpecs(t *testing.T) {
	s := newTestStore(t)
	seedSavedRow(t, s, "invalid", "not JSON", `{"name":"invalid"}`, "2026-01-01T00:00:00Z")
	seedSavedRow(t, s, "large", `{"payload":"`+strings.Repeat("SECRET", 200000)+`"}`, `{"name":"large"}`, "2026-01-01T00:00:00Z")
	got, err := s.ListSavedMetadata(context.Background(), "missing.provider/v1", "Machine")
	if err != nil || len(got) != 2 {
		t.Fatalf("metadata list depends on specs: %+v, %v", got, err)
	}
	if got, err := s.LoadSavedRecord(context.Background(), "missing.provider/v1", "Machine", "invalid"); got != nil || err == nil || !strings.Contains(err.Error(), "decode spec") {
		t.Fatalf("invalid spec was not actionable: %+v, %v", got, err)
	}
}

func TestSavedRecordRetainsIntentAndProvenance(t *testing.T) {
	s := newTestStore(t)
	want := &protocol.Resource{
		APIVersion: "missing.provider/v1", Kind: "Machine",
		Metadata: protocol.ResourceMetadata{
			Name: "owned", Namespace: "site", UID: "author-uid", CreatedAt: "author-time",
			Labels:      map[string]string{"openctl.io/owner-kind": "Cluster", "openctl.io/owner-name": "parent"},
			Annotations: map[string]string{"note": "untouched", "token": "${secret:ops/token}"},
		},
		Spec: map[string]any{
			"secret":   "${secret:ops/password}",
			"upstream": map[string]any{"$ref": "other.provider/v1/Network/main.status.id"},
		},
		Status: map[string]any{"ready": true},
	}
	if err := s.Save(WithSource(context.Background(), SourceUI), want); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadSavedRecord(context.Background(), want.APIVersion, want.Kind, want.Metadata.Name)
	if err != nil || got == nil {
		t.Fatalf("load saved record = %+v, %v", got, err)
	}
	want.Status = nil // Observed status was never persisted as desired intent.
	if !reflect.DeepEqual(got.Resource, want) || got.Source != SourceUI || got.AppliedAt.IsZero() {
		t.Fatalf("intent/provenance changed: %+v", got)
	}
}

func TestSavedReadsRejectMalformedMetadataAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, metadata, appliedAt, detail string
	}{
		{"invalid-json", `{`, "", "decode metadata"},
		{"invalid-labels", `{"labels":{"team":42}}`, "", "decode metadata"},
		{"invalid-object", `[]`, "", "decode metadata"},
		{"wrong-name", `{"name":"someone-else","labels":{"keep":"yes"}}`, "", "disagrees with row name"},
		{"invalid-time", `{}`, "yesterday", "decode applied_at"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			seedSavedRow(t, s, "target", `{}`, tc.metadata, tc.appliedAt)
			if got, err := s.ListSavedMetadata(context.Background(), "missing.provider/v1", "Machine"); got != nil || err == nil || !strings.Contains(err.Error(), tc.detail) {
				t.Fatalf("malformed list = %+v, %v", got, err)
			}
			if got, err := s.LoadSavedRecord(context.Background(), "missing.provider/v1", "Machine", "target"); got != nil || err == nil || !strings.Contains(err.Error(), tc.detail) {
				t.Fatalf("malformed record = %+v, %v", got, err)
			}
		})
	}
}

func TestSavedReadsMissingLegacyAndFailures(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if got, err := s.LoadSavedRecord(ctx, "missing.provider/v1", "Machine", "absent"); got != nil || err != nil {
		t.Fatalf("missing record = %+v, %v", got, err)
	}
	if got, err := s.ListSavedMetadata(ctx, "missing.provider/v1", "Machine"); got == nil || len(got) != 0 || err != nil {
		t.Fatalf("valid empty list = %+v, %v", got, err)
	}
	seedSavedRow(t, s, "legacy", "null", `{"labels":{"team":"infra"}}`, "")
	got, err := s.LoadSavedRecord(ctx, "missing.provider/v1", "Machine", "legacy")
	if err != nil || got == nil || got.Resource.Metadata.Name != "legacy" || got.Resource.Metadata.Labels["team"] != "infra" || !got.AppliedAt.IsZero() {
		t.Fatalf("legacy name fallback lost metadata: %+v, %v", got, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if got, err := s.ListSavedMetadata(canceled, "missing.provider/v1", "Machine"); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled list = %+v, %v", got, err)
	}
	if got, err := s.LoadSavedRecord(canceled, "missing.provider/v1", "Machine", "legacy"); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled record = %+v, %v", got, err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ListSavedMetadata(ctx, "missing.provider/v1", "Machine"); got != nil || err == nil {
		t.Fatalf("closed store list = %+v, %v", got, err)
	}
	if got, err := s.LoadSavedRecord(ctx, "missing.provider/v1", "Machine", "legacy"); got != nil || err == nil {
		t.Fatalf("closed store record = %+v, %v", got, err)
	}
}
