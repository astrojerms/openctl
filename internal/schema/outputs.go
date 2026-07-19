package schema

import (
	"sort"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
)

// Output is one field a kind declares in its status — a value another resource
// can $ref (e.g. a Cluster's "status.outputs.kubeconfigPath"). Path is the
// dotted reference path; Type is the CUE kind ("string", "int", …); Doc is the
// field's leading comment.
type Output struct {
	Path string
	Type string
	Doc  string
}

// OutputsFor returns the status fields a kind declares in its schema — the
// values available to $ref off a resource of that kind. ok is false when the
// kind declares no status (it stays the open base default) or has no schema.
// Leaf fields are flattened to dotted paths, so a nested
// status.outputs.kubeconfigPath comes back as one Output. Purely a
// discoverability read of the declared schema — it does not inspect any live
// resource.
func OutputsFor(apiVersion, kind string) ([]Output, bool) {
	ctx := cuecontext.New()
	def, _, ok, err := schemaDefValue(ctx, apiVersion, kind)
	if err != nil || !ok {
		return nil, false
	}
	// status is an optional field; LookupPath is unreliable for optionals across
	// CUE versions, so find it by iterating fields.
	status, found := fieldByName(def, "status")
	if !found {
		return nil, false
	}
	var out []Output
	collectOutputs("status", status, &out)
	if len(out) == 0 {
		return nil, false
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, true
}

// collectOutputs walks a CUE struct, flattening leaf fields to dotted paths
// under prefix. Struct fields recurse; everything else is a leaf. A map-typed
// field (e.g. endpoints: {[string]: string}) is treated as a leaf — its keys
// are dynamic, so there's nothing static to enumerate beneath it.
func collectOutputs(prefix string, v cue.Value, out *[]Output) {
	it, err := v.Fields(cue.Optional(true), cue.Definitions(false))
	if err != nil {
		return
	}
	for it.Next() {
		name := it.Selector().Unquoted()
		fv := it.Value()
		path := prefix + "." + name
		if fv.IncompleteKind() == cue.StructKind && !isMap(fv) {
			collectOutputs(path, fv, out)
			continue
		}
		*out = append(*out, Output{Path: path, Type: kindString(fv), Doc: docOf(fv)})
	}
}

// fieldByName returns the value of the named (possibly optional) field of a
// struct value, and whether it was found.
func fieldByName(v cue.Value, name string) (cue.Value, bool) {
	it, err := v.Fields(cue.Optional(true), cue.Definitions(false))
	if err != nil {
		return cue.Value{}, false
	}
	for it.Next() {
		if it.Selector().Unquoted() == name {
			return it.Value(), true
		}
	}
	return cue.Value{}, false
}

// isMap reports whether a struct value has no enumerable named fields — a
// {[string]: T} pattern map (or an empty struct). Such a value is treated as a
// leaf: its keys are dynamic, so there's nothing static to recurse into.
func isMap(v cue.Value) bool {
	it, err := v.Fields(cue.Optional(true), cue.Definitions(false))
	if err != nil {
		return true
	}
	return !it.Next()
}

func kindString(v cue.Value) string {
	switch v.IncompleteKind() {
	case cue.StringKind:
		return "string"
	case cue.IntKind:
		return "int"
	case cue.FloatKind, cue.NumberKind:
		return "number"
	case cue.BoolKind:
		return "bool"
	case cue.StructKind:
		return "object"
	case cue.ListKind:
		return "array"
	default:
		return v.IncompleteKind().String()
	}
}

// docOf returns the field's leading comment, joined to a single line.
func docOf(v cue.Value) string {
	var parts []string
	for _, cg := range v.Doc() {
		parts = append(parts, strings.TrimSpace(cg.Text()))
	}
	return strings.TrimSpace(strings.ReplaceAll(strings.Join(parts, " "), "\n", " "))
}
