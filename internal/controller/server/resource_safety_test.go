package server

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/storage"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func safetyManifest(name string, value int) *protocol.Resource {
	return &protocol.Resource{APIVersion: "fake.openctl.io/v1", Kind: "FakeKind", Metadata: protocol.ResourceMetadata{Name: name}, Spec: map[string]any{"value": value}}
}

func safetyStore(t *testing.T) *manifests.Store {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return manifests.New(db)
}

func TestDeleteRequiresTrackingAndBlocksPersistedDependents(t *testing.T) {
	ctx := context.Background()
	f := &fakeProvider{}
	reg := providers.NewRegistry()
	reg.Register(f)
	h := newResourceHandler(reg, nil, nil, safetyStore(t))
	req := &apiv1.DeleteRequest{ApiVersion: "fake.openctl.io/v1", Kind: "FakeKind", Name: "vm"}
	if _, err := h.Delete(ctx, req); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unmanaged delete err=%v", err)
	}
	if err := h.manifests.Save(ctx, safetyManifest("vm", 1)); err != nil {
		t.Fatal(err)
	}
	dependent := safetyManifest("dependent", 1)
	dependent.Spec["vm"] = map[string]any{"$ref": map[string]any{"apiVersion": "fake.openctl.io/v1", "kind": "FakeKind", "name": "vm"}}
	if err := h.manifests.Save(ctx, dependent); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Delete(ctx, req); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("dependent delete err=%v", err)
	}
	if len(f.deletes) != 0 {
		t.Fatalf("unsafe deletes reached provider: %v", f.deletes)
	}
	dependent.Spec["vm"] = map[string]any{"$ref": map[string]any{"name": "vm"}}
	if err := h.manifests.Save(ctx, dependent); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Delete(ctx, req); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("malformed dependency metadata allowed deletion: %v", err)
	}
}

type unavailableOwnerProvider struct{ fakeProvider }

func (p *unavailableOwnerProvider) CheckedOwnerOf(context.Context, string, string) (string, string, bool, error) {
	return "", "", false, fmt.Errorf("ownership unavailable")
}

func TestDeleteFailsClosedOnOwnershipReadFailure(t *testing.T) {
	f := &unavailableOwnerProvider{}
	reg := providers.NewRegistry()
	reg.Register(f)
	store := safetyStore(t)
	if err := store.Save(context.Background(), safetyManifest("vm", 1)); err != nil {
		t.Fatal(err)
	}
	h := newResourceHandler(reg, nil, nil, store)
	_, err := h.Delete(context.Background(), &apiv1.DeleteRequest{ApiVersion: "fake.openctl.io/v1", Kind: "FakeKind", Name: "vm"})
	if status.Code(err) != codes.FailedPrecondition || len(f.deletes) != 0 {
		t.Fatalf("ownership failure allowed mutation: err=%v deletes=%v", err, f.deletes)
	}
}

func TestDryRunOperationSeparatesIntentEditsFromLiveDrift(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		saved                        bool
		desired, submitted, observed int
		operation                    string
		diff, live                   int
	}{
		{"first apply", false, 1, 1, 1, "create", 0, 0},
		{"unchanged", true, 1, 1, 1, "no-op", 0, 0},
		{"live drift", true, 1, 1, 2, "update", 0, 1},
		{"intent edit already live", true, 1, 2, 2, "update", 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := safetyStore(t)
			if tc.saved {
				if err := store.Save(context.Background(), safetyManifest("vm", tc.desired)); err != nil {
					t.Fatal(err)
				}
			}
			f := &fakeProvider{getReturn: safetyManifest("vm", tc.observed)}
			reg := providers.NewRegistry()
			reg.Register(f)
			h := newResourceHandler(reg, nil, nil, store)
			wire, err := resourceToProto(safetyManifest("vm", tc.submitted))
			if err != nil {
				t.Fatal(err)
			}
			out, err := h.DryRunApply(ctxWithRole(auth.RoleViewer), &apiv1.DryRunApplyRequest{Resource: wire})
			if err != nil {
				t.Fatal(err)
			}
			if out.Operation != tc.operation || len(out.Diff) != tc.diff || len(out.LiveDrift) != tc.live {
				t.Fatalf("preview=%+v", out)
			}
			if len(f.applied) != 0 || len(f.deletes) != 0 {
				t.Fatal("preview mutated provider")
			}
		})
	}
}

func TestCreateOnlyRejectsUnmanagedPhysicalCollision(t *testing.T) {
	f := &fakeProvider{getReturn: safetyManifest("vm", 1)}
	reg := providers.NewRegistry()
	reg.Register(f)
	h := newResourceHandler(reg, nil, nil, safetyStore(t))
	wire, err := resourceToProto(safetyManifest("vm", 1))
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.Apply(context.Background(), &apiv1.ApplyRequest{Resource: wire, CreateOnly: true})
	if status.Code(err) != codes.AlreadyExists || len(f.applied) != 0 {
		t.Fatalf("existing VM mutated: err=%v applied=%v", err, f.applied)
	}
}
