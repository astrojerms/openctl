// Package secrets implements the $secret spec-level primitive: an
// in-manifest placeholder that names a secret provider and a
// provider-specific key, resolved at Apply time to the real secret value.
//
// It mirrors the refs package (the $ref primitive) deliberately: the same
// recursive spec walk, the same single-key-marker detection, the same
// "resolve into a transient copy; the persisted manifest keeps the marker"
// discipline. That last property is the whole point — a resolved secret is
// handed to provider.Apply but never written to the operations store, the
// on-disk manifest mirror, or git. The manifest carries {"$secret": {...}},
// not "hunter2".
//
// Wire shape (canonical): {"$secret": {"provider": "<name>", "key": "<key>"}}.
// Two built-in providers get terse sugar so the common case stays short:
//
//	{"$secret": {"file": "db.pw"}}   → provider "file", key "db.pw"
//	{"$secret": {"env":  "DB_PW"}}   → provider "env",  key "DB_PW"
//
// Sources are a pluggable registry of named SecretProviders. v1 registers
// "file" and "env"; Vault / cloud secret managers (configured backends) and
// external secret-provider plugins register the same way later, with no
// change to the marker, the resolver, or the redaction guarantee — resolution
// is the only thing that varies by provider.
package secrets

import (
	"context"
	"errors"
	"fmt"
)

// SecretMarker is the top-level key identifying a $secret placeholder inside a
// spec value. Parallels refs.RefMarker.
const SecretMarker = "$secret"

// builtinFileProvider and builtinEnvProvider are the names of the two
// providers that get sugar in the marker ({file: ...} / {env: ...}).
const (
	builtinFileProvider = "file"
	builtinEnvProvider  = "env"
)

// SecretProvider resolves a provider-specific key to a secret value. The value
// is used transiently (handed to provider.Apply) and never persisted. This is
// the only interface a new backend (Vault, a cloud secret manager, an external
// plugin) needs to implement.
type SecretProvider interface {
	// Name is the provider identifier referenced by a $secret marker's
	// "provider" field (e.g. "file", "env", "vault").
	Name() string
	// Resolve returns the secret value for the given provider-specific key,
	// or an error if it can't be found or read.
	Resolve(ctx context.Context, key string) (string, error)
}

// SecretRef is the parsed form of a $secret marker.
type SecretRef struct {
	Provider string
	Key      string
}

// Registry holds SecretProviders keyed by name.
type Registry struct {
	providers map[string]SecretProvider
}

// NewRegistry returns an empty registry. Callers register the built-in file
// and env providers (see RegisterBuiltins) plus any configured backends.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]SecretProvider)}
}

// Register adds a provider under its Name. A later registration with the same
// name overrides an earlier one.
func (r *Registry) Register(p SecretProvider) {
	r.providers[p.Name()] = p
}

// lookup returns the provider registered under name.
func (r *Registry) lookup(name string) (SecretProvider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// Resolver walks a spec tree and substitutes $secret markers with the resolved
// secret value by dispatching to the named provider in the Registry.
type Resolver struct {
	reg *Registry
}

// New constructs a Resolver bound to the given Registry.
func New(reg *Registry) *Resolver {
	return &Resolver{reg: reg}
}

// Resolve returns a copy of the input map with every {"$secret": ...} entry
// replaced by its resolved value. Nested arrays and objects are traversed;
// input is never mutated. Returns an error on any unresolvable secret
// (unknown provider, missing key) so the caller fails loud rather than
// applying with an empty value.
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
		return nil, fmt.Errorf("secrets: top-level result is not a map (got %T)", out)
	}
	return m, nil
}

// walk is the recursive traversal, mirroring refs.Resolver.walk. Returns a
// fresh value tree with substitutions applied.
func (r *Resolver) walk(ctx context.Context, v any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		if ref, isSecret, err := extractSecret(x); err != nil {
			return nil, err
		} else if isSecret {
			return r.resolveSecret(ctx, ref)
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

// extractSecret inspects a map for the SecretMarker key. Returns
// (ref, true, nil) for a well-formed $secret, (nil, false, nil) when the map
// isn't a secret, and an error when the map has a SecretMarker key but is
// malformed. Accepts the canonical {provider, key} form and the built-in
// sugar {file: ...} / {env: ...}.
func extractSecret(m map[string]any) (*SecretRef, bool, error) {
	raw, present := m[SecretMarker]
	if !present {
		return nil, false, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, true, fmt.Errorf("secret: %s must be an object (got %T)", SecretMarker, raw)
	}

	// Built-in sugar: {file: "x"} / {env: "X"} desugar to a provider+key.
	if v, ok := stringField(obj, builtinFileProvider); ok {
		return &SecretRef{Provider: builtinFileProvider, Key: v}, true, nil
	}
	if v, ok := stringField(obj, builtinEnvProvider); ok {
		return &SecretRef{Provider: builtinEnvProvider, Key: v}, true, nil
	}

	// Canonical form: {provider, key}.
	ref := &SecretRef{}
	if v, ok := obj["provider"].(string); ok {
		ref.Provider = v
	}
	if v, ok := obj["key"].(string); ok {
		ref.Key = v
	}
	if ref.Provider == "" || ref.Key == "" {
		return nil, true, fmt.Errorf("secret: needs {provider, key} or a built-in {file|env} sugar (got %+v)", obj)
	}
	return ref, true, nil
}

// stringField returns (value, true) when key holds a non-empty string.
func stringField(obj map[string]any, key string) (string, bool) {
	if v, ok := obj[key].(string); ok && v != "" {
		return v, true
	}
	return "", false
}

// resolveSecret dispatches to the named provider and returns the secret value.
func (r *Resolver) resolveSecret(ctx context.Context, ref *SecretRef) (any, error) {
	if r.reg == nil {
		return nil, errors.New("secret: no Registry configured on Resolver")
	}
	p, ok := r.reg.lookup(ref.Provider)
	if !ok {
		return nil, fmt.Errorf("secret: unknown provider %q", ref.Provider)
	}
	val, err := p.Resolve(ctx, ref.Key)
	if err != nil {
		return nil, fmt.Errorf("secret %s:%s: %w", ref.Provider, ref.Key, err)
	}
	return val, nil
}

// HasSecrets reports whether the spec contains any $secret marker, without
// resolving. Cheap pre-check so the dispatcher can skip the resolve pass (and
// the provider registry dependency) for the common secret-free manifest.
func HasSecrets(spec map[string]any) bool {
	var found bool
	var visit func(v any)
	visit = func(v any) {
		if found {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			if _, present := x[SecretMarker]; present {
				found = true
				return
			}
			for _, v2 := range x {
				visit(v2)
			}
		case []any:
			for _, item := range x {
				visit(item)
			}
		}
	}
	visit(spec)
	return found
}
