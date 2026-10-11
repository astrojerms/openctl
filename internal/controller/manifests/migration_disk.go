package manifests

import (
	"context"
	"fmt"

	"github.com/openctl/openctl/pkg/protocol"
)

// Materialize projects committed adoption intent without performing another
// database save. Migration RPCs require GitOps watching to be disabled.
func (m *DiskMirror) Materialize(ctx context.Context, r *protocol.Resource) error {
	if r == nil {
		return fmt.Errorf("adoption manifest is required")
	}
	if err := CheckNotForgotten(ctx, m.store.db, r.APIVersion, r.Kind, r.Metadata.Name); err != nil {
		return err
	}
	if err := m.writeFile(r); err != nil {
		return fmt.Errorf("materialize adopted manifest: %w", err)
	}
	if m.hook != nil {
		return m.hook(ctx, r.Kind, r, "adopt")
	}
	return nil
}

// RemoveProjection removes only the mirror after the canonical release is
// committed. The persistent release marker fences future Git pulls/submissions.
func (m *DiskMirror) RemoveProjection(ctx context.Context, av, kind, name string) error {
	forgotten, err := m.store.IsForgotten(ctx, av, kind, name)
	if err != nil {
		return err
	}
	if !forgotten {
		return fmt.Errorf("%w: projection removal requires committed ownership release", ErrMigrationConflict)
	}
	if err := m.removeFile(av, kind, name); err != nil {
		return fmt.Errorf("remove forgotten manifest projection: %w", err)
	}
	if m.hook != nil {
		return m.hook(ctx, kind, &protocol.Resource{APIVersion: av, Kind: kind, Metadata: protocol.ResourceMetadata{Name: name}}, "forget")
	}
	return nil
}
