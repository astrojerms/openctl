package provider

import "testing"

func TestParseObjects(t *testing.T) {
	// Multi-document YAML → two objects; empty docs skipped.
	y := `
apiVersion: v1
kind: Namespace
metadata:
  name: demo
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: cfg
  namespace: demo
data:
  k: v
---
`
	objs, err := parseObjects(y)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("got %d objects, want 2", len(objs))
	}
	if objs[0].GetKind() != "Namespace" || objs[1].GetKind() != "ConfigMap" {
		t.Errorf("kinds = %s, %s", objs[0].GetKind(), objs[1].GetKind())
	}
	if objs[1].GetNamespace() != "demo" {
		t.Errorf("cm namespace = %q", objs[1].GetNamespace())
	}

	if _, err := parseObjects("\n---\n"); err == nil {
		t.Error("empty manifest should error")
	}
}

func TestPrunedRefs(t *testing.T) {
	cm := func(ns, name string) objectRef {
		return objectRef{Version: "v1", Resource: "configmaps", Kind: "ConfigMap", Namespace: ns, Name: name}
	}
	prior := []objectRef{cm("demo", "a"), cm("demo", "b"), cm("demo", "c")}
	current := []objectRef{cm("demo", "a"), cm("demo", "c")}

	pruned := prunedRefs(prior, current)
	if len(pruned) != 1 || pruned[0].Name != "b" {
		t.Fatalf("pruned = %+v, want just b", pruned)
	}
	// Nothing to prune when current ⊇ prior.
	if got := prunedRefs(current, prior); len(got) != 0 {
		t.Errorf("expected no prune when current grows, got %+v", got)
	}
	// Distinct namespaces are distinct objects.
	if got := prunedRefs([]objectRef{cm("a", "x")}, []objectRef{cm("b", "x")}); len(got) != 1 {
		t.Errorf("same name different namespace should prune, got %+v", got)
	}
}
