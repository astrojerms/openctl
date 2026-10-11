package manifests

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl/internal/controller/storage"
	"github.com/openctl/openctl/pkg/protocol"
)

func TestMigrationReleaseSurvivesRestartAndExplicitAdoption(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "controller.db")
	db, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s := New(db)
	vm := sampleVM(4)
	other := sampleVM(8)
	other.Metadata.Name = "dev-system"
	if err := s.Save(ctx, vm); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, other); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.ExportMigration(ctx, vm.APIVersion, vm.Kind, vm.Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Forget(ctx, snapshot.Resource, snapshot.ControllerID, snapshot.Digest); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	s = New(db)
	var forgotten *ForgottenError
	if err := s.Save(ctx, vm); !errors.As(err, &forgotten) {
		t.Fatalf("stale desired intent was not fenced after restart: %v", err)
	}
	if got, err := s.Load(ctx, vm.APIVersion, vm.Kind, vm.Metadata.Name); err != nil || got != nil {
		t.Fatalf("forgotten intent reappeared: %v, %v", got, err)
	}
	if got, err := s.Load(ctx, other.APIVersion, other.Kind, other.Metadata.Name); err != nil || got == nil || got.Spec["cpus"] != float64(8) {
		t.Fatalf("unrelated dev-system changed: %v, %v", got, err)
	}
	if err := s.Forget(ctx, snapshot.Resource, snapshot.ControllerID, snapshot.Digest); err != nil {
		t.Fatalf("same release could not be retried: %v", err)
	}
	if err := s.Adopt(ctx, snapshot.Resource); err != nil {
		t.Fatal(err)
	}
	if released, err := s.IsForgotten(ctx, vm.APIVersion, vm.Kind, vm.Metadata.Name); err != nil || released {
		t.Fatalf("explicit adoption did not clear release: %v, %v", released, err)
	}
	if got, err := s.Load(ctx, vm.APIVersion, vm.Kind, vm.Metadata.Name); err != nil || got == nil || got.Spec["cpus"] != float64(4) {
		t.Fatalf("adopted intent differs: %v, %v", got, err)
	}
}

func TestMigrationAnnotationChangeInvalidatesExport(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	vm := sampleVM(4)
	vm.Metadata.Annotations = map[string]string{"openctl.io/autoReconcile": "false"}
	if err := s.Save(ctx, vm); err != nil {
		t.Fatal(err)
	}
	exported, err := s.ExportMigration(ctx, vm.APIVersion, vm.Kind, vm.Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	vm.Metadata.Annotations["openctl.io/autoReconcile"] = "true"
	if err := s.Save(ctx, vm); err != nil {
		t.Fatal(err)
	}
	if err := s.Forget(ctx, exported.Resource, exported.ControllerID, exported.Digest); !errors.Is(err, ErrMigrationConflict) {
		t.Fatalf("stale annotation change allowed ownership release: %v", err)
	}
	got, err := s.Load(ctx, vm.APIVersion, vm.Kind, vm.Metadata.Name)
	if err != nil || got == nil || got.Metadata.Annotations["openctl.io/autoReconcile"] != "true" {
		t.Fatalf("newer automation intent was lost: %v, %v", got, err)
	}
}

func TestMigrationRejectsReferencesAndOpaqueState(t *testing.T) {
	for _, dependency := range []string{"inbound-reference", "outbound-reference", "owned-child", "provider-state"} {
		t.Run(dependency, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			vm := sampleVM(4)
			if dependency == "outbound-reference" {
				vm.Spec["dependency"] = map[string]any{"$ref": map[string]any{"apiVersion": "other.io/v1", "kind": "Other", "name": "other"}}
			}
			if err := s.Save(ctx, vm); err != nil {
				t.Fatal(err)
			}
			switch dependency {
			case "inbound-reference", "owned-child":
				child := &protocol.Resource{APIVersion: "other.io/v1", Kind: "Other", Metadata: protocol.ResourceMetadata{Name: "dependent"}, Spec: map[string]any{}}
				if dependency == "inbound-reference" {
					child.Spec["vm"] = map[string]any{"$ref": map[string]any{"apiVersion": vm.APIVersion, "kind": vm.Kind, "name": vm.Metadata.Name, "field": "status.ip"}}
				} else {
					child.Metadata.Labels = map[string]string{"openctl.io/owner-kind": vm.Kind, "openctl.io/owner-name": vm.Metadata.Name}
				}
				if err := s.Save(ctx, child); err != nil {
					t.Fatal(err)
				}
			case "provider-state":
				if _, err := s.db.ExecContext(ctx, `INSERT INTO provider_state (api_version, kind, name, state_blob, schema_version, updated_at) VALUES (?, ?, ?, ?, 1, 'now')`, vm.APIVersion, vm.Kind, vm.Metadata.Name, []byte("opaque-identity")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.ExportMigration(ctx, vm.APIVersion, vm.Kind, vm.Metadata.Name); !errors.Is(err, ErrMigrationUnsupported) {
				t.Fatalf("unsafe %s exported: %v", dependency, err)
			}
			if err := s.Adopt(ctx, vm); !errors.Is(err, ErrMigrationUnsupported) {
				t.Fatalf("unsafe %s adopted: %v", dependency, err)
			}
			if got, err := s.Load(ctx, vm.APIVersion, vm.Kind, vm.Metadata.Name); err != nil || got == nil || got.Spec["cpus"] != float64(4) {
				t.Fatalf("refusal altered original intent: %v, %v", got, err)
			}
		})
	}
}
