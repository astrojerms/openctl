package tfhost

import (
	"fmt"
	"math/big"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/openctl/openctl/pkg/tfplugin6"
)

// This file bridges openctl's map[string]any specs/state to the Terraform
// plugin wire format via tftypes. Real framework providers exchange values as
// msgpack-encoded DynamicValues typed by the resource schema's implied type
// (including nested blocks), which the previous JSON-only path could neither
// encode correctly nor decode at all. We build the exact implied type here and
// use terraform-plugin-go's codecs so any real provider round-trips.
//
// The implied type carries NO OptionalAttributes: Terraform's wire type has
// every attribute present (null when unset), and marking attributes optional
// would produce a type a real provider rejects. We null-fill missing values on
// encode instead.

// blockType builds the tftypes.Object implied by a schema block: its attributes
// plus every nested block (keyed by block type name, wrapped by nesting mode).
func blockType(block *tfplugin6.Schema_Block) (tftypes.Type, error) {
	if block == nil {
		return tftypes.Object{AttributeTypes: map[string]tftypes.Type{}}, nil
	}
	attrTypes := make(map[string]tftypes.Type, len(block.GetAttributes())+len(block.GetBlockTypes()))

	for _, a := range block.GetAttributes() {
		t, err := attributeType(a)
		if err != nil {
			return nil, fmt.Errorf("attribute %q: %w", a.GetName(), err)
		}
		attrTypes[a.GetName()] = t
	}

	for _, bt := range block.GetBlockTypes() {
		nested, err := blockType(bt.GetBlock())
		if err != nil {
			return nil, fmt.Errorf("block %q: %w", bt.GetTypeName(), err)
		}
		wrapped, err := wrapNesting(bt.GetNesting(), nested)
		if err != nil {
			return nil, fmt.Errorf("block %q: %w", bt.GetTypeName(), err)
		}
		attrTypes[bt.GetTypeName()] = wrapped
	}

	return tftypes.Object{AttributeTypes: attrTypes}, nil
}

// attributeType resolves one attribute's type: a protocol-6 NestedType (object
// attribute) when present, else the cty type JSON.
func attributeType(a *tfplugin6.Schema_Attribute) (tftypes.Type, error) {
	if nt := a.GetNestedType(); nt != nil {
		return nestedObjectType(nt)
	}
	return parseCtyType(a.GetType())
}

// nestedObjectType handles a protocol-6 SchemaObject (typed nested attribute),
// wrapping its object type by the object's own nesting mode.
func nestedObjectType(obj *tfplugin6.Schema_Object) (tftypes.Type, error) {
	attrTypes := make(map[string]tftypes.Type, len(obj.GetAttributes()))
	for _, a := range obj.GetAttributes() {
		t, err := attributeType(a)
		if err != nil {
			return nil, fmt.Errorf("nested attribute %q: %w", a.GetName(), err)
		}
		attrTypes[a.GetName()] = t
	}
	object := tftypes.Object{AttributeTypes: attrTypes}
	switch obj.GetNesting() {
	case tfplugin6.Schema_Object_SINGLE:
		return object, nil
	case tfplugin6.Schema_Object_LIST:
		return tftypes.List{ElementType: object}, nil
	case tfplugin6.Schema_Object_SET:
		return tftypes.Set{ElementType: object}, nil
	case tfplugin6.Schema_Object_MAP:
		return tftypes.Map{ElementType: object}, nil
	default:
		return nil, fmt.Errorf("unsupported nested object nesting %v", obj.GetNesting())
	}
}

// wrapNesting wraps a nested block's object type by its NestingMode.
func wrapNesting(mode tfplugin6.Schema_NestedBlock_NestingMode, nested tftypes.Type) (tftypes.Type, error) {
	switch mode {
	case tfplugin6.Schema_NestedBlock_SINGLE, tfplugin6.Schema_NestedBlock_GROUP:
		return nested, nil
	case tfplugin6.Schema_NestedBlock_LIST:
		return tftypes.List{ElementType: nested}, nil
	case tfplugin6.Schema_NestedBlock_SET:
		return tftypes.Set{ElementType: nested}, nil
	case tfplugin6.Schema_NestedBlock_MAP:
		return tftypes.Map{ElementType: nested}, nil
	default:
		return nil, fmt.Errorf("unsupported block nesting %v", mode)
	}
}

