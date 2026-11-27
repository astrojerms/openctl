package server

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/storage"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
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

func TestDryRunMetadataEditsReplaceWholeDesiredMaps(t *testing.T) {
	for _, tc := range []struct {
		name              string
		stored, submitted protocol.ResourceMetadata
		want              []*apiv1.DriftEntry
	}{
		{
			name:      "add label",
			submitted: protocol.ResourceMetadata{Labels: map[string]string{"env": "lab"}},
			want:      []*apiv1.DriftEntry{{Path: "labels.env", Desired: "<unset>", Observed: "lab"}},
		},
		{
			name:      "edit label",
			stored:    protocol.ResourceMetadata{Labels: map[string]string{"env": "lab"}},
			submitted: protocol.ResourceMetadata{Labels: map[string]string{"env": "prod"}},
			want:      []*apiv1.DriftEntry{{Path: "labels.env", Desired: "lab", Observed: "prod"}},
		},
		{
			name:   "clear labels with omitted map",
			stored: protocol.ResourceMetadata{Labels: map[string]string{"env": "lab"}},
			want:   []*apiv1.DriftEntry{{Path: "labels.env", Desired: "lab", Observed: "<unset>"}},
		},
		{
			name:      "clear annotations with empty map",
			stored:    protocol.ResourceMetadata{Annotations: map[string]string{"note": "reviewed"}},
			submitted: protocol.ResourceMetadata{Annotations: map[string]string{}},
			want:      []*apiv1.DriftEntry{{Path: "annotations.note", Desired: "reviewed", Observed: "<unset>"}},
		},
		{
			name:      "edit annotation",
			stored:    protocol.ResourceMetadata{Annotations: map[string]string{"note": "old"}},
			submitted: protocol.ResourceMetadata{Annotations: map[string]string{"note": "new"}},
			want:      []*apiv1.DriftEntry{{Path: "annotations.note", Desired: "old", Observed: "new"}},
		},
		{
			name:      "replace map keys including empty values",
			stored:    protocol.ResourceMetadata{Labels: map[string]string{"old": ""}},
			submitted: protocol.ResourceMetadata{Labels: map[string]string{"new": ""}},
			want: []*apiv1.DriftEntry{
				{Path: "labels.new", Desired: "<unset>", Observed: ""},
				{Path: "labels.old", Desired: "", Observed: "<unset>"},
			},
		},
		{
			name: "unchanged metadata",
			stored: protocol.ResourceMetadata{
				Labels: map[string]string{"env": "lab"}, Annotations: map[string]string{"note": "reviewed"},
			},
			submitted: protocol.ResourceMetadata{
				Labels: map[string]string{"env": "lab"}, Annotations: map[string]string{"note": "reviewed"},
			},
		},
		{
			name:      "absent and empty maps are equivalent",
			submitted: protocol.ResourceMetadata{Labels: map[string]string{}, Annotations: map[string]string{}},
		},
		{
			name:   "empty and absent maps are equivalent",
			stored: protocol.ResourceMetadata{Labels: map[string]string{}, Annotations: map[string]string{}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := ctxWithRole(auth.RoleViewer)
			stored, submitted := safetyManifest("vm", 1), safetyManifest("vm", 1)
			stored.Metadata, submitted.Metadata = tc.stored, tc.submitted
			stored.Metadata.Name, submitted.Metadata.Name = "vm", "vm"
			store := safetyStore(t)
			if err := store.Save(ctx, stored); err != nil {
				t.Fatal(err)
			}
			f := &fakeProvider{getReturn: safetyManifest("vm", 1)}
			reg := providers.NewRegistry()
			reg.Register(f)
			h := newResourceHandler(reg, nil, nil, store)
			wire, err := resourceToProto(submitted)
			if err != nil {
				t.Fatal(err)
			}
			out, err := h.DryRunApply(ctx, &apiv1.DryRunApplyRequest{Resource: wire})
			if err != nil {
				t.Fatal(err)
			}
			wantOperation := "no-op"
			if len(tc.want) > 0 {
				wantOperation = "update"
			}
			if out.Operation != wantOperation || len(out.Diff) != 0 || len(out.LiveDrift) != 0 || len(out.ValidationErrors) != 0 {
				t.Fatalf("preview=%+v", out)
			}
			if len(out.GetMetadataDiff()) != len(tc.want) {
				t.Fatalf("metadata diff=%+v, want %+v", out.GetMetadataDiff(), tc.want)
			}
			for i, want := range tc.want {
				got := out.GetMetadataDiff()[i]
				if got.Path != want.Path || got.Desired != want.Desired || got.Observed != want.Observed {
					t.Errorf("metadata diff[%d]=%+v, want %+v", i, got, want)
				}
			}
			if len(tc.want) > 0 && (out.Summary == "" || out.Summary == summarizeDryRun(out)) {
				t.Fatalf("summary does not account for metadata edits: %q", out.Summary)
			}
			after, err := store.Load(ctx, stored.APIVersion, stored.Kind, "vm")
			if err != nil {
				t.Fatal(err)
			}
			if after == nil || !maps.Equal(after.Metadata.Labels, tc.stored.Labels) || !maps.Equal(after.Metadata.Annotations, tc.stored.Annotations) {
				t.Fatal("preview changed stored desired metadata")
			}
			if !maps.Equal(wire.Metadata.Labels, tc.submitted.Labels) || !maps.Equal(wire.Metadata.Annotations, tc.submitted.Annotations) {
				t.Fatal("preview changed submitted metadata")
			}
			if len(f.applied) != 0 || len(f.deletes) != 0 {
				t.Fatal("preview mutated provider")
			}
		})
	}
}

