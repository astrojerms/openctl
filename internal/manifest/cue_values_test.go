package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// A manifest with an abstract field is completed by a values file via CUE
// unification.
func TestLoadCUEWithValues_FillsAbstractField(t *testing.T) {
	dir := t.TempDir()
	manifest := writeFile(t, dir, "vm.cue", `
apiVersion: "proxmox.openctl.io/v1"
kind:       "VirtualMachine"
metadata: name: "db-01"
spec: {
	node: string
	cpu: cores: int
	memory: size: int | *2048
}
`)
	values := writeFile(t, dir, "prod.cue", `
spec: {
	node: "pve1"
	cpu: cores: 8
}
`)

	res, err := LoadCUEWithValues(manifest, []string{values})
	if err != nil {
		t.Fatalf("LoadCUEWithValues: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("got %d resources, want 1", len(res))
	}
	spec := res[0].Spec
	if spec["node"] != "pve1" {
		t.Errorf("node = %v, want pve1", spec["node"])
	}
	cpu, _ := spec["cpu"].(map[string]any)
	if cpu["cores"] != float64(8) {
		t.Errorf("cpu.cores = %v, want 8", cpu["cores"])
	}
	// The default (2048) survives when the values file doesn't override it.
	mem, _ := spec["memory"].(map[string]any)
	if mem["size"] != float64(2048) {
		t.Errorf("memory.size = %v, want default 2048", mem["size"])
	}
}

// A values file overrides a manifest default.
func TestLoadCUEWithValues_OverridesDefault(t *testing.T) {
	dir := t.TempDir()
	manifest := writeFile(t, dir, "vm.cue", `
apiVersion: "proxmox.openctl.io/v1"
kind:       "VirtualMachine"
metadata: name: "db-01"
spec: {
	node: "pve1"
	memory: size: int | *2048
}
`)
	values := writeFile(t, dir, "big.cue", `spec: memory: size: 16384`)

	res, err := LoadCUEWithValues(manifest, []string{values})
	if err != nil {
		t.Fatalf("LoadCUEWithValues: %v", err)
	}
	mem, _ := res[0].Spec["memory"].(map[string]any)
	if mem["size"] != float64(16384) {
		t.Errorf("memory.size = %v, want overridden 16384", mem["size"])
	}
}

// An abstract field left unfilled fails the concreteness check (loud, not a
// silent zero).
func TestLoadCUEWithValues_MissingRequiredIsError(t *testing.T) {
	dir := t.TempDir()
	manifest := writeFile(t, dir, "vm.cue", `
apiVersion: "proxmox.openctl.io/v1"
kind:       "VirtualMachine"
metadata: name: "db-01"
spec: node: string
`)
	// No values file → spec.node stays abstract → not concrete.
	if _, err := LoadCUEWithValues(manifest, nil); err == nil {
		t.Fatal("expected a concreteness error for an unfilled abstract field")
	}
}

// A conflicting values file (two different concrete values) is an error, not
// silent last-writer-wins.
func TestLoadCUEWithValues_ConflictErrors(t *testing.T) {
	dir := t.TempDir()
	manifest := writeFile(t, dir, "vm.cue", `
apiVersion: "proxmox.openctl.io/v1"
kind:       "VirtualMachine"
metadata: name: "db-01"
spec: node: "pve1"
`)
	values := writeFile(t, dir, "bad.cue", `spec: node: "pve2"`)
	if _, err := LoadCUEWithValues(manifest, []string{values}); err == nil {
		t.Fatal("expected a conflict error unifying node pve1 with pve2")
	}
}
