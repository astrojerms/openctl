package reconciler

import "fmt"

// specsEqual reports whether two spec trees are structurally equivalent.
// Mirrors the loose-comparison rules used by server/drift.computeDrift —
// only fields present in `desired` are compared, so provider-set defaults
// in `observed` don't read as drift; numeric types are unified so int(2)
// matches float64(2.0). Kept here as a yes/no helper so the reconciler
// doesn't depend on the server package (and its proto types).
func specsEqual(desired, observed map[string]any) bool {
	return compareNode(desired, observed)
}

func compareNode(desired, observed any) bool {
	switch d := desired.(type) {
	case map[string]any:
		o, ok := observed.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range d {
			if !compareNode(v, o[k]) {
				return false
			}
		}
		return true
	case []any:
		o, ok := observed.([]any)
		if !ok || len(d) != len(o) {
			return false
		}
		for i := range d {
			if !compareNode(d[i], o[i]) {
				return false
			}
		}
		return true
	default:
		return scalarEqual(desired, observed)
	}
}

func scalarEqual(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if af, aok := toFloat(a); aok {
		if bf, bok := toFloat(b); bok {
			return af == bf
		}
	}
	// Fall back to string-formatted comparison for unknown types; cheaper
	// than reflect.DeepEqual and good enough for spec scalars.
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
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
