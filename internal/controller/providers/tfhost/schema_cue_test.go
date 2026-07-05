package tfhost_test

import (
	"strings"
	"testing"

	"github.com/openctl/openctl/internal/controller/providers/tfhost"
	openctlschema "github.com/openctl/openctl/internal/schema"
	"github.com/openctl/openctl/pkg/protocol"
	"github.com/openctl/openctl/pkg/tfplugin6"
)

func TestSchemaToCUERegistersExternalSchema(t *testing.T) {
	openctlschema.ResetExternal()
	defer openctlschema.ResetExternal()

	resp := &tfplugin6.GetProviderSchema_Response{
		ResourceSchemas: map[string]*tfplugin6.Schema{"fake_thing": fakeThingCueSchema()},
	}
	if err := tfhost.RegisterExternalSchemas("fake.openctl.io/v1", []tfhost.ResourceMapping{
		{Kind: "Thing", TypeName: "fake_thing"},
	}, resp.ResourceSchemas); err != nil {
		t.Fatalf("RegisterExternalSchemas: %v", err)
	}

	info, ok := findSchema("fake.openctl.io/v1", "Thing")
	if !ok {
		t.Fatal("generated external schema not present in registry")
	}
	source, err := openctlschema.SourceFor(info)
	if err != nil {
		t.Fatalf("SourceFor: %v", err)
	}
	if src := string(source); !strings.Contains(src, `#Thing`) || !strings.Contains(src, `"note"?: string`) {
		t.Fatalf("generated source missing expected definition/field:\n%s", src)
	}

	valid := thing(map[string]any{"name": "alpha", "note": "ok"})
	if err := openctlschema.Validate(valid); err != nil {
		t.Fatalf("valid generated schema resource rejected: %v", err)
	}

	missingRequired := thing(map[string]any{"note": "missing name"})
	if err := openctlschema.Validate(missingRequired); err == nil {
		t.Fatal("resource missing required Terraform attribute was accepted")
	}

	withComputedOutput := thing(map[string]any{"name": "alpha", "id": "fake-alpha"})
	if err := openctlschema.Validate(withComputedOutput); err == nil {
		t.Fatal("resource setting computed-only Terraform output was accepted")
	}
}

func TestSchemaToCUENestedTypes(t *testing.T) {
	openctlschema.ResetExternal()
	defer openctlschema.ResetExternal()

	nested := &tfplugin6.Schema{
		Version: 1,
		Block: &tfplugin6.Schema_Block{
			Attributes: []*tfplugin6.Schema_Attribute{
				{
					Name:     "labels",
					Type:     []byte(`["map","string"]`),
					Optional: true,
				},
				{
					Name:     "rules",
					Type:     []byte(`["list",["object",{"port":"number","name":"string"}]]`),
					Optional: true,
				},
			},
			BlockTypes: []*tfplugin6.Schema_NestedBlock{
				{
					TypeName: "settings",
					Nesting:  tfplugin6.Schema_NestedBlock_SINGLE,
					Block: &tfplugin6.Schema_Block{
						Attributes: []*tfplugin6.Schema_Attribute{
							{Name: "enabled", Type: []byte(`"bool"`), Required: true},
						},
					},
				},
			},
		},
	}
	if err := tfhost.RegisterExternalSchemas("fake.openctl.io/v1", []tfhost.ResourceMapping{
		{Kind: "Complex", TypeName: "fake_complex"},
	}, map[string]*tfplugin6.Schema{"fake_complex": nested}); err != nil {
		t.Fatalf("RegisterExternalSchemas: %v", err)
	}

	valid := &protocol.Resource{
		APIVersion: "fake.openctl.io/v1",
		Kind:       "Complex",
		Metadata:   protocol.ResourceMetadata{Name: "complex"},
		Spec: map[string]any{
			"labels": map[string]any{"env": "test"},
			"rules":  []any{map[string]any{"name": "http", "port": 80}},
			"settings": map[string]any{
				"enabled": true,
			},
		},
	}
	if err := openctlschema.Validate(valid); err != nil {
		t.Fatalf("valid nested generated schema resource rejected: %v", err)
	}

	invalid := &protocol.Resource{
		APIVersion: "fake.openctl.io/v1",
		Kind:       "Complex",
		Metadata:   protocol.ResourceMetadata{Name: "complex"},
		Spec: map[string]any{
			"rules": []any{map[string]any{"name": "http", "port": "not-a-number"}},
		},
	}
	if err := openctlschema.Validate(invalid); err == nil {
		t.Fatal("invalid nested generated schema resource was accepted")
	}
}

func thing(spec map[string]any) *protocol.Resource {
	return &protocol.Resource{
		APIVersion: "fake.openctl.io/v1",
		Kind:       "Thing",
		Metadata:   protocol.ResourceMetadata{Name: "alpha"},
		Spec:       spec,
	}
}

func fakeThingCueSchema() *tfplugin6.Schema {
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
				{Name: "id", Type: []byte(`"string"`), Computed: true},
			},
		},
	}
}

func findSchema(apiVersion, kind string) (openctlschema.Info, bool) {
	for _, info := range openctlschema.Registry() {
		if info.APIVersion == apiVersion && info.Kind == kind {
			return info, true
		}
	}
	return openctlschema.Info{}, false
}
