package tfhost

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	openctlschema "github.com/openctl/openctl/internal/schema"
	"github.com/openctl/openctl/pkg/tfplugin6"
)

// SchemaToCUE translates a Terraform resource schema into the standalone CUE
// shape expected by internal/schema.RegisterExternal. The generated schema
// validates the whole openctl resource and keeps provider-computed-only
// attributes out of spec, since those are observed state rather than desired
// input.
func SchemaToCUE(apiVersion, kind string, schema *tfplugin6.Schema) (string, error) {
	if apiVersion == "" || kind == "" {
		return "", fmt.Errorf("apiVersion and kind are required")
	}
	if schema == nil || schema.GetBlock() == nil {
		return "", fmt.Errorf("terraform schema for %s/%s has no block", apiVersion, kind)
	}
	spec, err := blockCue(schema.GetBlock(), 1)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "#%s: {\n", kind)
	fmt.Fprintf(&b, "\tapiVersion: %s\n", strconv.Quote(apiVersion))
	fmt.Fprintf(&b, "\tkind:       %s\n", strconv.Quote(kind))
	b.WriteString("\tmetadata: {\n")
	b.WriteString("\t\tname: string\n")
	b.WriteString("\t\t...\n")
	b.WriteString("\t}\n")
	b.WriteString("\tspec: ")
	b.WriteString(spec)
	b.WriteString("\n\tstatus?: _\n")
	b.WriteString("\t...\n")
	b.WriteString("}\n")
	return b.String(), nil
}

// RegisterExternalSchemas generates and registers external CUE schemas for a
// set of Kind -> Terraform type mappings. resourceSchemas is the
// GetProviderSchema response's ResourceSchemas map.
func RegisterExternalSchemas(apiVersion string, mappings []ResourceMapping, resourceSchemas map[string]*tfplugin6.Schema) error {
	for _, m := range mappings {
		schema, ok := resourceSchemas[m.TypeName]
		if !ok {
			return fmt.Errorf("terraform resource type %q for kind %q not offered by provider", m.TypeName, m.Kind)
		}
		source, err := SchemaToCUE(apiVersion, m.Kind, schema)
		if err != nil {
			return err
		}
		openctlschema.RegisterExternal(apiVersion, m.Kind, source)
	}
	return nil
}

func blockCue(block *tfplugin6.Schema_Block, indent int) (string, error) {
	if block == nil {
		return "{\n" + tabs(indent) + "... " + "\n" + tabs(indent-1) + "}", nil
	}

	var fields []cueField
	for _, attr := range block.GetAttributes() {
		if attr == nil || attr.GetName() == "" || isComputedOnly(attr) {
			continue
		}
		expr, err := attributeCueType(attr, indent+1)
		if err != nil {
			return "", fmt.Errorf("attribute %q: %w", attr.GetName(), err)
		}
		fields = append(fields, cueField{
			name:        attr.GetName(),
			optional:    !attr.GetRequired(),
			expr:        expr,
			description: attr.GetDescription(),
		})
	}
	for _, nested := range block.GetBlockTypes() {
		if nested == nil || nested.GetTypeName() == "" {
			continue
		}
		expr, err := nestedBlockCueType(nested, indent+1)
		if err != nil {
			return "", fmt.Errorf("nested block %q: %w", nested.GetTypeName(), err)
		}
		fields = append(fields, cueField{
			name:        nested.GetTypeName(),
			optional:    nested.GetMinItems() == 0,
			expr:        expr,
			description: nested.GetBlock().GetDescription(),
		})
	}

	sort.Slice(fields, func(i, j int) bool { return fields[i].name < fields[j].name })

	var b strings.Builder
	b.WriteString("{\n")
	for _, f := range fields {
		writeDescription(&b, f.description, indent)
		fmt.Fprintf(&b, "%s%s", tabs(indent), cueLabel(f.name))
		if f.optional {
			b.WriteString("?")
		}
		fmt.Fprintf(&b, ": %s\n", f.expr)
	}
	b.WriteString(tabs(indent - 1))
	b.WriteString("}")
	return b.String(), nil
}

