package reconciler

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/pkg/protocol"
)

type readFailureProvider struct {
	*stubProvider
	err error
}

func (p *readFailureProvider) Get(context.Context, string, string) (*protocol.Resource, error) {
	return nil, p.err
}

func TestMissingResourcesRemediateOnlyConfirmedOptIn(t *testing.T) {
	for _, tc := range []struct {
		name       string
		annotation string
		getErr     error
		wantApply  bool
		wantDrift  bool
	}{
		{"opted in missing", "true", providers.NotFound("FakeKind", "x"), true, true},
		{"wrapped absence", "true", fmt.Errorf("lookup: %w", providers.NotFound("FakeKind", "x")), true, true},
		{"default missing", "", providers.NotFound("FakeKind", "x"), false, true},
		{"explicit opt out", "false", providers.NotFound("FakeKind", "x"), false, true},
		{"read outage", "true", errors.New("provider unavailable"), false, false},
		{"ambiguous empty response", "true", nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := newTestStore(t)
			desired := &protocol.Resource{
				APIVersion: "fake.openctl.io/v1", Kind: "FakeKind",
				Metadata: protocol.ResourceMetadata{Name: "x", Annotations: map[string]string{AutoReconcileAnnotation: tc.annotation}},
				Spec:     map[string]any{"cpus": 2.0},
			}
			if err := store.Save(ctx, desired); err != nil {
				t.Fatal(err)
			}
			reg := providers.NewRegistry()
			reg.Register(&readFailureProvider{stubProvider: newStubProvider(), err: tc.getErr})
			var applied *protocol.Resource
			rec := New(reg, store, 0).WithAutoApply(func(_ context.Context, raw *protocol.Resource) error {
				applied = raw
				return nil
			})
			rec.ReconcileOnce(ctx)
			if (applied != nil) != tc.wantApply {
				t.Fatalf("remediation = %+v, want apply %v", applied, tc.wantApply)
			}
			if applied != nil && (applied.Metadata.Name != "x" || applied.Spec["cpus"] != 2.0) {
				t.Fatalf("remediation lost stored intent: %+v", applied)
			}
			if got := rec.driftState["fake.openctl.io/v1/FakeKind/x"]; got != tc.wantDrift {
				t.Fatalf("drift = %v, want %v", got, tc.wantDrift)
			}
		})
	}
}

func TestMissingResourceRemediationUsesFailureBackoff(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := store.Save(ctx, &protocol.Resource{
		APIVersion: "fake.openctl.io/v1", Kind: "FakeKind",
		Metadata: protocol.ResourceMetadata{Name: "x", Annotations: map[string]string{AutoReconcileAnnotation: "true"}},
		Spec:     map[string]any{"cpus": 2.0},
	}); err != nil {
		t.Fatal(err)
	}
	prov := newStubProvider()
	prov.setMissing("FakeKind", "x")
	reg := providers.NewRegistry()
	reg.Register(prov)
	calls := 0
	rec := New(reg, store, 0).WithAutoApply(func(context.Context, *protocol.Resource) error {
		calls++
		return errors.New("cannot enqueue")
	})
	rec.ReconcileOnce(ctx)
	rec.ReconcileOnce(ctx)
	if calls != 1 {
		t.Fatalf("missing-resource remediation ignored backoff: %d attempts", calls)
	}
}

type projectedReconcileProvider struct {
	*stubProvider
	unobservable bool
}

func (p *projectedReconcileProvider) DriftSpecs(_ string, desired, observed map[string]any) (map[string]any, map[string]any) {
	if p.unobservable {
		return nil, nil
	}
	return map[string]any{"cpus": desired["cpus"]}, observed
}

type observedOnlyReconcileProvider struct{ *stubProvider }

func (p *observedOnlyReconcileProvider) ObservedOnlyKinds() []string { return []string{"FakeKind"} }

func TestReconcilerDoesNotClaimUnobservableConvergence(t *testing.T) {
	for _, kind := range []string{"empty projection", "observed only", "secret marker"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			store := newTestStore(t)
			spec := map[string]any{"cpus": 2.0}
			if kind == "secret marker" {
				spec["password"] = map[string]any{"$secret": map[string]any{"env": "PASSWORD"}}
			}
			if err := store.Save(ctx, &protocol.Resource{
				APIVersion: "fake.openctl.io/v1", Kind: "FakeKind",
				Metadata: protocol.ResourceMetadata{Name: "x", Annotations: map[string]string{AutoReconcileAnnotation: "true"}},
				Spec:     spec,
			}); err != nil {
				t.Fatal(err)
			}
			prov := newStubProvider()
			prov.set("FakeKind", "x", &protocol.Resource{Spec: spec})
			reg := providers.NewRegistry()
			switch kind {
			case "empty projection":
				reg.Register(&projectedReconcileProvider{stubProvider: prov, unobservable: true})
			case "observed only":
				reg.Register(&observedOnlyReconcileProvider{prov})
			default:
				reg.Register(prov)
			}
			calls := 0
			rec := New(reg, store, 0).WithAutoApply(func(context.Context, *protocol.Resource) error {
				calls++
				return nil
			})
			key := "fake.openctl.io/v1/FakeKind/x"
			rec.driftState[key] = true
			rec.ReconcileOnce(ctx)
			if !rec.driftState[key] || calls != 0 {
				t.Fatalf("unobservable resource asserted clean or remediated: drift=%v, applies=%d", rec.driftState[key], calls)
			}
		})
	}
}

