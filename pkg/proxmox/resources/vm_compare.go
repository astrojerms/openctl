package resources

import (
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
)

// ComparableVMSpecs projects live configuration onto explicitly managed fields.
// Only creation/routing and unobservable cloud-init execution inputs are
// excluded. Unknown desired fields remain managed, so missing observations drift.
// Neither input is modified.
func ComparableVMSpecs(desired, observed map[string]any) (map[string]any, map[string]any) {
	d, o := compareVMMaps(desired, observed, "")
	return d, o
}

func compareVMMaps(desired, observed map[string]any, path string) (map[string]any, map[string]any) {
	d, o := make(map[string]any, len(desired)), make(map[string]any, len(desired))
	for key, value := range desired {
		if path == "" {
			switch key {
			case "context", "template", "image", "cloudImage", "startOnCreate":
				continue
			}
		}
		if path == "cloudInit" {
			switch key {
			case "password", "packages", "runcmd":
				continue
			}
		}
		child := key
		if path != "" {
			child = path + "." + key
		}
		live, exists := observed[key]
		if m, ok := value.(map[string]any); ok {
			liveMap, _ := live.(map[string]any)
			dm, om := compareVMMaps(m, liveMap, child)
			// An execution-only cloud-init declaration manages no native state.
			if child == "cloudInit" && len(dm) == 0 {
				continue
			}
			d[key] = dm
			if exists {
				o[key] = om
			}
			continue
		}
		if list, ok := value.([]any); ok && (child == "disks" || child == "networks" || child == "hostPCI") {
			liveList, _ := live.([]any)
			dl, ol := compareVMLists(list, liveList, child)
			d[key] = dl
			if exists {
				o[key] = ol
			}
			continue
		}
		if text, ok := value.(string); ok && live == text {
			d[key], o[key] = value, live
			continue
		}
		d[key] = normalizedVMValue(value, child)
		if exists {
			o[key] = normalizedVMValue(live, child)
		}
	}
	return d, o
}

func compareVMLists(desired, observed []any, path string) ([]any, []any) {
	d, o := make([]any, 0, len(desired)), make([]any, 0, len(desired))
	for i, value := range desired {
		m, ok := value.(map[string]any)
		if !ok {
			d = append(d, normalizedVMValue(value, path))
			if i < len(observed) {
				o = append(o, normalizedVMValue(observed[i], path))
			}
			continue
		}
		var live map[string]any
		if path == "hostPCI" {
			if i < len(observed) {
				live, _ = observed[i].(map[string]any)
			}
		} else if name, ok := m["name"].(string); ok {
			for _, candidate := range observed {
				if candidateMap, ok := candidate.(map[string]any); ok && candidateMap["name"] == name {
					live = candidateMap
					break
				}
			}
		}
		dm, om := compareVMMaps(m, live, path)
		d = append(d, dm)
		if live != nil {
			o = append(o, om)
		} else {
			o = append(o, nil)
		}
	}
	return d, o
}

func normalizedVMValue(value any, path string) any {
	switch v := value.(type) {
	case map[string]any:
		m := make(map[string]any, len(v))
		for key, value := range v {
			m[key] = normalizedVMValue(value, path+"."+key)
		}
		return m
	case []any:
		list := make([]any, len(v))
		for i, value := range v {
			list[i] = normalizedVMValue(value, path)
		}
		return list
	case []string:
		list := make([]any, len(v))
		for i, value := range v {
			list[i] = value
		}
		return list
	case string:
		if path == "disks.size" {
			return binaryDiskSize(v)
		}
		if path == "networks.macAddress" {
			return strings.ToLower(v)
		}
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case int32:
		return float64(v)
	case uint:
		return float64(v)
	case uint64:
		return float64(v)
	case float32:
		return float64(v)
	case json.Number:
		if n, err := strconv.ParseFloat(string(v), 64); err == nil {
			return n
		}
	}
	return value
}

// Proxmox disk sizes use binary units even for the G/T spellings. Rational
// arithmetic preserves fractional sizes without float rounding or overflow.
func binaryDiskSize(value string) string {
	s := strings.ToUpper(strings.TrimSpace(value))
	i := 0
	for i < len(s) && ((s[i] >= '0' && s[i] <= '9') || s[i] == '.') {
		i++
	}
	if i == 0 {
		return value
	}
	unit := s[i:]
	power := 0
	switch unit {
	case "", "B":
	case "K", "KB", "KIB":
		power = 1
	case "M", "MB", "MIB":
		power = 2
	case "G", "GB", "GIB":
		power = 3
	case "T", "TB", "TIB":
		power = 4
	case "P", "PB", "PIB":
		power = 5
	default:
		return value
	}
	if whole, err := strconv.ParseUint(s[:i], 10, 64); err == nil && whole <= ^uint64(0)>>uint(10*power) {
		return strconv.FormatUint(whole<<uint(10*power), 10)
	}
	n, ok := new(big.Rat).SetString(s[:i])
	if !ok {
		return value
	}
	multiplier := new(big.Int).Lsh(big.NewInt(1), uint(10*power))
	n.Mul(n, new(big.Rat).SetInt(multiplier))
	return n.RatString()
}
