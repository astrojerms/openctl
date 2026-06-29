package schema

import (
	"encoding/json"
	"fmt"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/load"

	"github.com/openctl/openctl/pkg/protocol"
)

// Validate checks a resource against its embedded CUE schema. Returns nil if
// the resource matches the schema for its apiVersion+kind, or if no schema
// is registered for that apiVersion+kind (best-effort: unknown resource
// types pass through unvalidated rather than blocking unknown providers).
//
// The CUE schema is the same one embedded in the controller binary, so the
// CLI and controller both use this function to validate the same way.
func Validate(r *protocol.Resource) error {
	if r == nil {
		return fmt.Errorf("resource is nil")
	}
	if r.APIVersion == "" || r.Kind == "" {
		return fmt.Errorf("resource missing apiVersion or kind")
	}

	pkg, def, ok := SchemaSelector(r.APIVersion, r.Kind)
	if !ok {
		// No embedded schema for this apiVersion+kind — pass through.
		return nil
	}

	ctx := cuecontext.New()
	const overlayDir = "/openctl-validate"
	cfg := &load.Config{
		Dir:     overlayDir,
		Overlay: GetOverlay(overlayDir),
	}
	insts := load.Instances([]string{"openctl.io/schemas/" + pkg}, cfg)
	if len(insts) == 0 {
		return fmt.Errorf("no CUE instance for schema package %q", pkg)
	}
	if insts[0].Err != nil {
		return fmt.Errorf("load schema package %q: %w", pkg, insts[0].Err)
	}

	schemaVal := ctx.BuildInstance(insts[0])
	if err := schemaVal.Err(); err != nil {
		return fmt.Errorf("build schema: %w", err)
	}
	defVal := schemaVal.LookupPath(cue.ParsePath(def))
	if !defVal.Exists() {
		return fmt.Errorf("schema %s.%s not found in package", pkg, def)
	}

	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal resource: %w", err)
	}
	resourceVal := ctx.CompileBytes(data)
	if err := resourceVal.Err(); err != nil {
		return fmt.Errorf("parse resource: %w", err)
	}

	unified := defVal.Unify(resourceVal)
	if err := unified.Validate(cue.Concrete(true)); err != nil {
		return fmt.Errorf("does not match schema %s.%s: %w", pkg, def, err)
	}
	return nil
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
	case "k3s":
		if kind == "Cluster" {
			return "k3s", "#Cluster", true
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
