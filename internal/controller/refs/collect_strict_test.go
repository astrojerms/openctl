package refs

import (
	"reflect"
	"testing"
)

func TestCollectStrictRetainsStableNestedDependencyEdges(t *testing.T) {
	spec := map[string]any{
		"nested": []any{
			map[string]any{"$ref": map[string]any{"apiVersion": "p.openctl.io/v1", "kind": "VM", "name": "z", "field": "status.ip"}},
			map[string]any{"child": map[string]any{"$ref": map[string]any{"apiVersion": "p.openctl.io/v1", "kind": "VM", "name": "a"}}},
		},
		"ordinary": "value",
	}
	got, err := CollectStrict(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []Ref{
		{APIVersion: "p.openctl.io/v1", Kind: "VM", Name: "a"},
		{APIVersion: "p.openctl.io/v1", Kind: "VM", Name: "z", Field: "status.ip"},
	}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(Collect(spec), want) {
		t.Fatalf("collectors lost stable dependency edges: strict=%+v, best-effort=%+v", got, Collect(spec))
	}
	if got, err := CollectStrict(nil); err != nil || got != nil {
		t.Fatalf("nil spec = %+v, %v", got, err)
	}
}

func TestCollectStrictRejectsMalformedMarkersWithoutChangingBestEffortCollect(t *testing.T) {
	valid := map[string]any{"$ref": map[string]any{"apiVersion": "p.openctl.io/v1", "kind": "VM", "name": "valid"}}
	for _, malformed := range []any{
		"invalid",
		nil,
		map[string]any{"kind": "VM", "name": "missing-api-version"},
		map[string]any{"apiVersion": "p.openctl.io/v1", "kind": 42, "name": "wrong-kind-type"},
		map[string]any{"apiVersion": "p.openctl.io/v1", "kind": "VM", "name": "wrong-field-type", "field": 42},
		map[string]any{"apiVersion": "p.openctl.io/v1", "kind": "VM", "name": ""},
	} {
		spec := map[string]any{"nested": []any{valid, map[string]any{"$ref": malformed}}}
		if got, err := CollectStrict(spec); err == nil || got != nil {
			t.Fatalf("malformed dependency returned partial success: %+v, %v", got, err)
		}
		if got := Collect(spec); len(got) != 1 || got[0].Name != "valid" {
			t.Fatalf("best-effort graph collection changed for malformed markers: %+v", got)
		}
	}
}
