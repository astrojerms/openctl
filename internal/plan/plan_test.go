package plan

import (
	"strings"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

// res builds a resource whose spec $refs each of the given targets ("Kind/Name"
// with a fixed apiVersion "v1" unless the target carries its own "av|Kind|Name").
func res(apiVersion, kind, name string, refTargets ...refTarget) *protocol.Resource {
	spec := map[string]any{"field": "value"}
	for i, rt := range refTargets {
		spec["dep"+string(rune('a'+i))] = map[string]any{
			"$ref": map[string]any{
				"apiVersion": rt.apiVersion,
				"kind":       rt.kind,
				"name":       rt.name,
				"field":      "status.x",
			},
		}
	}
	return &protocol.Resource{
		APIVersion: apiVersion,
		Kind:       kind,
		Metadata:   protocol.ResourceMetadata{Name: name},
		Spec:       spec,
	}
}

type refTarget struct{ apiVersion, kind, name string }

func ref(apiVersion, kind, name string) refTarget {
	return refTarget{apiVersion, kind, name}
}

// waveDisplays flattens a plan's waves into a slice of display-label slices.
func waveDisplays(p *Plan) [][]string {
	out := make([][]string, len(p.Waves))
	for i, w := range p.Waves {
		for _, n := range w {
			out[i] = append(out[i], n.Display())
		}
	}
	return out
}

func TestBuild_Chain(t *testing.T) {
	// C → B → A: waves must be [[A],[B],[C]].
	a := res("v1", "A", "a")
	b := res("v1", "B", "b", ref("v1", "A", "a"))
	c := res("v1", "C", "c", ref("v1", "B", "b"))

	p, err := Build([]*protocol.Resource{c, b, a}) // input order shouldn't matter
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := waveDisplays(p)
	want := [][]string{{"A/a"}, {"B/b"}, {"C/c"}}
	if !equalWaves(got, want) {
		t.Fatalf("waves = %v, want %v", got, want)
	}
	// B records its dependency label.
	if len(p.Waves[1]) != 1 || len(p.Waves[1][0].Deps) != 1 || p.Waves[1][0].Deps[0] != "A/a" {
		t.Errorf("B deps = %v, want [A/a]", p.Waves[1][0].Deps)
	}
}

func TestBuild_Diamond(t *testing.T) {
	// D depends on B and C; both depend on A → [[A],[B,C],[D]].
	a := res("v1", "A", "a")
	b := res("v1", "B", "b", ref("v1", "A", "a"))
	c := res("v1", "C", "c", ref("v1", "A", "a"))
	d := res("v1", "D", "d", ref("v1", "B", "b"), ref("v1", "C", "c"))

	p, err := Build([]*protocol.Resource{a, b, c, d})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := waveDisplays(p)
	want := [][]string{{"A/a"}, {"B/b", "C/c"}, {"D/d"}} // wave 2 sorted by label
	if !equalWaves(got, want) {
		t.Fatalf("waves = %v, want %v", got, want)
	}
	if d := p.Waves[2][0].Deps; len(d) != 2 || d[0] != "B/b" || d[1] != "C/c" {
		t.Errorf("D deps = %v, want [B/b C/c]", d)
	}
}

func TestBuild_ExternalRef(t *testing.T) {
	// A $refs a resource not in the set → recorded as external, no wave dep.
	a := res("v1", "A", "a", ref("k3s.openctl.io/v1", "Cluster", "home"))
	p, err := Build([]*protocol.Resource{a})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(p.Waves) != 1 || len(p.Waves[0]) != 1 {
		t.Fatalf("want a single node in one wave, got %v", waveDisplays(p))
	}
	n := p.Waves[0][0]
	if len(n.Deps) != 0 {
		t.Errorf("external ref should not be an in-set dep, got %v", n.Deps)
	}
	if len(n.External) != 1 || n.External[0] != "Cluster/home" {
		t.Errorf("external = %v, want [Cluster/home]", n.External)
	}
}

func TestBuild_Cycle(t *testing.T) {
	a := res("v1", "A", "a", ref("v1", "B", "b"))
	b := res("v1", "B", "b", ref("v1", "A", "a"))
	_, err := Build([]*protocol.Resource{a, b})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("want a cycle error, got %v", err)
	}
	if !strings.Contains(err.Error(), "A/a") || !strings.Contains(err.Error(), "B/b") {
		t.Errorf("cycle error should name both nodes, got %v", err)
	}
}

func TestBuild_DuplicateIdentity(t *testing.T) {
	a1 := res("v1", "A", "a")
	a2 := res("v1", "A", "a")
	if _, err := Build([]*protocol.Resource{a1, a2}); err == nil {
		t.Fatal("want a duplicate-identity error")
	}
}

func TestBuild_SameKindNameDifferentAPIVersion(t *testing.T) {
	// Two resources sharing Kind+Name but distinct apiVersion are NOT duplicates
	// and don't collide (full-triple key).
	a := res("prov-a/v1", "Thing", "x")
	b := res("prov-b/v1", "Thing", "x")
	p, err := Build([]*protocol.Resource{a, b})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Count() != 2 {
		t.Errorf("count = %d, want 2", p.Count())
	}
}

func TestBuild_NoRefsAllOneWave(t *testing.T) {
	p, err := Build([]*protocol.Resource{
		res("v1", "A", "a"), res("v1", "B", "b"), res("v1", "C", "c"),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(p.Waves) != 1 || len(p.Waves[0]) != 3 {
		t.Fatalf("independent resources should share one wave, got %v", waveDisplays(p))
	}
}

func equalWaves(a, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return false
			}
		}
	}
	return true
}
