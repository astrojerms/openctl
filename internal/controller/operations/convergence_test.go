package operations

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/secrets"
	"github.com/openctl/openctl/pkg/protocol"
)

func submitIntent(t *testing.T, store *Store, raw *protocol.Resource, createOnly bool) *Operation {
	t.Helper()
	body, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	op, err := store.Submit(context.Background(), &Operation{
		Type: TypeApply, APIVersion: raw.APIVersion, Kind: raw.Kind,
		ResourceName: raw.Metadata.Name, ManifestJSON: string(body), CreateOnly: createOnly,
	})
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func TestUnchangedIntentReappliesLiveDrift(t *testing.T) {
	for _, inline := range []bool{false, true} {
		for _, tc := range []struct {
			name string
			live map[string]any
		}{
			{"changed scalar", map[string]any{"cores": 8}},
			{"missing field", map[string]any{}},
			{"no observable spec", nil},
			{"different scalar type", map[string]any{"cores": "2"}},
		} {
			t.Run(tc.name+map[bool]string{false: "/operation", true: "/child"}[inline], func(t *testing.T) {
				raw := childManifest("fake.openctl.io/v1", "FakeKind", "x")
				raw.Spec = map[string]any{"cores": 2}
				live := *raw
				live.Spec = tc.live
				p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}, getOut: &live}
				store, d := newDispatcherWithManifests(t, p)
				if inline {
					for range 2 {
						if _, err := d.ApplyManifest(context.Background(), raw); err != nil {
							t.Fatal(err)
						}
					}
				} else {
					for range 2 {
						op := submitIntent(t, store, raw, false)
						final := runPendingOp(t, d, store, op.ID, StatusSucceeded)
						if final.Label != "" {
							t.Fatalf("drifted resource reported cached: %s", final.Label)
						}
					}
				}
				if got := p.applies.Load(); got != 2 {
					t.Fatalf("unchanged intent masked live drift: %d applies", got)
				}
			})
		}
	}
}

type projectedCacheProvider struct {
	*fakeProvider
	unobservable bool
}

func (p *projectedCacheProvider) DriftSpecs(_ string, desired, observed map[string]any) (map[string]any, map[string]any) {
	if p.unobservable {
		return nil, nil
	}
	return map[string]any{"cores": desired["cores"]}, observed
}

type observedOnlyCacheProvider struct{ *fakeProvider }

func (p *observedOnlyCacheProvider) ObservedOnlyKinds() []string { return []string{"FakeKind"} }

func TestCacheRequiresObservableManagedConvergence(t *testing.T) {
	for _, tc := range []struct {
		name        string
		provider    func(*fakeProvider) providers.Provider
		wantApplies int32
	}{
		{"projected live match", func(p *fakeProvider) providers.Provider { return &projectedCacheProvider{fakeProvider: p} }, 1},
		{"unobservable projection", func(p *fakeProvider) providers.Provider {
			return &projectedCacheProvider{fakeProvider: p, unobservable: true}
		}, 2},
		{"observed only", func(p *fakeProvider) providers.Provider { return &observedOnlyCacheProvider{p} }, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := childManifest("fake.openctl.io/v1", "FakeKind", "x")
			raw.Spec = map[string]any{"cores": 2, "creationOnly": "not returned by provider"}
			live := *raw
			live.Spec = map[string]any{"cores": float64(2), "providerDefault": true}
			p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}, getOut: &live}
			store, d := newDispatcherWithManifests(t, p)
			d.registry = providers.NewRegistry()
			d.registry.Register(tc.provider(p))
			for range 2 {
				op := submitIntent(t, store, raw, false)
				runPendingOp(t, d, store, op.ID, StatusSucceeded)
			}
			if got := p.applies.Load(); got != tc.wantApplies {
				t.Fatalf("Apply count = %d, want %d", got, tc.wantApplies)
			}
		})
	}
}

func TestCachedMetadataOnlyApplyPersistsIntent(t *testing.T) {
	for _, inline := range []bool{false, true} {
		t.Run(map[bool]string{false: "operation", true: "child"}[inline], func(t *testing.T) {
			raw := childManifest("fake.openctl.io/v1", "FakeKind", "x")
			raw.Spec = map[string]any{"cores": 2}
			p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}, getOut: raw}
			store, d := newDispatcherWithManifests(t, p)
			apply := func() {
				if inline {
					if _, err := d.ApplyManifest(context.Background(), raw); err != nil {
						t.Fatal(err)
					}
				} else {
					op := submitIntent(t, store, raw, false)
					runPendingOp(t, d, store, op.ID, StatusSucceeded)
				}
			}
			apply()
			raw.Metadata.Annotations = map[string]string{"openctl.io/autoReconcile": "true"}
			apply()
			loaded, err := d.manifests.(*manifests.Store).Load(context.Background(), raw.APIVersion, raw.Kind, raw.Metadata.Name)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Metadata.Annotations["openctl.io/autoReconcile"] != "true" {
				t.Fatal("cache discarded metadata-only desired-state update")
			}
			if got := p.applies.Load(); got != 1 {
				t.Fatalf("verified metadata-only change ran Apply %d times", got)
			}
		})
	}
}

