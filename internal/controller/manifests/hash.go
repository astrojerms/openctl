package manifests

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/openctl/openctl/pkg/protocol"
)

// Hash returns the sha256 (hex) of a canonical encoding of the manifest's
// apply input — apiVersion, kind, metadata.name, metadata.labels, spec.
// Used as the verifying-trace cache key: two manifests with the same Hash
// produce the same Apply outcome, modulo external state changes that the
// dispatcher can't observe.
//
// Excluded:
//   - metadata.annotations — runtime flags (allow-destructive, etc.) that
//     don't change what the resource SHOULD look like.
//   - status — observed state, not input.
//
// Canonicalization: spec is normalized via sort-keyed JSON so map ordering
// doesn't perturb the hash.
func Hash(r *protocol.Resource) string {
	if r == nil {
		return ""
	}
	canon := canonical(map[string]any{
		"apiVersion": r.APIVersion,
		"kind":       r.Kind,
		"name":       r.Metadata.Name,
		"labels":     normalize(r.Metadata.Labels),
		"spec":       normalize(r.Spec),
	})
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:])
}

// canonical returns a sort-keyed JSON encoding of v. Maps are recursively
// sorted; arrays preserve order. Used as the hash input so two
// semantically-equal manifests produce the same bytes.
func canonical(v any) []byte {
	out, _ := json.Marshal(normalize(v))
	return out
}

// normalize walks v converting map[string]any (or map[string]string) into a
// stable shape: keys sorted, nested values normalized recursively. Other
// types pass through unchanged. The sorted-keys property is what makes
// json.Marshal deterministic.
func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]kv, 0, len(keys))
		for _, k := range keys {
			out = append(out, kv{K: k, V: normalize(x[k])})
		}
		return out
	case map[string]string:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]kv, 0, len(keys))
		for _, k := range keys {
			out = append(out, kv{K: k, V: x[k]})
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalize(e)
		}
		return out
	default:
		return v
	}
}

// kv is the wire shape for a normalized map entry. Using a slice of these
// instead of map[string]any guarantees json.Marshal emits keys in the order
// we sorted them — Go's standard library sorts map keys for you, but we
// rely on the explicit slice to avoid subtle non-determinism in nested
// types (e.g. map[string]string buried inside an []any).
type kv struct {
	K string `json:"k"`
	V any    `json:"v"`
}
