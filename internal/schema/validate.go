package schema

import (
	"encoding/json"
	"fmt"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	cueerrors "cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/load"

	"github.com/openctl/openctl/pkg/protocol"
)

// ValidationError is one path-attributed schema violation. Path is
// dot-separated relative to the top of the resource (e.g. "spec.cpu.cores");
// Message is the CUE error text. Emitted by ValidateStructured so callers
// can attach errors to individual form fields instead of dumping the whole
// blob in a bottom panel.
type ValidationError struct {
	Path    string
	Message string
}

// Validate checks a resource against its CUE schema. Returns nil if the
// resource matches the schema for its apiVersion+kind, or if no schema is
// registered for that apiVersion+kind (best-effort: unknown resource types
// pass through unvalidated rather than blocking unknown providers).
//
// Both embedded (built-in) and external (plugin-supplied) schemas are
// consulted, embedded first. The CLI and controller both use this function so
// they validate the same way.
func Validate(r *protocol.Resource) error {
	if err := checkResourceMeta(r); err != nil {
		return err
	}
	ctx := cuecontext.New()
	defVal, label, ok, err := schemaDefValue(ctx, r.APIVersion, r.Kind)
	if err != nil {
		return err
	}
	if !ok {
		return nil // no schema — pass through
	}
	resourceVal, err := compileResource(ctx, r)
	if err != nil {
		return err
	}
	unified := defVal.Unify(resourceVal)
	if err := unified.Validate(cue.Concrete(true)); err != nil {
		return fmt.Errorf("does not match schema %s: %w", label, err)
	}
	return nil
}

// checkResourceMeta rejects a nil resource or one missing apiVersion/kind.
func checkResourceMeta(r *protocol.Resource) error {
	if r == nil {
		return fmt.Errorf("resource is nil")
	}
	if r.APIVersion == "" || r.Kind == "" {
		return fmt.Errorf("resource missing apiVersion or kind")
	}
	return nil
}

// compileResource marshals the resource to JSON and compiles it to a CUE value.
func compileResource(ctx *cue.Context, r *protocol.Resource) (cue.Value, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return cue.Value{}, fmt.Errorf("marshal resource: %w", err)
	}
	v := ctx.CompileBytes(data)
	if err := v.Err(); err != nil {
		return cue.Value{}, fmt.Errorf("parse resource: %w", err)
	}
	return v, nil
}

// schemaDefValue resolves the CUE definition to validate a resource against.
// It consults the embedded built-in mapping first, then the runtime external
// registry. Returns (def, label, true, nil) when a schema is found, where
// label identifies the schema for error messages; (_, _, false, nil) when no
// schema is registered (caller passes through); or a non-nil error only for
// schema-loading failures.
func schemaDefValue(ctx *cue.Context, apiVersion, kind string) (cue.Value, string, bool, error) {
	if pkg, def, ok := SchemaSelector(apiVersion, kind); ok {
		const overlayDir = "/openctl-validate"
		cfg := &load.Config{Dir: overlayDir, Overlay: GetOverlay(overlayDir)}
		insts := load.Instances([]string{"openctl.io/schemas/" + pkg}, cfg)
		if len(insts) == 0 {
			return cue.Value{}, "", false, fmt.Errorf("no CUE instance for schema package %q", pkg)
		}
		if insts[0].Err != nil {
			return cue.Value{}, "", false, fmt.Errorf("load schema package %q: %w", pkg, insts[0].Err)
		}
		schemaVal := ctx.BuildInstance(insts[0])
		if err := schemaVal.Err(); err != nil {
			return cue.Value{}, "", false, fmt.Errorf("build schema: %w", err)
		}
		defVal := schemaVal.LookupPath(cue.ParsePath(def))
		if !defVal.Exists() {
			return cue.Value{}, "", false, fmt.Errorf("schema %s.%s not found in package", pkg, def)
		}
		return defVal, pkg + "." + def, true, nil
	}

	if s, ok := lookupExternal(apiVersion, kind); ok {
		schemaVal := ctx.CompileString(s.source)
		if err := schemaVal.Err(); err != nil {
			return cue.Value{}, "", false, fmt.Errorf("compile external schema for %s/%s: %w", apiVersion, kind, err)
		}
		defName := "#" + kind
		defVal := schemaVal.LookupPath(cue.ParsePath(defName))
		if !defVal.Exists() {
			return cue.Value{}, "", false, fmt.Errorf("external schema for %s/%s does not define %s", apiVersion, kind, defName)
		}
		return defVal, apiVersion + " " + defName, true, nil
	}

	return cue.Value{}, "", false, nil
}

// ValidateStructured is like Validate but returns the CUE error list
// broken out into (path, message) pairs. Empty slice + nil error means
// the resource is valid. A non-nil error is returned only for schema-
// loading failures (missing schema pkg, unresolvable module, etc.);
// validation failures come back as a populated slice, not an error.
func ValidateStructured(r *protocol.Resource) ([]ValidationError, error) {
	if err := checkResourceMeta(r); err != nil {
		return nil, err
	}
	ctx := cuecontext.New()
	defVal, _, ok, err := schemaDefValue(ctx, r.APIVersion, r.Kind)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	resourceVal, err := compileResource(ctx, r)
	if err != nil {
		return nil, err
	}
	unified := defVal.Unify(resourceVal)
	if err := unified.Validate(cue.Concrete(true)); err != nil {
		out := make([]ValidationError, 0)
		for _, e := range cueerrors.Errors(err) {
			path := strings.Join(e.Path(), ".")
			// CUE surfaces messages via Msg() as (format, args) pairs.
			// Errors don't always carry a fully-formatted string on their
			// own; format with the default cueerrors format for stability.
			out = append(out, ValidationError{
				Path:    path,
				Message: cueerrors.Details(e, nil),
			})
		}
		return out, nil
	}
	return nil, nil
}

// SchemaSelector returns the embedded-schema (package, definition) for a
// given apiVersion+kind. Returns ok=false when no schema is registered.
// Exported so internal/schema/form can pick the same CUE def Validate
// would — keeps the form bridge and the validator in lockstep.
//
// The mapping is intentionally explicit. Adding a new resource kind
// requires updating both the .cue file under internal/schema/schemas/ and
// this map.
func SchemaSelector(apiVersion, kind string) (pkg, def string, ok bool) {
	provider := providerOf(apiVersion)
	switch provider {
	case "proxmox":
		if kind == "VirtualMachine" {
			return "proxmox", "#VirtualMachine", true
		}
		if kind == "ProxmoxNode" {
			return "proxmox", "#ProxmoxNode", true
		}
	case "k3s":
		if kind == "Cluster" {
			return "k3s", "#Cluster", true
		}
		if kind == "K3sNode" {
			return "k3s", "#K3sNode", true
		}
		if kind == "AgentInstall" {
			return "k3s", "#AgentInstall", true
		}
	}
	return "", "", false
}

func providerOf(apiVersion string) string {
	dot := strings.IndexByte(apiVersion, '.')
	if dot <= 0 {
		return ""
	}
	return apiVersion[:dot]
}
