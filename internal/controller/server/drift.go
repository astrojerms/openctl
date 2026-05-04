package server

import (
	"fmt"
	"sort"
	"strings"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

// computeDrift compares a desired spec (from the persisted manifest) against
// an observed spec (from the provider's Get). It implements the "loose
// comparison" rule from CONTROLLER.md: only fields *present in the desired
// spec* are checked. Provider-set defaults that don't appear in the manifest
// are unmanaged and never produce drift.
//
// The walk descends into nested maps and slices. Path entries are
// dot-separated for maps and [i]-suffixed for slice indices, e.g.
// "spec.nodes.workers[0].count".
//
// Number normalization: JSON round-trips integers as float64 and YAML
// produces ints. We coerce both sides to a canonical numeric form before
// comparing so an `int(2)` desired and a `float64(2)` observed don't read
// as drift.
func computeDrift(desired, observed map[string]any) []*apiv1.DriftEntry {
	var out []*apiv1.DriftEntry
	walk("spec", desired, observed, &out)
	// Stable order for deterministic API responses.
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func walk(path string, desired, observed any, out *[]*apiv1.DriftEntry) {
	switch d := desired.(type) {
	case map[string]any:
		o, ok := observed.(map[string]any)
		if !ok {
			*out = append(*out, &apiv1.DriftEntry{
				Path:     path,
				Desired:  renderValue(desired),
				Observed: renderValue(observed),
			})
			return
		}
		// Iterate in stable key order so the (rare) cases where renderValue
		// dumps a whole subtree produce identical output run-to-run.
		keys := make([]string, 0, len(d))
		for k := range d {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			walk(path+"."+k, d[k], o[k], out)
		}

	case []any:
		o, ok := observed.([]any)
		if !ok {
			*out = append(*out, &apiv1.DriftEntry{
				Path:     path,
				Desired:  renderValue(desired),
				Observed: renderValue(observed),
			})
			return
		}
		// Different lengths is a structural difference — surface as a single
		// entry on the path itself so the caller sees the count change.
		if len(d) != len(o) {
			*out = append(*out, &apiv1.DriftEntry{
				Path:     path + ".length",
				Desired:  fmt.Sprintf("%d", len(d)),
				Observed: fmt.Sprintf("%d", len(o)),
			})
			// Continue walking the overlap so per-element drift surfaces too.
		}
		overlap := len(d)
		if len(o) < overlap {
			overlap = len(o)
		}
		for i := 0; i < overlap; i++ {
			walk(fmt.Sprintf("%s[%d]", path, i), d[i], o[i], out)
		}

	default:
		if !valuesEqual(desired, observed) {
			*out = append(*out, &apiv1.DriftEntry{
				Path:     path,
				Desired:  renderValue(desired),
				Observed: renderValue(observed),
			})
		}
	}
}

// valuesEqual compares two scalar values, normalizing numeric types so
// int/float/uint of the same numeric value compare equal. Anything that
// isn't a number falls back to == on the interface.
func valuesEqual(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if af, aok := toFloat(a); aok {
		if bf, bok := toFloat(b); bok {
			return af == bf
		}
	}
	return a == b
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	}
	return 0, false
}

// renderValue produces a short, human-friendly string for a drift entry's
// desired/observed display. Maps and slices fall back to a Go-ish dump —
// we don't try to be JSON-pretty here because in practice the recursive
// walk surfaces leaves, not whole subtrees.
func renderValue(v any) string {
	if v == nil {
		return "<unset>"
	}
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	}
	if f, ok := toFloat(v); ok {
		// Integer-valued floats render without trailing ".0".
		if f == float64(int64(f)) {
			return fmt.Sprintf("%d", int64(f))
		}
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", f), "0"), ".")
	}
	return fmt.Sprintf("%v", v)
}