type failingIntentSink struct {
	ManifestSink
	failure    error
	failSave   bool
	failDelete bool
}

func (s *failingIntentSink) SaveWithRefsHash(ctx context.Context, raw *protocol.Resource, hash string) error {
	if s.failSave {
		return s.failure
	}
	return s.ManifestSink.SaveWithRefsHash(ctx, raw, hash)
}

func (s *failingIntentSink) Delete(ctx context.Context, version, kind, name string) error {
	if s.failDelete {
		return s.failure
	}
	return s.ManifestSink.Delete(ctx, version, kind, name)
}

func TestDesiredStateFailuresFailOperationsWithoutPretendingRollback(t *testing.T) {
	for _, phase := range []string{"apply", "cached metadata", "delete"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			raw := childManifest("fake.openctl.io/v1", "FakeKind", "x")
			raw.Spec = map[string]any{"cores": 2}
			p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}, getOut: raw}
			store, d := newDispatcherWithManifests(t, p)
			intentStore := d.manifests.(*manifests.Store)
			if phase != "apply" {
				op := submitIntent(t, store, raw, false)
				runPendingOp(t, d, store, op.ID, StatusSucceeded)
			}
			failure := errors.New("desired-state disk is full")
			d.manifests = &failingIntentSink{ManifestSink: d.manifests, failure: failure, failSave: true, failDelete: true}
			var op *Operation
			if phase == "delete" {
				var err error
				op, err = store.Submit(ctx, &Operation{Type: TypeDelete, APIVersion: raw.APIVersion, Kind: raw.Kind, ResourceName: raw.Metadata.Name})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				raw.Metadata.Annotations = map[string]string{"openctl.io/autoReconcile": "true"}
				op = submitIntent(t, store, raw, false)
			}
			final := runPendingOp(t, d, store, op.ID, StatusFailed)
			if final.Error == "" {
				t.Fatal("partial infrastructure success lost its failure explanation")
			}
			if got := p.applies.Load(); got != 1 {
				t.Fatalf("unexpected Apply/rollback calls: %d", got)
			}
			wantDeletes := int32(0)
			if phase == "delete" {
				wantDeletes = 1
			}
			if got := p.deletes.Load(); got != wantDeletes {
				t.Fatalf("unexpected Delete/rollback calls: %d", got)
			}
			loaded, err := intentStore.Load(ctx, raw.APIVersion, raw.Kind, raw.Metadata.Name)
			if err != nil {
				t.Fatal(err)
			}
			if phase == "apply" && loaded != nil {
				t.Fatal("failed save unexpectedly recorded desired state")
			}
			if phase != "apply" && (loaded == nil || loaded.Metadata.Annotations["openctl.io/autoReconcile"] != "") {
				t.Fatal("failed persistence did not retain the previous desired state")
			}
		})
	}
}

func TestChildPersistenceFailuresPreserveUnderlyingCause(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		t.Run(map[bool]string{false: "apply", true: "delete"}[deleting], func(t *testing.T) {
			failure := errors.New("desired-state disk is read only")
			p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}}
			sink := &failingIntentSink{ManifestSink: &recordingManifestSink{}, failure: failure, failSave: true, failDelete: true}
			d := newDispatcherWithSink(t, p, sink)
			raw := childManifest("fake.openctl.io/v1", "FakeKind", "x")
			var err error
			if deleting {
				err = d.DeleteManifest(context.Background(), raw)
			} else {
				_, err = d.ApplyManifest(context.Background(), raw)
			}
			var partial *partialSuccessError
			if !errors.Is(err, failure) || !errors.As(err, &partial) {
				t.Fatalf("child did not expose partial success and persistence cause: %v", err)
			}
			d.userCancelled["parent"] = true
			if status, _ := d.terminalOutcome("parent", err); status != StatusFailed {
				t.Fatal("cancellation hid failed desired-state persistence")
			}
		})
	}
}

func TestCreateOnlyAdmissionRejectsManagedIntent(t *testing.T) {
	raw := childManifest("fake.openctl.io/v1", "FakeKind", "x")
	raw.Spec = map[string]any{"cores": 2}
	p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}}
	store, d := newDispatcherWithManifests(t, p)
	if err := d.manifests.Save(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	_, err := store.Submit(context.Background(), &Operation{
		Type: TypeApply, APIVersion: raw.APIVersion, Kind: raw.Kind, ResourceName: raw.Metadata.Name, CreateOnly: true,
	})
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !conflict.ResourceExists {
		t.Fatalf("create admitted an existing desired resource: %v", err)
	}
	ops, err := store.List(context.Background(), ListFilter{})
	if err != nil || len(ops) != 0 {
		t.Fatalf("rejected create left operations: %v, %v", ops, err)
	}
}

