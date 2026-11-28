package templates

import (
	"fmt"
	"math"
	"strings"
)

// Starter integers must survive both native int conversion and JSON/Struct's
// float64 transport without truncation or overflow.
func starterInt(p map[string]any, defs []ParamDef, key string, minimum, maximum int64) (int, error) {
	value, present := p[key]
	if !present {
		for _, def := range defs {
			if def.Name == key {
				value = def.Default
				break
			}
		}
	}
	maximum = min(maximum, int64(^uint(0)>>1), 1<<53-1)
	var number int64
	switch n := value.(type) {
	case int:
		number = int64(n)
	case int32:
		number = int64(n)
	case int64:
		number = n
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < float64(minimum) || n > float64(maximum) {
			return 0, fmt.Errorf("parameter %q must be a whole number between %d and %d", key, minimum, maximum)
		}
		number = int64(n)
	default:
		return 0, fmt.Errorf("parameter %q must be an integer", key)
	}
	if number < minimum || number > maximum {
		return 0, fmt.Errorf("parameter %q must be a whole number between %d and %d", key, minimum, maximum)
	}
	return int(number), nil
}

func validateStarterParams(defs []ParamDef, p map[string]any) error {
	if err := validateParams(defs, p); err != nil {
		return err
	}
	for _, def := range defs {
		if def.Required && def.Type == "string" && strings.TrimSpace(getString(p, defs, def.Name)) == "" {
			return fmt.Errorf("parameter %q is required", def.Name)
		}
	}
	return nil
}
