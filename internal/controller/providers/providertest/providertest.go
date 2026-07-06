// Package providertest is a reusable conformance battery for
// providers.Provider implementations. The controller now hosts several kinds
// of provider — first-party compiled-in (proxmox, k3s), external plugins over
// pluginproto, and Terraform providers over tfhost — and the strategic
// direction is to widen that ecosystem further. Before it widens, every
// implementation must agree on the same baseline contract: what Apply
// returns, how a missing Get is signaled, that Delete is idempotent, and so
// on. This package encodes that contract once so each provider can assert it
// from its own tests instead of re-deriving it (or quietly diverging).
//
// Bind it by constructing a Suite with a factory that returns a fresh,
// isolated provider instance and a manifest builder for one of its kinds,
// then call Run:
//
//	func TestConformance(t *testing.T) {
//		providertest.Suite{
//			NewProvider: func(t *testing.T) (providers.Provider, func()) { ... },
//			Kind:        "Note",
//			Manifest:    func(name string) *protocol.Resource { ... },
//			Capabilities: providertest.Capabilities{SupportsList: true},
//		}.Run(t)
//	}
package providertest

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/pkg/protocol"
)

// Capabilities declares legitimate, provider-specific variations from the
// baseline contract, so the Suite asserts the right behavior for each
// implementation instead of collapsing to a lowest-common-denominator set
// that proves little.
type Capabilities struct {
	// SupportsList is true when the provider enumerates resources of a kind.
	// Some providers cannot (the Terraform host has no list API and returns
	// an error from List); those set this false and the Suite skips the
	// enumeration assertions rather than treating the error as a failure.
	SupportsList bool

	// NoOpOnExisting is true for atomic providers whose Apply on an existing
	// resource returns the observed state WITHOUT mutating it — the proxmox
	// VirtualMachine "no-op + surface drift" decision. When false (CRUD /
	// update providers, e.g. external plugins), the Suite only requires that
	// re-Apply succeeds and the resource still round-trips.
	NoOpOnExisting bool
}

// Suite is the conformance battery. Every field is required except
// Capabilities.
type Suite struct {
	// NewProvider returns a fresh provider plus a cleanup func, called once
	// per subtest so subtests never share mutable state. The cleanup func is
	// invoked via t.Cleanup.
	NewProvider func(t *testing.T) (providers.Provider, func())

	// Kind is the resource kind the Suite exercises. The provider must handle
	// it and Manifest must build it.
	Kind string

	// Manifest builds a valid Apply manifest for the given resource name. Two
	// calls with different names must describe independent resources.
	Manifest func(name string) *protocol.Resource

	Capabilities Capabilities
}

// testingT is the slice of *testing.T the assertions use. Splitting the
// assertions out behind this interface lets the package's own tests drive a
// deliberately-broken provider through a check and confirm the battery fails
// it — i.e. prove the suite has teeth, not just that it passes compliant
// providers.
type testingT interface {
	Helper()
	Error(args ...any)
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	Fatal(args ...any)
	Skip(args ...any)
}

// Run executes the full battery as a tree of subtests. Each subtest gets its
// own provider instance from NewProvider.
func (s Suite) Run(t *testing.T) {
	t.Helper()
	if s.NewProvider == nil || s.Manifest == nil || s.Kind == "" {
		t.Fatal("providertest.Suite requires NewProvider, Manifest, and Kind")
	}

	cases := []struct {
		name   string
		assert func(t testingT, p providers.Provider)
	}{
		{"Name/NonEmptyAndStable", s.assertNameStable},
		{"Kinds/IncludesSuiteKind", s.assertKindsIncludes},
		{"Apply/ReturnsMatchingIdentity", s.assertApplyIdentity},
		{"Get/RoundTripsAfterApply", s.assertGetRoundTrip},
		{"Get/MissingIsNotFound", s.assertGetMissing},
		{"Delete/IdempotentOnMissing", s.assertDeleteIdempotent},
		{"Delete/RemovesResource", s.assertDeleteRemoves},
		{"Apply/RepeatIsStable", s.assertApplyRepeat},
		{"List/IncludesApplied", s.assertList},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.assert(t, s.newProvider(t))
		})
	}
}

func (s Suite) newProvider(t *testing.T) providers.Provider {
	t.Helper()
	p, cleanup := s.NewProvider(t)
	if p == nil {
		t.Fatal("NewProvider returned a nil provider")
	}
	if cleanup != nil {
		t.Cleanup(cleanup)
	}
	return p
}

func (s Suite) assertNameStable(t testingT, p providers.Provider) {
	t.Helper()
	first, second := p.Name(), p.Name()
	if first == "" {
		t.Fatal("Name() is empty; must match the apiVersion prefix")
	}
	if first != second {
		t.Fatalf("Name() is not stable across calls: %q then %q", first, second)
	}
}

func (s Suite) assertKindsIncludes(t testingT, p providers.Provider) {
	t.Helper()
	kinds := p.Kinds()
	if len(kinds) == 0 {
		t.Fatal("Kinds() is empty; a provider must handle at least one kind")
	}
	if !slices.Contains(kinds, s.Kind) {
		t.Fatalf("Kinds() %v does not include the Suite kind %q", kinds, s.Kind)
	}
}

