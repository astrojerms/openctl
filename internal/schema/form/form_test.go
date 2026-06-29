package form

import (
	"testing"

	"cuelang.org/go/cue/cuecontext"
)

// build is a tiny test helper: compile a CUE source snippet and run
// the walker over its root. Keeps the tests focused on observable
// behaviour rather than CUE setup boilerplate. (Tests that need to
// look up a named definition can switch to LookupPath inline.)
func build(t *testing.T, src string) Field {
	t.Helper()
	ctx := cuecontext.New()
	v := ctx.CompileString(src)
	if err := v.Err(); err != nil {
		t.Fatalf("compile: %v", err)
	}
	f, err := FromValue(v)
	if err != nil {
		t.Fatalf("FromValue: %v", err)
	}
	return f
}

func TestWalkStringIntBoolPrimitives(t *testing.T) {
	src := `
		s: string
		i: int
		b: bool
	`
	f := build(t, src)
	if f.Type != FieldObject {
		t.Fatalf("root type = %s, want object", f.Type)
	}
	if got := len(f.Fields); got != 3 {
		t.Fatalf("len(fields) = %d, want 3", got)
	}
	want := []struct {
		name string
		typ  FieldType
	}{
		{"s", FieldString}, {"i", FieldInt}, {"b", FieldBool},
	}
	for i, w := range want {
		if f.Fields[i].Name != w.name || f.Fields[i].Type != w.typ {
			t.Errorf("field[%d] = %s/%s, want %s/%s",
				i, f.Fields[i].Name, f.Fields[i].Type, w.name, w.typ)
		}
		if f.Fields[i].Optional {
			t.Errorf("field[%d] %q should not be optional", i, f.Fields[i].Name)
		}
	}
}

func TestWalkOptionalAndDefault(t *testing.T) {
	src := `
		req: string
		opt?: string
		dflt: string | *"hello"
	`
	f := build(t, src)
	by := byName(f.Fields)

	if by["req"].Optional {
		t.Error("req should not be optional")
	}
	if !by["opt"].Optional {
		t.Error("opt should be optional")
	}
	if by["dflt"].Default != "hello" {
		t.Errorf("dflt default = %v, want \"hello\"", by["dflt"].Default)
	}
}

func TestWalkNumberBounds(t *testing.T) {
	src := `
		cores: int & >=1 | *2
		memMB: int & >=512
	`
	f := build(t, src)
	by := byName(f.Fields)

	cores := by["cores"]
	if cores.Type != FieldInt {
		t.Errorf("cores type = %s, want int", cores.Type)
	}
	if cores.Default != int64(2) && cores.Default != float64(2) && cores.Default != 2 {
		t.Errorf("cores default = %v (%T), want 2", cores.Default, cores.Default)
	}
	if cores.Min == nil || *cores.Min != 1 {
		t.Errorf("cores min = %v, want 1", cores.Min)
	}

	mem := by["memMB"]
	if mem.Min == nil || *mem.Min != 512 {
		t.Errorf("memMB min = %v, want 512", mem.Min)
	}
}

func TestWalkNestedStruct(t *testing.T) {
	src := `
		network: {
			bridge: string | *"vmbr0"
			dhcp: bool | *true
		}
	`
	f := build(t, src)
	if f.Fields[0].Type != FieldObject {
		t.Fatalf("network type = %s, want object", f.Fields[0].Type)
	}
	net := byName(f.Fields[0].Fields)
	if net["bridge"].Default != "vmbr0" {
		t.Errorf("bridge default = %v, want vmbr0", net["bridge"].Default)
	}
	if net["dhcp"].Default != true {
		t.Errorf("dhcp default = %v, want true", net["dhcp"].Default)
	}
}

func TestWalkArrayOfStructs(t *testing.T) {
	src := `
		workers: [...{name: string, count: int}]
	`
	f := build(t, src)
	workers := f.Fields[0]
	if workers.Type != FieldArray {
		t.Fatalf("workers type = %s, want array", workers.Type)
	}
	if workers.Items == nil {
		t.Fatal("workers.items is nil")
	}
	if workers.Items.Type != FieldObject {
		t.Fatalf("workers item type = %s, want object", workers.Items.Type)
	}
	itemFields := byName(workers.Items.Fields)
	if itemFields["name"].Type != FieldString {
		t.Errorf("item.name = %s, want string", itemFields["name"].Type)
	}
	if itemFields["count"].Type != FieldInt {
		t.Errorf("item.count = %s, want int", itemFields["count"].Type)
	}
}

func TestWalkArrayOfPrimitives(t *testing.T) {
	src := `
		tags: [...string]
	`
	f := build(t, src)
	tags := f.Fields[0]
	if tags.Items == nil || tags.Items.Type != FieldString {
		t.Errorf("tags item = %+v, want string", tags.Items)
	}
}

func TestWalkConstFieldsAreMarkedReadOnly(t *testing.T) {
	src := `
		apiVersion: "k3s.openctl.io/v1"
		kind: "Cluster"
		name: string
	`
	f := build(t, src)
	by := byName(f.Fields)
	if by["apiVersion"].Const != "k3s.openctl.io/v1" {
		t.Errorf("apiVersion const = %v, want \"k3s.openctl.io/v1\"", by["apiVersion"].Const)
	}
	if by["kind"].Const != "Cluster" {
		t.Errorf("kind const = %v, want \"Cluster\"", by["kind"].Const)
	}
	// name is unconstrained, must NOT be const.
	if by["name"].Const != nil {
		t.Errorf("name should not be const, got %v", by["name"].Const)
	}
}

func TestWalkTopAndAny(t *testing.T) {
	// CUE `_` collapses to TopKind — render as the "any" escape hatch.
	src := `
		raw: _
	`
	f := build(t, src)
	if f.Fields[0].Type != FieldAny {
		t.Errorf("raw type = %s, want any", f.Fields[0].Type)
	}
}

// byName indexes a Field list by name for test assertions.
func byName(fs []Field) map[string]Field {
	out := make(map[string]Field, len(fs))
	for _, f := range fs {
		out[f.Name] = f
	}
	return out
}