func TestDryRunIgnoresSourceOnlyChanges(t *testing.T) {
	store := safetyStore(t)
	stored := safetyManifest("vm", 1)
	if err := store.Save(manifests.WithSource(context.Background(), manifests.SourceGitOps), stored); err != nil {
		t.Fatal(err)
	}
	reg := providers.NewRegistry()
	reg.Register(&fakeProvider{getReturn: safetyManifest("vm", 1)})
	h := newResourceHandler(reg, nil, nil, store)
	wire, err := resourceToProto(stored)
	if err != nil {
		t.Fatal(err)
	}
	ctx := metadata.NewIncomingContext(ctxWithRole(auth.RoleViewer), metadata.Pairs(sourceMetadataKey, manifests.SourceUI))
	out, err := h.DryRunApply(ctx, &apiv1.DryRunApplyRequest{Resource: wire})
	if err != nil {
		t.Fatal(err)
	}
	if out.Operation != "no-op" || len(out.GetMetadataDiff()) != 0 || len(out.Diff) != 0 || len(out.LiveDrift) != 0 {
		t.Fatalf("source-only preview=%+v", out)
	}
}

func TestDryRunCompositeSummaryIncludesMetadataEdits(t *testing.T) {
	store := safetyStore(t)
	stored := safetyManifest("vm", 1)
	if err := store.Save(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	f := &fakeProvider{getReturn: safetyManifest("vm", 1)}
	plan := &providers.DryRunResult{
		Summary:  "all provider children unchanged",
		Children: []providers.ChildAction{{Verb: "no-op", Kind: "FakeKind", Name: "child"}},
	}
	reg := providers.NewRegistry()
	reg.Register(&fakeDryRunner{fakeProvider: f, result: plan})
	h := newResourceHandler(reg, nil, nil, store)
	wire, err := resourceToProto(stored)
	if err != nil {
		t.Fatal(err)
	}
	req := &apiv1.DryRunApplyRequest{Resource: wire}
	baseline, err := h.DryRunApply(ctxWithRole(auth.RoleViewer), req)
	if err != nil {
		t.Fatal(err)
	}
	wire.Metadata.Annotations = map[string]string{"note": "reviewed"}
	out, err := h.DryRunApply(ctxWithRole(auth.RoleViewer), req)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Operation != "no-op" || out.Operation != "update" || len(out.GetMetadataDiff()) != 1 || len(out.Diff) != 0 || len(out.LiveDrift) != 0 {
		t.Fatalf("baseline=%+v, metadata edit=%+v", baseline, out)
	}
	if out.Summary == "" || out.Summary == baseline.Summary || out.Summary == plan.Summary {
		t.Fatalf("provider summary hid metadata update: %q", out.Summary)
	}
	if len(out.Children) != 1 || out.Children[0].Verb != "no-op" || len(out.RequiredGates) != 0 {
		t.Fatalf("metadata preview changed provider plan: %+v", out)
	}
	if len(f.applied) != 0 || len(f.deletes) != 0 {
		t.Fatal("preview mutated provider")
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
