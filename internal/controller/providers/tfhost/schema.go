package tfhost

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/openctl/openctl/pkg/tfplugin6"
)

// parseCtyType decodes a Terraform cty type from its JSON wire encoding (as
// carried in tfplugin6.Schema_Attribute.Type) into a tftypes.Type. We parse it
// ourselves rather than call tftypes.ParseJSONType, which terraform-plugin-go
// deprecates for third-party use — owning the parser keeps the TF host off the
// library's unstable internal surface.
//
// The wire format (github.com/zclconf/go-cty type JSON):
//
//	"string" | "number" | "bool" | "dynamic"
//	["list"|"set"|"map", <elem type>]
//	["object", {name: <type>, ...}, [optional names]?]
//	["tuple", [<type>, ...]]
func parseCtyType(raw []byte) (tftypes.Type, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch s {
		case "string":
			return tftypes.String, nil
		case "number":
			return tftypes.Number, nil
		case "bool":
			return tftypes.Bool, nil
		case "dynamic":
			return tftypes.DynamicPseudoType, nil
		default:
			return nil, fmt.Errorf("unknown primitive cty type %q", s)
		}
	}

	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, fmt.Errorf("cty type is neither a string nor an array: %s", raw)
	}
	if len(arr) < 2 {
		return nil, fmt.Errorf("cty composite type too short: %s", raw)
	}
	var kind string
	if err := json.Unmarshal(arr[0], &kind); err != nil {
		return nil, fmt.Errorf("cty composite kind: %w", err)
	}

	switch kind {
	case "list", "set", "map":
		el, err := parseCtyType(arr[1])
		if err != nil {
			return nil, err
		}
		switch kind {
		case "list":
			return tftypes.List{ElementType: el}, nil
		case "set":
			return tftypes.Set{ElementType: el}, nil
		default:
			return tftypes.Map{ElementType: el}, nil
		}
	case "object":
		var attrs map[string]json.RawMessage
		if err := json.Unmarshal(arr[1], &attrs); err != nil {
			return nil, fmt.Errorf("cty object attrs: %w", err)
		}
		attrTypes := make(map[string]tftypes.Type, len(attrs))
		for name, at := range attrs {
			t, err := parseCtyType(at)
			if err != nil {
				return nil, fmt.Errorf("object attribute %q: %w", name, err)
			}
			attrTypes[name] = t
		}
		obj := tftypes.Object{AttributeTypes: attrTypes}
		if len(arr) >= 3 {
			var optional []string
			if err := json.Unmarshal(arr[2], &optional); err != nil {
				return nil, fmt.Errorf("cty object optional attrs: %w", err)
			}
			if len(optional) > 0 {
				obj.OptionalAttributes = make(map[string]struct{}, len(optional))
				for _, name := range optional {
					obj.OptionalAttributes[name] = struct{}{}
				}
			}
		}
		return obj, nil
	case "tuple":
		var elems []json.RawMessage
		if err := json.Unmarshal(arr[1], &elems); err != nil {
			return nil, fmt.Errorf("cty tuple elems: %w", err)
		}
		types := make([]tftypes.Type, 0, len(elems))
		for i, e := range elems {
			t, err := parseCtyType(e)
			if err != nil {
				return nil, fmt.Errorf("tuple element %d: %w", i, err)
			}
			types = append(types, t)
		}
		return tftypes.Tuple{ElementTypes: types}, nil
	default:
		return nil, fmt.Errorf("unknown composite cty kind %q", kind)
	}
}

// AttrInfo is a normalized view of one resource attribute from a tfplugin6
// schema — enough for openctl to know what fields a hosted resource has and
// how they behave, without the caller decoding cty type JSON itself.
type AttrInfo struct {
	Name     string
	Type     tftypes.Type // decoded from the attribute's cty type JSON
	Required bool
	Optional bool
	Computed bool
	// Sensitive marks secret values (e.g. passwords) so openctl can redact.
	Sensitive bool
}

// SchemaAttributes returns the top-level attributes of a resource schema,
// sorted by name for stable output. Nested blocks are not yet included (added
// when a hosted provider needs them); the common case is flat attributes.
func SchemaAttributes(schema *tfplugin6.Schema) ([]AttrInfo, error) {
	if schema == nil || schema.Block == nil {
		return nil, fmt.Errorf("schema has no block")
	}
	out := make([]AttrInfo, 0, len(schema.Block.Attributes))
	for _, a := range schema.Block.Attributes {
		t, err := parseCtyType(a.Type)
		if err != nil {
			return nil, fmt.Errorf("attribute %q: parse type: %w", a.Name, err)
		}
		out = append(out, AttrInfo{
			Name:      a.Name,
			Type:      t,
			Required:  a.Required,
			Optional:  a.Optional,
			Computed:  a.Computed,
			Sensitive: a.Sensitive,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ObjectTypeForSchema builds the tftypes.Object type that describes a
// resource's attributes. This is the type used to encode/decode a resource's
// config and state as a tfplugin6 DynamicValue on the wire — the foundation of
// the Apply/Read path.
//
// Optional and computed attributes are recorded in the object's
// OptionalAttributes set so a config value may omit them (Terraform fills
// computed values in during plan/apply).
func ObjectTypeForSchema(schema *tfplugin6.Schema) (tftypes.Object, error) {
	attrs, err := SchemaAttributes(schema)
	if err != nil {
		return tftypes.Object{}, err
	}
	attrTypes := make(map[string]tftypes.Type, len(attrs))
	optional := make(map[string]struct{})
	for _, a := range attrs {
		attrTypes[a.Name] = a.Type
		if a.Optional || a.Computed {
			optional[a.Name] = struct{}{}
		}
	}
	return tftypes.Object{AttributeTypes: attrTypes, OptionalAttributes: optional}, nil
}

// ResourceSchema returns the schema for a named resource type from a
// GetProviderSchema response, or an error naming the available types.
func ResourceSchema(resp *tfplugin6.GetProviderSchema_Response, resourceType string) (*tfplugin6.Schema, error) {
	if resp == nil {
		return nil, fmt.Errorf("nil schema response")
	}
	s, ok := resp.ResourceSchemas[resourceType]
	if !ok {
		return nil, fmt.Errorf("resource type %q not offered by provider (has %d types)", resourceType, len(resp.ResourceSchemas))
	}
	return s, nil
}
