package templates

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTemplate drops a .cue file into dir and returns nothing; fatal on error.
func writeTemplate(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const validVMTemplate = `
template: {
	name:        "test-vm"
	displayName: "Test VM"
	description: "A test VM starter"
	apiVersion:  "proxmox.openctl.io/v1"
	kind:        "VirtualMachine"
	parameters: [
		{name: "hostname", type: "string", description: "VM hostname", required: true},
		{name: "cores", type: "int", description: "vCPUs", default: 2},
	]
}
params: {...}
resource: {
	apiVersion: "proxmox.openctl.io/v1"
	kind:       "VirtualMachine"
	metadata: name: params.hostname
	spec: cpu: cores: params.cores
}
`

func TestLoadFromDir_MissingDirIsNotAnError(t *testing.T) {
	ts, err := LoadFromDir(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing dir should not error, got: %v", err)
	}
	if ts != nil {
		t.Errorf("missing dir should yield nil templates, got %v", ts)
	}
}

func TestLoadFromDir_ParsesMetadata(t *testing.T) {
	dir := t.TempDir()
	writeTemplate(t, dir, "vm.cue", validVMTemplate)

	ts, err := LoadFromDir(dir)
	if err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	if len(ts) != 1 {
		t.Fatalf("expected 1 template, got %d", len(ts))
	}
	tm := ts[0]
	if tm.Name != "test-vm" || tm.DisplayName != "Test VM" {
		t.Errorf("metadata mismatch: name=%q display=%q", tm.Name, tm.DisplayName)
	}
	if tm.APIVersion != "proxmox.openctl.io/v1" || tm.Kind != "VirtualMachine" {
		t.Errorf("apiVersion/kind mismatch: %q %q", tm.APIVersion, tm.Kind)
	}
	if len(tm.Parameters) != 2 {
		t.Fatalf("expected 2 params, got %d", len(tm.Parameters))
	}
	if tm.Parameters[0].Name != "hostname" || !tm.Parameters[0].Required {
		t.Errorf("param[0] mismatch: %+v", tm.Parameters[0])
	}
	if tm.Parameters[1].Name != "cores" || tm.Parameters[1].Type != "int" {
		t.Errorf("param[1] mismatch: %+v", tm.Parameters[1])
	}
}

func TestLoadFromDir_RenderFillsParamsAndDefaults(t *testing.T) {
	dir := t.TempDir()
	writeTemplate(t, dir, "vm.cue", validVMTemplate)
	reg := Default().With(mustLoad(t, dir)...)

	// hostname provided, cores omitted → default 2.
	res, err := reg.Render("test-vm", map[string]any{"hostname": "web-1"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if res.APIVersion != "proxmox.openctl.io/v1" || res.Kind != "VirtualMachine" {
		t.Errorf("rendered apiVersion/kind wrong: %q %q", res.APIVersion, res.Kind)
	}
	if res.Metadata.Name != "web-1" {
		t.Errorf("metadata.name = %q, want web-1", res.Metadata.Name)
	}
	cpu, _ := res.Spec["cpu"].(map[string]any)
	if cpu == nil || toInt(cpu["cores"]) != 2 {
		t.Errorf("default cores not applied: spec.cpu=%v", res.Spec["cpu"])
	}

	// cores overridden.
	res2, err := reg.Render("test-vm", map[string]any{"hostname": "web-2", "cores": float64(4)})
	if err != nil {
		t.Fatalf("render override: %v", err)
	}
	cpu2, _ := res2.Spec["cpu"].(map[string]any)
	if cpu2 == nil || toInt(cpu2["cores"]) != 4 {
		t.Errorf("override cores not applied: spec.cpu=%v", res2.Spec["cpu"])
	}
}

func TestLoadFromDir_MissingRequiredParamErrors(t *testing.T) {
	dir := t.TempDir()
	writeTemplate(t, dir, "vm.cue", validVMTemplate)
	reg := Default().With(mustLoad(t, dir)...)

	if _, err := reg.Render("test-vm", map[string]any{}); err == nil {
		t.Fatal("expected error for missing required 'hostname'")
	}
}

func TestLoadFromDir_MalformedTemplateSkipped(t *testing.T) {
	dir := t.TempDir()
	writeTemplate(t, dir, "good.cue", validVMTemplate)
	// Missing `template:` block entirely.
	writeTemplate(t, dir, "bad.cue", `resource: {apiVersion: "x", kind: "Y", metadata: name: "z"}`)
	// Not concrete metadata (name is a type, not a value).
	writeTemplate(t, dir, "bad2.cue", `template: {name: string, kind: "K"}`+"\nresource: {}\n")

	ts, err := LoadFromDir(dir)
	if err != nil {
		t.Fatalf("LoadFromDir should not fail on bad files: %v", err)
	}
	if len(ts) != 1 || ts[0].Name != "test-vm" {
		t.Errorf("expected only the good template, got %d: %v", len(ts), names(ts))
	}
}

func TestWith_UserTemplateOverridesBuiltin(t *testing.T) {
	base := Default()
	override := &Template{
		Name:        "ubuntu-server-vm", // same name as a built-in
		DisplayName: "Custom Ubuntu",
		Render:      base.Get("ubuntu-server-vm").Render,
	}
	merged := base.With(override)
	got := merged.Get("ubuntu-server-vm")
	if got == nil || got.DisplayName != "Custom Ubuntu" {
		t.Errorf("user template should override built-in, got %+v", got)
	}
	// Count unchanged — override replaces, doesn't add a duplicate.
	if len(merged.All()) != len(base.All()) {
		t.Errorf("override changed count: %d vs %d", len(merged.All()), len(base.All()))
	}
}

func TestWith_AddsNewTemplate(t *testing.T) {
	base := Default()
	merged := base.With(&Template{Name: "z-extra", DisplayName: "ZZ Extra"})
	if merged.Get("z-extra") == nil {
		t.Fatal("new template not added")
	}
	if len(merged.All()) != len(base.All())+1 {
		t.Errorf("expected count+1, got %d", len(merged.All()))
	}
	// Base registry untouched.
	if base.Get("z-extra") != nil {
		t.Error("With mutated the receiver")
	}
}

// TestExampleTemplateIsValid guards the shipped examples/user-template.cue
// reference against rot — it must load and render with its required params.
func TestExampleTemplateIsValid(t *testing.T) {
	tm, err := loadCUETemplate(filepath.Join("..", "..", "examples", "user-template.cue"))
	if err != nil {
		t.Fatalf("examples/user-template.cue failed to load: %v", err)
	}
	if tm.Name != "dev-vm" {
		t.Errorf("example name = %q, want dev-vm", tm.Name)
	}
	res, err := tm.Render(map[string]any{"hostname": "dev-1", "node": "pve1"})
	if err != nil {
		t.Fatalf("example render: %v", err)
	}
	if res.Metadata.Name != "dev-1" || res.Kind != "VirtualMachine" {
		t.Errorf("example rendered wrong: name=%q kind=%q", res.Metadata.Name, res.Kind)
	}
}

// mustLoad loads templates from dir, failing the test on error.
func mustLoad(t *testing.T, dir string) []*Template {
	t.Helper()
	ts, err := LoadFromDir(dir)
	if err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	return ts
}

func names(ts []*Template) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}

func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return -1
}
