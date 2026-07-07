package form

import "testing"

// findField returns the named descendant field (depth-first).
func findField(f Field, name string) (Field, bool) {
	if f.Name == name {
		return f, true
	}
	for _, c := range f.Fields {
		if got, ok := findField(c, name); ok {
			return got, true
		}
	}
	if f.Items != nil {
		if got, ok := findField(*f.Items, name); ok {
			return got, true
		}
	}
	if f.ValueType != nil {
		if got, ok := findField(*f.ValueType, name); ok {
			return got, true
		}
	}
	return Field{}, false
}

// A field authored as `(string | base.#Secret) @secret()` is surfaced as a
// renderable string field flagged Secret — not an "unsupported" disjunction.
func TestFormWalker_SecretField(t *testing.T) {
	root, ok, err := BuildForKind("proxmox.openctl.io/v1", "VirtualMachine")
	if err != nil || !ok {
		t.Fatalf("build: ok=%v err=%v", ok, err)
	}
	pw, found := findField(root, "password")
	if !found {
		t.Fatal("password field not found")
	}
	if !pw.Secret {
		t.Errorf("password.Secret = false, want true")
	}
	if pw.Type == FieldUnsupported {
		t.Errorf("password rendered as unsupported: %q", pw.Reason)
	}
}
