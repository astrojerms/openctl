// Package form walks a CUE schema value and produces a UI-renderable
// FormField tree. The output is JSON-shaped (no CUE types reach the
// wire) so the controller can ship it via SchemaService.GetFormSchema
// and the browser doesn't need to know anything about CUE.
//
// Scope for UI Phase U5.1: handle the constructs the shipped VM and
// Cluster schemas use — strings, ints, bools, nested objects, arrays
// of objects, arrays of primitives, optional+required, defaults, and
// the bounded-number pattern (`int & >=1`). Constructs we don't yet
// support (free disjunctions, regex patterns, open structs) emit a
// "unsupported" leaf carrying a short reason; the form renderer greys
// those out and falls back to the CUE editor for that subtree.
//
// Defer to later sub-phases:
//   - regex patterns → field-level constraint (U5.3)
//   - non-bound disjunctions → discriminated-union UI (U5.3)
//   - map[string]string (labels/annotations) → key/value editor (U5.3)
package form

import (
	"fmt"
	"strings"

	"cuelang.org/go/cue"
)

// FieldType is the wire-side tag the renderer dispatches on. Keep in
// sync with the TS interface in ui/src/lib/form.ts.
type FieldType string

const (
	FieldString      FieldType = "string"
	FieldInt         FieldType = "int"
	FieldNumber      FieldType = "number"
	FieldBool        FieldType = "bool"
	FieldObject      FieldType = "object"
	FieldArray       FieldType = "array"
	FieldAny         FieldType = "any"         // CUE `_` — escape hatch
	FieldUnsupported FieldType = "unsupported" // walker hit something it can't model
)

