package reconciler

import (
	"math"
	"reflect"
)

// SpecsEqual compares declared fields against live configuration. Extra live
// fields are ignored, but absent declared fields and differing scalar types
// are not equal. Numeric representations may differ without changing value.
func SpecsEqual(desired, observed map[string]any) bool {
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
			live, present := o[k]
			if !present || !compareNode(v, live) {
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
	av, bv := reflect.ValueOf(a), reflect.ValueOf(b)
	if !av.IsValid() || !bv.IsValid() {
		return !av.IsValid() && !bv.IsValid()
	}
	switch av.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		switch bv.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return av.Int() == bv.Int()
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return av.Int() >= 0 && uint64(av.Int()) == bv.Uint()
		case reflect.Float32, reflect.Float64:
			return floatEqualsInt(bv.Float(), av.Int())
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		switch bv.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return bv.Int() >= 0 && av.Uint() == uint64(bv.Int())
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return av.Uint() == bv.Uint()
		case reflect.Float32, reflect.Float64:
			return floatEqualsUint(bv.Float(), av.Uint())
		}
	case reflect.Float32, reflect.Float64:
		switch bv.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return floatEqualsInt(av.Float(), bv.Int())
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return floatEqualsUint(av.Float(), bv.Uint())
		case reflect.Float32, reflect.Float64:
			return av.Float() == bv.Float()
		}
	}
	return reflect.DeepEqual(a, b)
}

func floatEqualsInt(f float64, n int64) bool {
	return !math.IsNaN(f) && f >= -0x1p63 && f < 0x1p63 && int64(f) == n && float64(n) == f
}

func floatEqualsUint(f float64, n uint64) bool {
	return !math.IsNaN(f) && f >= 0 && f < 0x1p64 && uint64(f) == n && float64(n) == f
}