func TestCreateOnlySurvivesPersistenceAndFailsClosedOnLiveState(t *testing.T) {
	for _, tc := range []struct {
		name        string
		live        *protocol.Resource
		getErr      error
		wantStatus  string
		wantApplies int32
	}{
		{"confirmed absent", nil, providers.NotFound("FakeKind", "x"), StatusSucceeded, 1},
		{"already exists", childManifest("fake.openctl.io/v1", "FakeKind", "x"), nil, StatusFailed, 0},
		{"read outage", nil, errors.New("provider unavailable"), StatusFailed, 0},
		{"ambiguous empty success", nil, nil, StatusFailed, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}, getOut: tc.live, getErr: tc.getErr}
			store, d := newDispatcherWithManifests(t, p)
			raw := childManifest("fake.openctl.io/v1", "FakeKind", "x")
			op := submitIntent(t, store, raw, true)
			// A fresh Store reads the durable field, just as after a restart.
			reopened := New(store.db, 50)
			persisted, err := reopened.Get(context.Background(), op.ID)
			if err != nil || !persisted.CreateOnly {
				t.Fatalf("create-only lost after persistence: %+v, %v", persisted, err)
			}
			runPendingOp(t, d, reopened, op.ID, tc.wantStatus)
			if got := p.applies.Load(); got != tc.wantApplies {
				t.Fatalf("Apply calls = %d, want %d", got, tc.wantApplies)
			}
		})
	}
}

type recoverableCreateProvider struct{ *fakeProvider }

func (*recoverableCreateProvider) CanResumeCreate(context.Context, string, string) (bool, error) {
	return true, nil
}

func TestCreateRecoveryCannotOverwriteBaselineSavedWhileQueued(t *testing.T) {
	raw := childManifest("fake.openctl.io/v1", "FakeKind", "x")
	raw.Spec = map[string]any{"cores": 2}
	p := &recoverableCreateProvider{&fakeProvider{
		name: "fake", kinds: []string{"FakeKind"}, getOut: raw,
	}}
	store, d := newDispatcherWithManifests(t, p)
	op := submitIntent(t, store, raw, true)
	baseline := *raw
	baseline.Spec = map[string]any{"cores": 8}
	if err := d.manifests.Save(context.Background(), &baseline); err != nil {
		t.Fatal(err)
	}
	runPendingOp(t, d, store, op.ID, StatusFailed)
	saved, err := d.manifests.(*manifests.Store).Load(context.Background(), raw.APIVersion, raw.Kind, raw.Metadata.Name)
	if err != nil || saved == nil || saved.Spec["cores"] != float64(8) {
		t.Fatalf("queued create changed the completed baseline: %+v, %v", saved, err)
	}
	if p.applies.Load() != 0 {
		t.Fatal("queued create overwrote a completed deployment")
	}
}

func TestSecretApplyDoesNotPersistProviderEcho(t *testing.T) {
	t.Setenv("OPENCTL_TEST_PASSWORD", "must-not-land-in-operation-storage")
	p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}}
	store, d := newDispatcherWithManifests(t, p)
	reg := secrets.NewRegistry()
	secrets.RegisterBuiltins(reg, t.TempDir())
	d.SetSecrets(reg)
	raw := childManifest("fake.openctl.io/v1", "FakeKind", "x")
	raw.Spec = map[string]any{"password": map[string]any{"$secret": map[string]any{"env": "OPENCTL_TEST_PASSWORD"}}}
	op := submitIntent(t, store, raw, false)
	final := runPendingOp(t, d, store, op.ID, StatusSucceeded)
	if final.ResultJSON != "" {
		t.Fatal("provider secret echo was persisted as operation result")
	}
	var stored protocol.Resource
	if err := json.Unmarshal([]byte(final.ManifestJSON), &stored); err != nil {
		t.Fatal(err)
	}
	if containsValue(stored.Spec, "must-not-land-in-operation-storage") || !secrets.HasSecrets(stored.Spec) {
		t.Fatal("operation manifest did not retain secret markers safely")
	}
}

func TestTopLevelDeleteRechecksAdmissionWithoutBlockingOwnedChildDelete(t *testing.T) {
	p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}}
	sink := &recordingManifestSink{}
	d := newDispatcherWithSink(t, p, sink)
	calls := 0
	d.SetDeleteGuard(func(_ context.Context, version, kind, name string) error {
		calls++
		if version != "fake.openctl.io/v1" || kind != "FakeKind" || name != "x" {
			t.Fatalf("guard received wrong identity: %s/%s/%s", version, kind, name)
		}
		return errors.New("a dependent was added after submission")
	})
	op, err := d.store.Submit(context.Background(), &Operation{
		Type: TypeDelete, APIVersion: "fake.openctl.io/v1", Kind: "FakeKind", ResourceName: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	runPendingOp(t, d, d.store, op.ID, StatusFailed)
	if calls != 1 || p.deletes.Load() != 0 || len(sink.deleted) != 0 {
		t.Fatal("failed execution-time admission still mutated the resource")
	}
	if err := d.DeleteManifest(context.Background(), childManifest("fake.openctl.io/v1", "FakeKind", "x")); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || p.deletes.Load() != 1 || len(sink.deleted) != 1 {
		t.Fatal("top-level deletion guard prevented composite-owned child deletion")
	}
}
