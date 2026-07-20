package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl/internal/plan"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestLoadManifestBatch_DirAndMultiDoc loads a directory recursively, splitting
// multi-doc YAML and dispatching by extension.
func TestLoadManifestBatch_DirAndMultiDoc(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.yaml"),
		"apiVersion: v1\nkind: A\nmetadata:\n  name: a\n---\napiVersion: v1\nkind: B\nmetadata:\n  name: b\n")
	writeFile(t, filepath.Join(dir, "sub", "c.yml"),
		"apiVersion: v1\nkind: C\nmetadata:\n  name: c\n")
	// A non-manifest file must be ignored by the dir walk.
	writeFile(t, filepath.Join(dir, "notes.txt"), "ignore me")

	rs, err := loadManifestBatch([]string{dir})
	if err != nil {
		t.Fatalf("loadManifestBatch: %v", err)
	}
	if len(rs) != 3 {
		t.Fatalf("loaded %d resources, want 3 (A,B,C)", len(rs))
	}
	// The loaded set feeds Build without error.
	if _, err := plan.Build(rs); err != nil {
		t.Fatalf("Build: %v", err)
	}
}

func TestLoadManifestBatch_MissingPath(t *testing.T) {
	if _, err := loadManifestBatch([]string{filepath.Join(t.TempDir(), "nope.yaml")}); err == nil {
		t.Fatal("want an error for a missing path")
	}
}

func TestLoadManifestFile_UnsupportedExt(t *testing.T) {
	f := filepath.Join(t.TempDir(), "x.json")
	writeFile(t, f, "{}")
	if _, err := loadManifestFile(f); err == nil {
		t.Fatal("want an error for an unsupported extension")
	}
}

// TestPrintPlan_Output checks the rendered plan lists waves, dependencies, and
// external references.
func TestPrintPlan_Output(t *testing.T) {
	writeDir := t.TempDir()
	// B $refs A (in set) and an external Cluster/home (not in set).
	writeFile(t, filepath.Join(writeDir, "m.yaml"),
		"apiVersion: v1\nkind: A\nmetadata:\n  name: a\n"+
			"---\n"+
			"apiVersion: v1\nkind: B\nmetadata:\n  name: b\nspec:\n"+
			"  dep:\n    $ref:\n      apiVersion: v1\n      kind: A\n      name: a\n      field: status.x\n"+
			"  ext:\n    $ref:\n      apiVersion: k3s.openctl.io/v1\n      kind: Cluster\n      name: home\n      field: status.x\n")

	rs, err := loadManifestBatch([]string{writeDir})
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.Build(rs)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	printPlan(&buf, p)
	out := buf.String()
	for _, want := range []string{"wave 1:", "A/a", "wave 2:", "B/b  ← A/a", "External references", "Cluster/home"} {
		if !bytes.Contains(buf.Bytes(), []byte(want)) {
			t.Errorf("plan output missing %q:\n%s", want, out)
		}
	}
}
