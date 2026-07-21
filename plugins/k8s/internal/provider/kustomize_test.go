package provider

import (
	"strings"
	"testing"
)

func TestRenderKustomization_TransformsAndBuilds(t *testing.T) {
	files := map[string]string{
		"kustomization.yaml": "" +
			"namespace: demo\n" +
			"namePrefix: prod-\n" +
			"commonLabels:\n  team: platform\n" +
			"resources:\n  - cm.yaml\n",
		"cm.yaml": "" +
			"apiVersion: v1\n" +
			"kind: ConfigMap\n" +
			"metadata:\n  name: settings\n" +
			"data:\n  key: value\n",
	}
	out, err := renderKustomization(files, ".")
	if err != nil {
		t.Fatalf("renderKustomization: %v", err)
	}
	// The kustomize transforms must have been applied.
	for _, want := range []string{"kind: ConfigMap", "name: prod-settings", "namespace: demo", "team: platform"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered output missing %q:\n%s", want, out)
		}
	}
	// The rendered YAML must parse back into objects (feeds the Manifest apply core).
	objs, err := parseObjects(out)
	if err != nil {
		t.Fatalf("parse rendered objects: %v", err)
	}
	if len(objs) != 1 || objs[0].GetName() != "prod-settings" {
		t.Fatalf("expected one ConfigMap named prod-settings, got %d: %v", len(objs), objs)
	}
}

func TestRenderKustomization_BuildDirSubfolder(t *testing.T) {
	// kustomization.yaml lives under overlays/prod; build that dir.
	files := map[string]string{
		"base/cm.yaml":                     "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: base\n",
		"base/kustomization.yaml":          "resources:\n  - cm.yaml\n",
		"overlays/prod/kustomization.yaml": "namePrefix: prod-\nresources:\n  - ../../base\n",
	}
	out, err := renderKustomization(files, "overlays/prod")
	if err != nil {
		t.Fatalf("renderKustomization: %v", err)
	}
	if !strings.Contains(out, "name: prod-base") {
		t.Errorf("overlay build did not apply namePrefix:\n%s", out)
	}
}

func TestRenderKustomization_InvalidErrors(t *testing.T) {
	// A kustomization referencing a missing resource must error, not silently
	// produce nothing.
	_, err := renderKustomization(map[string]string{
		"kustomization.yaml": "resources:\n  - nope.yaml\n",
	}, ".")
	if err == nil {
		t.Fatal("expected an error building a kustomization with a missing resource")
	}
}

func TestKustomizeFiles(t *testing.T) {
	// Missing files → error.
	if _, err := kustomizeFiles(map[string]any{}); err == nil {
		t.Error("want error when spec.files is absent")
	}
	// Non-string value → error.
	if _, err := kustomizeFiles(map[string]any{"files": map[string]any{"a": 5}}); err == nil {
		t.Error("want error when a file value is not a string")
	}
	// Happy path.
	got, err := kustomizeFiles(map[string]any{"files": map[string]any{
		"kustomization.yaml": "resources: []\n",
	}})
	if err != nil {
		t.Fatalf("kustomizeFiles: %v", err)
	}
	if got["kustomization.yaml"] != "resources: []\n" {
		t.Errorf("files not extracted: %v", got)
	}
}
