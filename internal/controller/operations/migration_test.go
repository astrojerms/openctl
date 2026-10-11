package operations

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/storage"
	"github.com/openctl/openctl/pkg/protocol"
)

func TestReleasedResourceCannotBeQueuedByGitOps(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	intent := manifests.New(db)
	vm := &protocol.Resource{APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine", Metadata: protocol.ResourceMetadata{Name: "runner"}, Spec: map[string]any{"node": "pve-test"}}
	if err := intent.Save(ctx, vm); err != nil {
		t.Fatal(err)
	}
	snapshot, err := intent.ExportMigration(ctx, vm.APIVersion, vm.Kind, vm.Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := intent.Forget(ctx, snapshot.Resource, snapshot.ControllerID, snapshot.Digest); err != nil {
		t.Fatal(err)
	}
	ops := New(db, 0)
	for _, operationType := range []string{TypeApply, TypeDelete} {
		_, err := ops.Submit(ctx, &Operation{Type: operationType, APIVersion: vm.APIVersion, Kind: vm.Kind, ResourceName: vm.Metadata.Name, Source: manifests.SourceGitOps})
		var forgotten *manifests.ForgottenError
		if !errors.As(err, &forgotten) {
			t.Fatalf("%s could be queued after release: %v", operationType, err)
		}
	}
	var queued int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM operations WHERE status IN ('pending', 'running')`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 0 {
		t.Fatalf("release rejection left %d runnable operations", queued)
	}
	if err := intent.Adopt(ctx, snapshot.Resource); err != nil {
		t.Fatal(err)
	}
	accepted, err := ops.Submit(ctx, &Operation{Type: TypeApply, APIVersion: vm.APIVersion, Kind: vm.Kind, ResourceName: vm.Metadata.Name, Source: manifests.SourceCLI})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != StatusPending {
		t.Fatalf("explicit reclamation did not restore ordinary management: %s", accepted.Status)
	}
}