func (s Suite) assertApplyIdentity(t testingT, p providers.Provider) {
	t.Helper()
	got := mustApply(t, p, s.Manifest("identity"))
	if got.Kind != s.Kind {
		t.Errorf("Apply result Kind = %q, want %q", got.Kind, s.Kind)
	}
	if got.Metadata.Name != "identity" {
		t.Errorf("Apply result name = %q, want %q", got.Metadata.Name, "identity")
	}
	if got.APIVersion == "" {
		t.Error("Apply result APIVersion is empty; providers must stamp it")
	}
}

func (s Suite) assertGetRoundTrip(t testingT, p providers.Provider) {
	t.Helper()
	mustApply(t, p, s.Manifest("roundtrip"))

	got, err := p.Get(context.Background(), s.Kind, "roundtrip")
	if err != nil {
		t.Fatalf("Get after Apply: %v (want the applied resource)", err)
	}
	if got == nil {
		t.Fatal("Get after Apply returned nil resource")
		return // testingT.Fatal terminates for *testing.T; unreachable, but keeps staticcheck's nil analysis happy
	}
	if got.Metadata.Name != "roundtrip" || got.Kind != s.Kind {
		t.Errorf("Get returned %s/%s, want %s/%s", got.Kind, got.Metadata.Name, s.Kind, "roundtrip")
	}
}

func (s Suite) assertGetMissing(t testingT, p providers.Provider) {
	t.Helper()
	_, err := p.Get(context.Background(), s.Kind, "does-not-exist-conformance")
	if err == nil {
		t.Fatal("Get on a missing resource returned nil error; want a NotFoundError")
	}
	var nf *providers.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("Get on a missing resource returned %v (%T); want a *providers.NotFoundError so the server maps it to codes.NotFound", err, err)
	}
}

func (s Suite) assertDeleteIdempotent(t testingT, p providers.Provider) {
	t.Helper()
	if err := p.Delete(context.Background(), s.Kind, "never-created-conformance"); err != nil {
		t.Fatalf("Delete on a missing resource = %v; want nil (delete-on-missing is success)", err)
	}
}

func (s Suite) assertDeleteRemoves(t testingT, p providers.Provider) {
	t.Helper()
	mustApply(t, p, s.Manifest("deleteme"))

	if err := p.Delete(context.Background(), s.Kind, "deleteme"); err != nil {
		t.Fatalf("Delete of an existing resource = %v; want nil", err)
	}
	_, err := p.Get(context.Background(), s.Kind, "deleteme")
	var nf *providers.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("Get after Delete returned %v (%T); want *providers.NotFoundError (the resource is gone)", err, err)
	}
}

func (s Suite) assertApplyRepeat(t testingT, p providers.Provider) {
	t.Helper()
	first := mustApply(t, p, s.Manifest("repeat"))
	second := mustApply(t, p, s.Manifest("repeat"))

	// The resource must still round-trip after a second Apply, for every
	// provider — re-applying an unchanged manifest is never destructive.
	if _, err := p.Get(context.Background(), s.Kind, "repeat"); err != nil {
		t.Fatalf("Get after a repeated Apply: %v", err)
	}
	if s.Capabilities.NoOpOnExisting {
		// The stronger atomic guarantee: the second Apply observed the same
		// state it returned the first time, without mutating.
		if !reflect.DeepEqual(first.Spec, second.Spec) {
			t.Errorf("NoOpOnExisting provider mutated on re-Apply:\n first.Spec = %#v\nsecond.Spec = %#v", first.Spec, second.Spec)
		}
	}
}

func (s Suite) assertList(t testingT, p providers.Provider) {
	t.Helper()
	if !s.Capabilities.SupportsList {
		t.Skip("provider does not support List (Capabilities.SupportsList is false)")
	}
	mustApply(t, p, s.Manifest("list-a"))
	mustApply(t, p, s.Manifest("list-b"))

	got, err := p.List(context.Background(), s.Kind)
	if err != nil {
		t.Fatalf("List after applying two resources: %v", err)
	}
	names := map[string]bool{}
	for _, r := range got {
		if r.Kind != s.Kind {
			t.Errorf("List returned a %q among %q resources", r.Kind, s.Kind)
		}
		names[r.Metadata.Name] = true
	}
	for _, want := range []string{"list-a", "list-b"} {
		if !names[want] {
			t.Errorf("List did not include applied resource %q (got %v)", want, keys(names))
		}
	}
}

func mustApply(t testingT, p providers.Provider, m *protocol.Resource) *protocol.Resource {
	t.Helper()
	got, err := p.Apply(context.Background(), m)
	if err != nil {
		t.Fatalf("Apply(%s/%s): %v", m.Kind, m.Metadata.Name, err)
	}
	if got == nil {
		t.Fatalf("Apply(%s/%s) returned a nil resource with no error", m.Kind, m.Metadata.Name)
	}
	return got
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
