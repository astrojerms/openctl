// Package refs implements the ResourceRef spec-level primitive from
// Phase 8. A ResourceRef is an in-manifest placeholder that names
// another resource (by apiVersion + kind + name) and optionally a
// dotted field-path within that resource's status. The Resolver walks
// a manifest's spec tree pre-Apply, calls provider.Get on each
// referenced resource, and substitutes the resolved value in place.
//
// Wire shape: any object matching {"$ref": {apiVersion, kind, name,
// field?}} is treated as a ref. Bare (no field) resolves to the whole
// resource; with field, resolves to that dotted path (e.g.
// "status.nodeToken", "status.ip"). Unresolvable refs surface as
// errors so the caller can retry after the referenced resource is
// ready — no silent substitution to zero-values.
//
// Concrete use case (Phase 8 step 2+): a K3sNode-worker manifest
// says its join token is {"$ref": {kind: "K3sNode", name: "cp-0",
// field: "status.nodeToken"}}. The scheduler resolves this when the
// worker's Apply fires — running cp-0 first if not yet complete
// (that's a future scheduler concern; step 1 just does the resolver
// and errors on missing refs).
package refs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/openctl/openctl/pkg/protocol"
)

// RefMarker is the top-level key that identifies a ResourceRef inside
// a spec value. Chosen to be JSON-syntactically valid but visually
// distinct from user-authored fields.
const RefMarker = "$ref"

// Ref is the parsed form of a ResourceRef found inside a spec.
type Ref struct {
	APIVersion string
	Kind       string
	Name       string
	// Field, when non-empty, is a dot-separated walk into the
	// resolved resource (e.g. "status.nodeToken"). Empty resolves
	// to the whole resource — useful for callers that want to
	// consume multiple fields.
	Field string
}

// Getter is the minimal interface Resolver depends on. Provider
// registry implements it in production; tests can pass a fake.
type Getter interface {
	Get(ctx context.Context, apiVersion, kind, name string) (*protocol.Resource, error)
}

// Resolver walks a spec tree and substitutes ResourceRefs with the
// resolved values by calling Get on the configured Getter.
type Resolver struct {
	get Getter
}

// New constructs a Resolver bound to the given Getter.
func New(g Getter) *Resolver {
	return &Resolver{get: g}
}

// Resolve returns a copy of the input map with every {"$ref": ...}
// entry replaced by the value referenced. Nested arrays and objects
// are traversed. Returns an error on any unresolvable ref — refs
// pointing at missing resources, malformed shape, or a field-path
// that doesn't exist in the resolved resource.
func (r *Resolver) Resolve(ctx context.Context, in map[string]any) (map[string]any, error) {
	if in == nil {
		return nil, nil
	}
	out, err := r.walk(ctx, in)
	if err != nil {
		return nil, err
	}
	m, ok := out.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("resolver: top-level result is not a map (got %T)", out)
	}
	return m, nil
}

// walk is the recursive traversal. Returns a fresh value tree with
// substitutions applied — input is never mutated.
func (r *Resolver) walk(ctx context.Context, v any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		// A single-key map with key == RefMarker is a ref. Resolve
		// it in place; the caller's iteration sees the substituted
		// value, not a nested $ref marker.
		if ref, isRef, err := extractRef(x); err != nil {
			return nil, err
		} else if isRef {
			return r.resolveRef(ctx, ref)
		}
		out := make(map[string]any, len(x))
		for k, v2 := range x {
			resolved, err := r.walk(ctx, v2)
			if err != nil {
				return nil, err
			}
			out[k] = resolved
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			resolved, err := r.walk(ctx, item)
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	default:
		return v, nil
	}
}

// extractRef inspects a map for the RefMarker key. Returns
// (ref, true, nil) when the map is a well-formed ref, (nil, false,
// nil) when the map isn't a ref, and an error when the map contains
// a RefMarker key but is malformed (missing required fields, wrong
// types) — better to fail loud than substitute nothing.
func extractRef(m map[string]any) (*Ref, bool, error) {
	raw, present := m[RefMarker]
	if !present {
		return nil, false, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, true, fmt.Errorf("ref: %s must be an object (got %T)", RefMarker, raw)
	}
	ref := &Ref{}
	if v, ok := obj["apiVersion"].(string); ok {
		ref.APIVersion = v
	}
	if v, ok := obj["kind"].(string); ok {
		ref.Kind = v
	}
	if v, ok := obj["name"].(string); ok {
		ref.Name = v
	}
	if v, ok := obj["field"].(string); ok {
		ref.Field = v
	}
	if ref.APIVersion == "" || ref.Kind == "" || ref.Name == "" {
		return nil, true, fmt.Errorf("ref: apiVersion, kind, and name are required (got %+v)", ref)
	}
	return ref, true, nil
}

// resolveRef calls Get on the referenced resource and, if field is
// set, walks the dotted path into it. Missing resources become
// FailedPrecondition-shaped errors so schedulers can distinguish
// "ref target isn't ready yet, retry after it's applied" from real
// controller / provider failures.
func (r *Resolver) resolveRef(ctx context.Context, ref *Ref) (any, error) {
	if r.get == nil {
		return nil, errors.New("ref: no Getter configured on Resolver")
	}
	resource, err := r.get.Get(ctx, ref.APIVersion, ref.Kind, ref.Name)
	if err != nil {
		return nil, fmt.Errorf("ref %s/%s/%s: %w", ref.APIVersion, ref.Kind, ref.Name, err)
	}
	if ref.Field == "" {
		// Whole-resource substitution: return a plain map so the
		// substituted value stays JSON-shaped. Metadata + spec +
		// status flattened, matching what json.Marshal of the
		// Resource would produce.
		return map[string]any{
			"apiVersion": resource.APIVersion,
			"kind":       resource.Kind,
			"metadata":   map[string]any{"name": resource.Metadata.Name},
			"spec":       resource.Spec,
			"status":     resource.Status,
		}, nil
	}
	value, err := walkPath(resource, ref.Field)
	if err != nil {
		return nil, fmt.Errorf("ref %s/%s/%s field %q: %w",
			ref.APIVersion, ref.Kind, ref.Name, ref.Field, err)
	}
	return value, nil
}

// walkPath resolves a dotted path like "status.nodeToken" against a
// Resource. Only status.* and spec.* paths are supported today —
// other roots would need per-field handling and aren't needed for the
// Phase 8 use cases.
func walkPath(r *protocol.Resource, path string) (any, error) {
	parts := strings.Split(path, ".")
	if len(parts) == 0 {
		return nil, errors.New("empty path")
	}
	var current any
	switch parts[0] {
	case "status":
		if r.Status == nil {
			return nil, fmt.Errorf("resource has no status")
		}
		current = r.Status
	case "spec":
		if r.Spec == nil {
			return nil, fmt.Errorf("resource has no spec")
		}
		current = r.Spec
	default:
		return nil, fmt.Errorf("path must start with 'status' or 'spec' (got %q)", parts[0])
	}
	for _, seg := range parts[1:] {
		obj, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("path segment %q not traversable (got %T)", seg, current)
		}
		v, present := obj[seg]
		if !present {
			return nil, fmt.Errorf("path segment %q missing", seg)
		}
		current = v
	}
	return current, nil
}