func attributeCueType(attr *tfplugin6.Schema_Attribute, indent int) (string, error) {
	if nested := attr.GetNestedType(); nested != nil {
		expr, err := objectCue(nested.GetAttributes(), indent)
		if err != nil {
			return "", err
		}
		return wrapNestedObject(expr, nested.GetNesting()), nil
	}
	t, err := parseCtyType(attr.GetType())
	if err != nil {
		return "", err
	}
	return cueType(t, indent), nil
}

func nestedBlockCueType(nested *tfplugin6.Schema_NestedBlock, indent int) (string, error) {
	expr, err := blockCue(nested.GetBlock(), indent)
	if err != nil {
		return "", err
	}
	switch nested.GetNesting() {
	case tfplugin6.Schema_NestedBlock_SINGLE, tfplugin6.Schema_NestedBlock_GROUP:
		return expr, nil
	case tfplugin6.Schema_NestedBlock_LIST, tfplugin6.Schema_NestedBlock_SET:
		return "[..." + expr + "]", nil
	case tfplugin6.Schema_NestedBlock_MAP:
		return "{[string]: " + expr + "}", nil
	default:
		return "_", nil
	}
}

func objectCue(attrs []*tfplugin6.Schema_Attribute, indent int) (string, error) {
	block := &tfplugin6.Schema_Block{Attributes: attrs}
	return blockCue(block, indent)
}

func wrapNestedObject(expr string, nesting tfplugin6.Schema_Object_NestingMode) string {
	switch nesting {
	case tfplugin6.Schema_Object_SINGLE:
		return expr
	case tfplugin6.Schema_Object_LIST, tfplugin6.Schema_Object_SET:
		return "[..." + expr + "]"
	case tfplugin6.Schema_Object_MAP:
		return "{[string]: " + expr + "}"
	default:
		return expr
	}
}

func cueType(t tftypes.Type, indent int) string {
	if t == nil || t.Is(tftypes.DynamicPseudoType) {
		return "_"
	}
	if t.Is(tftypes.String) {
		return "string"
	}
	if t.Is(tftypes.Number) {
		return "number"
	}
	if t.Is(tftypes.Bool) {
		return "bool"
	}

	switch tt := t.(type) {
	case tftypes.List:
		return "[..." + cueType(tt.ElementType, indent) + "]"
	case tftypes.Set:
		return "[..." + cueType(tt.ElementType, indent) + "]"
	case tftypes.Map:
		return "{[string]: " + cueType(tt.ElementType, indent) + "}"
	case tftypes.Tuple:
		parts := make([]string, 0, len(tt.ElementTypes))
		for _, elem := range tt.ElementTypes {
			parts = append(parts, cueType(elem, indent))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case tftypes.Object:
		return cueObjectType(tt, indent)
	default:
		return "_"
	}
}

func cueObjectType(obj tftypes.Object, indent int) string {
	keys := make([]string, 0, len(obj.AttributeTypes))
	for name := range obj.AttributeTypes {
		keys = append(keys, name)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("{\n")
	for _, name := range keys {
		fmt.Fprintf(&b, "%s%s", tabs(indent), cueLabel(name))
		if _, ok := obj.OptionalAttributes[name]; ok {
			b.WriteString("?")
		}
		fmt.Fprintf(&b, ": %s\n", cueType(obj.AttributeTypes[name], indent+1))
	}
	b.WriteString(tabs(indent - 1))
	b.WriteString("}")
	return b.String()
}

type cueField struct {
	name        string
	optional    bool
	expr        string
	description string
}

func isComputedOnly(attr *tfplugin6.Schema_Attribute) bool {
	return attr.GetComputed() && !attr.GetOptional() && !attr.GetRequired()
}

func cueLabel(name string) string {
	return strconv.Quote(name)
}

func tabs(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("\t", n)
}

func writeDescription(b *strings.Builder, desc string, indent int) {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return
	}
	for _, line := range strings.Split(desc, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fmt.Fprintf(b, "%s// %s\n", tabs(indent), strings.ReplaceAll(line, "\r", " "))
	}
}
