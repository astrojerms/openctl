package schema

import (
	"sort"
	"sync"
)

// External schemas are CUE definitions supplied at runtime by out-of-process
// provider plugins (via the v2 pluginproto handshake), as opposed to the CUE
// files embedded in the controller binary. They let a third-party provider
// ship its own resource schemas so external kinds validate, list, and surface
// their source through the same SchemaService the built-in kinds use.
//
// A plugin schema is a STANDALONE CUE document defining a top-level definition
// named "#<Kind>" that describes the whole resource (apiVersion, kind,
// metadata, spec, ...) or is otherwise open — it is compiled on its own, with
// no access to openctl's internal CUE module, so it must not import the base
// package. Form-schema generation (the typed UI form) is not yet wired for
// external kinds; the UI falls back to the YAML/Monaco editor for them.

type externalSchema struct {
	apiVersion string
	kind       string
	source     string
}

var (
	extMu      sync.RWMutex
	extSchemas = map[string]externalSchema{} // key: apiVersion + "/" + kind
)

func extKey(apiVersion, kind string) string { return apiVersion + "/" + kind }

// RegisterExternal records (or overwrites) a plugin-supplied CUE schema for
// (apiVersion, kind). Safe for concurrent use. Registering a built-in kind's
// apiVersion+kind is allowed but pointless — SchemaSelector's embedded mapping
// is consulted first during validation, so the embedded schema wins.
func RegisterExternal(apiVersion, kind, source string) {
	extMu.Lock()
	defer extMu.Unlock()
	extSchemas[extKey(apiVersion, kind)] = externalSchema{apiVersion: apiVersion, kind: kind, source: source}
}

// ResetExternal clears all registered external schemas. Test helper.
func ResetExternal() {
	extMu.Lock()
	defer extMu.Unlock()
	extSchemas = map[string]externalSchema{}
}

func lookupExternal(apiVersion, kind string) (externalSchema, bool) {
	extMu.RLock()
	defer extMu.RUnlock()
	s, ok := extSchemas[extKey(apiVersion, kind)]
	return s, ok
}

// externalInfos returns Registry-style Info entries for all registered
// external schemas, sorted by apiVersion then kind for stable output. External
// Infos carry an empty FileName, which is how SourceFor distinguishes them
// from embedded schemas.
func externalInfos() []Info {
	extMu.RLock()
	defer extMu.RUnlock()
	out := make([]Info, 0, len(extSchemas))
	for _, s := range extSchemas {
		out = append(out, Info{
			APIVersion: s.apiVersion,
			Kind:       s.kind,
			Provider:   providerOf(s.apiVersion),
			FileName:   "",
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].APIVersion != out[j].APIVersion {
			return out[i].APIVersion < out[j].APIVersion
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}
