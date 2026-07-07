package form

import "testing"

// collectFields returns every descendant field (including nested/array/map).
func collectFields(f Field, out *[]Field) {
	*out = append(*out, f)
	for _, c := range f.Fields {
		collectFields(c, out)
	}
	if f.Items != nil {
		collectFields(*f.Items, out)
	}
	if f.ValueType != nil {
		collectFields(*f.ValueType, out)
	}
}

// findOptionField returns the field of the given name whose OptionsSource has
// the given dotted `field` path. There are two "storage" fields (template vs
// disk); this picks the annotated one.
func findOptionField(root Field, name, optField string) (Field, bool) {
	var all []Field
	collectFields(root, &all)
	for _, f := range all {
		if f.Name == name && f.OptionsSource != nil && f.OptionsSource.Field == optField {
			return f, true
		}
	}
	return Field{}, false
}

// A field annotated `@options(kind, field, dependsOn)` surfaces all three on
// its OptionsSource so the UI can resolve node-dependent dropdowns.
func TestFormWalker_DependentOptions(t *testing.T) {
	root, ok, err := BuildForKind("proxmox.openctl.io/v1", "VirtualMachine")
	if err != nil || !ok {
		t.Fatalf("build: ok=%v err=%v", ok, err)
	}

	storage, found := findOptionField(root, "storage", "status.storages")
	if !found {
		t.Fatal("annotated disk storage field not found")
	}
	if s := storage.OptionsSource; s.Kind != "ProxmoxNode" || s.DependsOn != "spec.node" {
		t.Errorf("storage OptionsSource = %+v, want ProxmoxNode dependsOn spec.node", s)
	}

	if _, found := findOptionField(root, "bridge", "status.bridges"); !found {
		t.Error("annotated bridge field not found")
	}

	// The plain `spec.node` @options (names only) must NOT carry field/dependsOn.
	node, found := findField(root, "node")
	if !found {
		t.Fatal("node field not found")
	}
	if node.OptionsSource == nil || node.OptionsSource.Field != "" || node.OptionsSource.DependsOn != "" {
		t.Errorf("node OptionsSource should be names-only, got %+v", node.OptionsSource)
	}
}