// encodeConfig builds a msgpack DynamicValue for a resource/provider config from
// an openctl spec map, conforming to the schema's implied type (nested blocks
// included). Missing attributes are encoded as null.
func encodeConfig(schema *tfplugin6.Schema, spec map[string]any) (*tfplugin6.DynamicValue, error) {
	if schema == nil {
		return nil, fmt.Errorf("schema has no block")
	}
	typ, err := blockType(schema.GetBlock())
	if err != nil {
		return nil, err
	}
	val, err := goToValue(spec, typ)
	if err != nil {
		return nil, err
	}
	dv, err := tfprotov6.NewDynamicValue(typ, val)
	if err != nil {
		return nil, fmt.Errorf("encode dynamic value: %w", err)
	}
	// Bridge terraform-plugin-go's DynamicValue to openctl's vendored wire type.
	return &tfplugin6.DynamicValue{Msgpack: dv.MsgPack, Json: dv.JSON}, nil
}

// decodeState decodes a DynamicValue (msgpack or JSON) into a Go map using the
// schema's implied type. Returns nil for a null/absent value.
func decodeState(schema *tfplugin6.Schema, dv *tfplugin6.DynamicValue) (map[string]any, error) {
	if schema == nil {
		return nil, fmt.Errorf("schema has no block")
	}
	if dv == nil || (len(dv.GetMsgpack()) == 0 && len(dv.GetJson()) == 0) {
		return nil, nil
	}
	typ, err := blockType(schema.GetBlock())
	if err != nil {
		return nil, err
	}
	wire := tfprotov6.DynamicValue{MsgPack: dv.GetMsgpack(), JSON: dv.GetJson()}
	val, err := wire.Unmarshal(typ)
	if err != nil {
		return nil, fmt.Errorf("decode dynamic value: %w", err)
	}
	goVal, err := valueToGo(val)
	if err != nil {
		return nil, err
	}
	if goVal == nil {
		return nil, nil
	}
	m, ok := goVal.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("decoded state is %T, want object", goVal)
	}
	return m, nil
}

// goToValue builds a tftypes.Value from a Go value (map[string]any / []any /
// scalars from a spec) conforming to typ. Absent object attributes become null.
func goToValue(raw any, typ tftypes.Type) (tftypes.Value, error) {
	if raw == nil {
		return tftypes.NewValue(typ, nil), nil
	}
	switch t := typ.(type) {
	case tftypes.Object:
		m, err := asStringMap(raw)
		if err != nil {
			return tftypes.Value{}, err
		}
		vals := make(map[string]tftypes.Value, len(t.AttributeTypes))
		for name, at := range t.AttributeTypes {
			sub, err := goToValue(m[name], at) // absent -> nil -> null
			if err != nil {
				return tftypes.Value{}, fmt.Errorf("%s: %w", name, err)
			}
			vals[name] = sub
		}
		return tftypes.NewValue(t, vals), nil
	case tftypes.Map:
		m, err := asStringMap(raw)
		if err != nil {
			return tftypes.Value{}, err
		}
		vals := make(map[string]tftypes.Value, len(m))
		for k, v := range m {
			sub, err := goToValue(v, t.ElementType)
			if err != nil {
				return tftypes.Value{}, fmt.Errorf("%s: %w", k, err)
			}
			vals[k] = sub
		}
		return tftypes.NewValue(t, vals), nil
	case tftypes.List:
		return sliceToValue(raw, t, t.ElementType)
	case tftypes.Set:
		return sliceToValue(raw, t, t.ElementType)
	case tftypes.Tuple:
		l, ok := raw.([]any)
		if !ok {
			return tftypes.Value{}, fmt.Errorf("expected tuple/list, got %T", raw)
		}
		if len(l) != len(t.ElementTypes) {
			return tftypes.Value{}, fmt.Errorf("tuple arity %d, want %d", len(l), len(t.ElementTypes))
		}
		vals := make([]tftypes.Value, len(l))
		for i, e := range l {
			sub, err := goToValue(e, t.ElementTypes[i])
			if err != nil {
				return tftypes.Value{}, fmt.Errorf("[%d]: %w", i, err)
			}
			vals[i] = sub
		}
		return tftypes.NewValue(t, vals), nil
	default:
		return primitiveToValue(raw, typ)
	}
}