// Field describes a single editable field in the form. Maps 1:1 onto the
// FormField TypeScript interface; serialised as JSON.
type Field struct {
	// Name is the field key in the parent object. Empty at the root.
	Name string `json:"name,omitempty"`
	// Type is one of the FieldType constants above.
	Type FieldType `json:"type"`
	// Optional fields can be omitted from the manifest entirely.
	Optional bool `json:"optional,omitempty"`
	// Default is the CUE-declared default value (the `*` marker), if any.
	// JSON-typed so the wire shape stays uniform.
	Default any `json:"default,omitempty"`
	// Description is a one-line summary pulled from CUE comments (//) on
	// the field. Empty when no comment is present.
	Description string `json:"description,omitempty"`
	// Const is set when the field is pinned to a single literal value
	// (e.g. `apiVersion: "k3s.openctl.io/v1"`). The form renders these
	// as read-only.
	Const any `json:"const,omitempty"`

	// Number constraints (Min/Max are inclusive). Set on FieldInt / FieldNumber.
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`

	// FieldObject: child fields, in CUE source order.
	Fields []Field `json:"fields,omitempty"`

	// FieldArray: element schema. nil means "any element" (we still emit
	// FieldArray so the renderer shows an add-row affordance; each row
	// renders as Field.Type=any).
	Items *Field `json:"items,omitempty"`

	// FieldUnsupported: human-readable reason.
	Reason string `json:"reason,omitempty"`
}

// FromValue walks the given CUE value and produces a Field tree. The
// caller usually passes the spec subtree of a #VirtualMachine or
// #Cluster definition.
func FromValue(v cue.Value) (Field, error) {
	if !v.Exists() {
		return Field{}, fmt.Errorf("value does not exist")
	}
	return walk(v, false), nil
}

// walk converts a single CUE value into a Field. optional is set by the
// parent struct's field iterator — CUE values themselves don't carry
// "optional" since optionality is a property of the parent field.
func walk(v cue.Value, optional bool) Field {
	f := Field{Optional: optional}
	if def, ok := v.Default(); ok {
		// The default value is itself a CUE value; render as Go-side any
		// for JSON serialisation. Use Decode on the concrete default,
		// not on v — decoding v would unify with the constraints.
		var dec any
		if err := def.Decode(&dec); err == nil {
			f.Default = dec
		}
	}
	if d := readDocs(v); d != "" {
		f.Description = d
	}

	// Const detection: a value is "concrete" iff CUE can fully determine
	// it (e.g. `apiVersion: "k3s.openctl.io/v1"`). We never want users
	// to edit those.
	if v.IsConcrete() && v.Kind() != cue.StructKind && v.Kind() != cue.ListKind {
		var dec any
		if err := v.Decode(&dec); err == nil {
			f.Const = dec
			f.Type = kindToFieldType(v.IncompleteKind())
			return f
		}
	}

	switch kind := v.IncompleteKind(); kind {
	case cue.StructKind:
		f.Type = FieldObject
		f.Fields = walkStruct(v)
	case cue.ListKind:
		f.Type = FieldArray
		items := walkListElem(v)
		f.Items = &items
	case cue.StringKind:
		f.Type = FieldString
	case cue.IntKind:
		f.Type = FieldInt
		applyNumberBounds(v, &f)
	case cue.FloatKind, cue.NumberKind:
		f.Type = FieldNumber
		applyNumberBounds(v, &f)
	case cue.BoolKind:
		f.Type = FieldBool
	case cue.TopKind:
		// CUE `_` — anything goes. Render as a freeform JSON/YAML
		// textarea.
		f.Type = FieldAny
	default:
		f.Type = FieldUnsupported
		f.Reason = fmt.Sprintf("unsupported CUE kind: %s", kind.String())
	}
	return f
}

// walkStruct iterates a struct's fields (including optional ones) in
// source order and produces the child Field list.
func walkStruct(v cue.Value) []Field {
	var out []Field
	iter, err := v.Fields(cue.Optional(true), cue.Definitions(false))
	if err != nil {
		return out
	}
	for iter.Next() {
		child := walk(iter.Value(), iter.IsOptional())
		// Selector.String() includes the "?" suffix on optional fields;
		// Unquoted() returns just the bare name so the form keys line
		// up with the JSON/YAML manifest the user submits.
		child.Name = iter.Selector().Unquoted()
		out = append(out, child)
	}
	return out
}

// walkListElem extracts the element schema for an array. CUE expresses
// `[...T]` as a list with an "any-index" constraint of type T; we look
// it up via the AnyIndex pattern. For heterogeneous tuples we fall back
// to FieldAny — the U5.1 form doesn't model fixed-shape tuples.
func walkListElem(v cue.Value) Field {
	elem := v.LookupPath(cue.MakePath(cue.AnyIndex))
	if !elem.Exists() {
		return Field{Type: FieldAny}
	}
	return walk(elem, false)
}

// applyNumberBounds extracts >=, <=, >, < constraints from a CUE value.
// CUE doesn't expose these as first-class API; we fall back to scanning
// the value's text representation, which is brittle but works for the
// shapes the shipped schemas use (`int & >=1`, `int & >=512`). When we
// can't parse them we leave Min/Max nil — the form just won't enforce
// client-side bounds; the server-side Validate still catches them.
func applyNumberBounds(v cue.Value, f *Field) {
	src := fmt.Sprintf("%v", v)
	for _, op := range []string{">=", "<=", ">", "<"} {
		_, after, ok := strings.Cut(src, op)
		if !ok {
			continue
		}
		rest := strings.TrimSpace(after)
		// Stop at the next operator or end-of-expression.
		end := len(rest)
		for i, r := range rest {
			if r == ' ' || r == '|' || r == '&' {
				end = i
				break
			}
		}
		num := strings.TrimSpace(rest[:end])
		var f64 float64
		if _, err := fmt.Sscanf(num, "%f", &f64); err != nil {
			continue
		}
		switch op {
		case ">=":
			f.Min = &f64
		case ">":
			adj := f64 + 1 // approximate strict bound as inclusive +1 for ints
			f.Min = &adj
		case "<=":
			f.Max = &f64
		case "<":
			adj := f64 - 1
			f.Max = &adj
		}
	}
}

// readDocs pulls the leading "// ..." comments off a CUE value, if any.
// CUE associates docs with the field declaration; v.Doc() returns the
// CommentGroup list.
func readDocs(v cue.Value) string {
	groups := v.Doc()
	if len(groups) == 0 {
		return ""
	}
	var parts []string
	for _, g := range groups {
		for _, c := range g.List {
			t := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
			if t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, " ")
}

func kindToFieldType(k cue.Kind) FieldType {
	switch k {
	case cue.StringKind:
		return FieldString
	case cue.IntKind:
		return FieldInt
	case cue.FloatKind, cue.NumberKind:
		return FieldNumber
	case cue.BoolKind:
		return FieldBool
	default:
		return FieldAny
	}
}
