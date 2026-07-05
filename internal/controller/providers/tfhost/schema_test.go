package tfhost

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/openctl/openctl/pkg/tfplugin6"
)

// fakeThingSchema mirrors what plugins/tf-fake advertises: name (required
// string) + note (optional string).
func fakeThingSchema() *tfplugin6.Schema {
	strAttr := func(name string, required bool) *tfplugin6.Schema_Attribute {
		return &tfplugin6.Schema_Attribute{
			Name:     name,
			Type:     []byte(`"string"`),
			Required: required,
			Optional: !required,
		}
	}
	return &tfplugin6.Schema{
		Version: 1,
		Block: &tfplugin6.Schema_Block{
			Attributes: []*tfplugin6.Schema_Attribute{
				strAttr("name", true),
				strAttr("note", false),
			},
		},
	}
}

func TestSchemaAttributes(t *testing.T) {
	attrs, err := SchemaAttributes(fakeThingSchema())
	if err != nil {
		t.Fatalf("SchemaAttributes: %v", err)
	}
	if len(attrs) != 2 {
		t.Fatalf("got %d attributes, want 2", len(attrs))
	}
	// Sorted by name: name, note.
	if attrs[0].Name != "name" || !attrs[0].Required {
		t.Errorf("attr[0] = %+v, want name/required", attrs[0])
	}
	if attrs[1].Name != "note" || !attrs[1].Optional {
		t.Errorf("attr[1] = %+v, want note/optional", attrs[1])
	}
	if !attrs[0].Type.Is(tftypes.String) {
		t.Errorf("name type = %v, want String", attrs[0].Type)
	}
}

func TestObjectTypeForSchema(t *testing.T) {
	obj, err := ObjectTypeForSchema(fakeThingSchema())
	if err != nil {
		t.Fatalf("ObjectTypeForSchema: %v", err)
	}
	if len(obj.AttributeTypes) != 2 {
		t.Fatalf("object has %d attrs, want 2", len(obj.AttributeTypes))
	}
	if !obj.AttributeTypes["name"].Is(tftypes.String) {
		t.Errorf("name type = %v", obj.AttributeTypes["name"])
	}
	// note is optional → in the OptionalAttributes set; name is not.
	if _, ok := obj.OptionalAttributes["note"]; !ok {
		t.Error("note should be optional")
	}
	if _, ok := obj.OptionalAttributes["name"]; ok {
		t.Error("name (required) should not be optional")
	}
}

func TestParseCtyTypePrimitivesAndComposites(t *testing.T) {
	cases := []struct {
		json string
		want tftypes.Type
	}{
		{`"string"`, tftypes.String},
		{`"number"`, tftypes.Number},
		{`"bool"`, tftypes.Bool},
		{`"dynamic"`, tftypes.DynamicPseudoType},
		{`["list","string"]`, tftypes.List{ElementType: tftypes.String}},
		{`["set","number"]`, tftypes.Set{ElementType: tftypes.Number}},
		{`["map","bool"]`, tftypes.Map{ElementType: tftypes.Bool}},
		{`["object",{"a":"string","b":"number"}]`, tftypes.Object{AttributeTypes: map[string]tftypes.Type{"a": tftypes.String, "b": tftypes.Number}}},
		{`["tuple",["string","bool"]]`, tftypes.Tuple{ElementTypes: []tftypes.Type{tftypes.String, tftypes.Bool}}},
	}
	for _, c := range cases {
		got, err := parseCtyType([]byte(c.json))
		if err != nil {
			t.Errorf("%s: %v", c.json, err)
			continue
		}
		if !got.Is(c.want) {
			t.Errorf("%s: got %v, want %v", c.json, got, c.want)
		}
	}
}

func TestParseCtyTypeObjectOptionalAttrs(t *testing.T) {
	got, err := parseCtyType([]byte(`["object",{"a":"string","b":"string"},["b"]]`))
	if err != nil {
		t.Fatal(err)
	}
	obj, ok := got.(tftypes.Object)
	if !ok {
		t.Fatalf("got %T, want Object", got)
	}
	if _, ok := obj.OptionalAttributes["b"]; !ok {
		t.Error("b should be optional")
	}
	if _, ok := obj.OptionalAttributes["a"]; ok {
		t.Error("a should be required")
	}
}

func TestParseCtyTypeRejectsGarbage(t *testing.T) {
	for _, bad := range []string{`"nope"`, `["frob","string"]`, `["list"]`, `42`} {
		if _, err := parseCtyType([]byte(bad)); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
}

func TestObjectTypeForSchemaHandlesCollections(t *testing.T) {
	// A list(string) attribute must parse into the right tftypes.List.
	schema := &tfplugin6.Schema{
		Block: &tfplugin6.Schema_Block{
			Attributes: []*tfplugin6.Schema_Attribute{
				{Name: "tags", Type: []byte(`["list","string"]`), Optional: true},
			},
		},
	}
	obj, err := ObjectTypeForSchema(schema)
	if err != nil {
		t.Fatalf("ObjectTypeForSchema: %v", err)
	}
	tagsType, ok := obj.AttributeTypes["tags"]
	if !ok {
		t.Fatal("tags attribute missing")
	}
	if !tagsType.Is(tftypes.List{ElementType: tftypes.String}) {
		t.Errorf("tags type = %v, want list(string)", tagsType)
	}
}

func TestResourceSchemaLookup(t *testing.T) {
	resp := &tfplugin6.GetProviderSchema_Response{
		ResourceSchemas: map[string]*tfplugin6.Schema{"fake_thing": fakeThingSchema()},
	}
	if _, err := ResourceSchema(resp, "fake_thing"); err != nil {
		t.Errorf("lookup fake_thing: %v", err)
	}
	if _, err := ResourceSchema(resp, "missing"); err == nil {
		t.Error("expected error for unknown resource type")
	}
}

func TestSchemaAttributesRejectsNilBlock(t *testing.T) {
	if _, err := SchemaAttributes(&tfplugin6.Schema{}); err == nil {
		t.Error("expected error for schema with no block")
	}
}