func sliceToValue(raw any, typ, elem tftypes.Type) (tftypes.Value, error) {
	l, ok := raw.([]any)
	if !ok {
		return tftypes.Value{}, fmt.Errorf("expected list/set, got %T", raw)
	}
	vals := make([]tftypes.Value, len(l))
	for i, e := range l {
		sub, err := goToValue(e, elem)
		if err != nil {
			return tftypes.Value{}, fmt.Errorf("[%d]: %w", i, err)
		}
		vals[i] = sub
	}
	return tftypes.NewValue(typ, vals), nil
}

func primitiveToValue(raw any, typ tftypes.Type) (tftypes.Value, error) {
	switch {
	case typ.Is(tftypes.String):
		s, ok := raw.(string)
		if !ok {
			return tftypes.Value{}, fmt.Errorf("expected string, got %T", raw)
		}
		return tftypes.NewValue(tftypes.String, s), nil
	case typ.Is(tftypes.Bool):
		b, ok := raw.(bool)
		if !ok {
			return tftypes.Value{}, fmt.Errorf("expected bool, got %T", raw)
		}
		return tftypes.NewValue(tftypes.Bool, b), nil
	case typ.Is(tftypes.Number):
		f, err := toFloat(raw)
		if err != nil {
			return tftypes.Value{}, err
		}
		return tftypes.NewValue(tftypes.Number, big.NewFloat(f)), nil
	default:
		return tftypes.Value{}, fmt.Errorf("unsupported attribute type %s for value %T", typ, raw)
	}
}

// valueToGo converts a decoded tftypes.Value into a plain Go value
// (map[string]any / []any / string / float64 / bool / nil).
func valueToGo(v tftypes.Value) (any, error) {
	if v.IsNull() || !v.IsKnown() {
		return nil, nil
	}
	switch v.Type().(type) {
	case tftypes.Object, tftypes.Map:
		var m map[string]tftypes.Value
		if err := v.As(&m); err != nil {
			return nil, err
		}
		out := make(map[string]any, len(m))
		for k, elem := range m {
			g, err := valueToGo(elem)
			if err != nil {
				return nil, err
			}
			out[k] = g
		}
		return out, nil
	case tftypes.List, tftypes.Set, tftypes.Tuple:
		var l []tftypes.Value
		if err := v.As(&l); err != nil {
			return nil, err
		}
		out := make([]any, 0, len(l))
		for _, elem := range l {
			g, err := valueToGo(elem)
			if err != nil {
				return nil, err
			}
			out = append(out, g)
		}
		return out, nil
	default: // primitive
		switch {
		case v.Type().Is(tftypes.String):
			var s string
			if err := v.As(&s); err != nil {
				return nil, err
			}
			return s, nil
		case v.Type().Is(tftypes.Bool):
			var b bool
			if err := v.As(&b); err != nil {
				return nil, err
			}
			return b, nil
		case v.Type().Is(tftypes.Number):
			var n *big.Float
			if err := v.As(&n); err != nil {
				return nil, err
			}
			f, _ := n.Float64()
			return f, nil
		default:
			return nil, fmt.Errorf("unsupported decoded type %s", v.Type())
		}
	}
}

func asStringMap(raw any) (map[string]any, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected object/map, got %T", raw)
	}
	return m, nil
}

func toFloat(raw any) (float64, error) {
	switch n := raw.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int32:
		return float64(n), nil
	case int64:
		return float64(n), nil
	default:
		return 0, fmt.Errorf("expected number, got %T", raw)
	}
}