func TestReconcilerProjectsLiveSpecWithoutFillingMissingFields(t *testing.T) {
	for _, live := range []map[string]any{{"cpus": 2, "providerDefault": true}, {}, nil} {
		t.Run(fmt.Sprintf("live=%v", live), func(t *testing.T) {
			ctx := context.Background()
			store := newTestStore(t)
			if err := store.Save(ctx, &protocol.Resource{
				APIVersion: "fake.openctl.io/v1", Kind: "FakeKind",
				Metadata: protocol.ResourceMetadata{Name: "x", Annotations: map[string]string{AutoReconcileAnnotation: "true"}},
				Spec:     map[string]any{"cpus": 2.0, "creationOnly": "not observable"},
			}); err != nil {
				t.Fatal(err)
			}
			prov := newStubProvider()
			prov.set("FakeKind", "x", &protocol.Resource{Spec: live})
			reg := providers.NewRegistry()
			reg.Register(&projectedReconcileProvider{stubProvider: prov})
			calls := 0
			rec := New(reg, store, 0).WithAutoApply(func(context.Context, *protocol.Resource) error {
				calls++
				return nil
			})
			rec.ReconcileOnce(ctx)
			_, hasCPUs := live["cpus"]
			if got := rec.driftState["fake.openctl.io/v1/FakeKind/x"]; got != !hasCPUs {
				t.Fatalf("drift = %v for live %v", got, live)
			}
			if (calls == 1) != !hasCPUs {
				t.Fatalf("remediation calls = %d for live %v", calls, live)
			}
		})
	}
}

func TestReconcilerResolvesReferencesBeforeComparingLiveFields(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	marker := map[string]any{"$ref": map[string]any{
		"apiVersion": "fake.openctl.io/v1", "kind": "FakeKind", "name": "target", "field": "status.ip",
	}}
	if err := store.Save(ctx, &protocol.Resource{
		APIVersion: "fake.openctl.io/v1", Kind: "FakeKind",
		Metadata: protocol.ResourceMetadata{Name: "x", Annotations: map[string]string{AutoReconcileAnnotation: "true"}},
		Spec:     map[string]any{"address": marker},
	}); err != nil {
		t.Fatal(err)
	}
	prov := newStubProvider()
	prov.set("FakeKind", "target", &protocol.Resource{Status: map[string]any{"ip": "10.0.0.1"}})
	prov.set("FakeKind", "x", &protocol.Resource{Spec: map[string]any{"address": "10.0.0.1"}})
	reg := providers.NewRegistry()
	reg.Register(prov)
	calls := 0
	rec := New(reg, store, 0).WithAutoApply(func(context.Context, *protocol.Resource) error {
		calls++
		return nil
	})
	rec.ReconcileOnce(ctx)
	if calls != 0 || rec.driftState["fake.openctl.io/v1/FakeKind/x"] {
		t.Fatal("matching resolved reference reported as live drift")
	}
	prov.set("FakeKind", "target", &protocol.Resource{Status: map[string]any{"ip": "10.0.0.2"}})
	rec.ReconcileOnce(ctx)
	if calls != 1 || !rec.driftState["fake.openctl.io/v1/FakeKind/x"] {
		t.Fatal("changed reference target failed to trigger drift remediation")
	}
}

func TestSpecsEqualPreservesDeclaredFieldSemantics(t *testing.T) {
	for _, tc := range []struct {
		name     string
		desired  any
		observed any
		want     bool
	}{
		{"number normalization", int64(2), float64(2), true},
		{"string is not a number", 2, "2", false},
		{"string is not a boolean", true, "true", false},
		{"string is not null", nil, "<nil>", false},
		{"large integers remain distinct", int64(1<<53 + 1), int64(1 << 53), false},
		{"rounded float is not large integer", int64(1<<53 + 1), float64(1 << 53), false},
		{"signed unsigned exact", int64(2), uint64(2), true},
		{"signed unsigned negative", int64(-1), uint64(math.MaxUint64), false},
		{"float exceeds int64", int64(math.MaxInt64), float64(0x1p63), false},
		{"float exceeds uint64", uint64(math.MaxUint64), float64(0x1p64), false},
		{"array values", []any{2, map[string]any{"enabled": true}}, []any{2.0, map[string]any{"enabled": true, "default": "ok"}}, true},
		{"array length", []any{2}, []any{2.0, 3.0}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SpecsEqual(map[string]any{"value": tc.desired}, map[string]any{"value": tc.observed}); got != tc.want {
				t.Fatalf("equal(%#v, %#v) = %v, want %v", tc.desired, tc.observed, got, tc.want)
			}
		})
	}
	if SpecsEqual(map[string]any{"required": nil}, map[string]any{}) {
		t.Fatal("missing declared null field incorrectly treated as present")
	}
}
