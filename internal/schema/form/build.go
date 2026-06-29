package form

import (
	"fmt"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/load"

	"github.com/openctl/openctl/internal/schema"
)

// BuildForKind loads the embedded CUE schema for (apiVersion, kind) and
// returns a Field tree for the root #Resource definition. Used by
// SchemaService.GetFormSchema. Returns (Field{}, false) when no schema
// is registered — the gRPC handler maps that to NotFound.
//
// The selector logic mirrors schema.Validate's so the form and the
// validator always agree on which CUE def to consult.
func BuildForKind(apiVersion, kind string) (Field, bool, error) {
	pkg, def, ok := schema.SchemaSelector(apiVersion, kind)
	if !ok {
		return Field{}, false, nil
	}

	ctx := cuecontext.New()
	const overlayDir = "/openctl-form"
	cfg := &load.Config{
		Dir:     overlayDir,
		Overlay: schema.GetOverlay(overlayDir),
	}
	insts := load.Instances([]string{"openctl.io/schemas/" + pkg}, cfg)
	if len(insts) == 0 {
		return Field{}, false, fmt.Errorf("no CUE instance for schema package %q", pkg)
	}
	if insts[0].Err != nil {
		return Field{}, false, fmt.Errorf("load schema package %q: %w", pkg, insts[0].Err)
	}
	schemaVal := ctx.BuildInstance(insts[0])
	if err := schemaVal.Err(); err != nil {
		return Field{}, false, fmt.Errorf("build schema: %w", err)
	}
	defVal := schemaVal.LookupPath(cue.ParsePath(def))
	if !defVal.Exists() {
		return Field{}, false, fmt.Errorf("schema %s.%s not found in package", pkg, def)
	}

	f, err := FromValue(defVal)
	if err != nil {
		return Field{}, false, err
	}
	return f, true, nil
}
