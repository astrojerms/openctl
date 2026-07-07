package manifest

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/load"

	"github.com/openctl/openctl/internal/schema"
	"github.com/openctl/openctl/pkg/protocol"
)

// LoadCUE loads resources from a CUE file.
func LoadCUE(path string) ([]*protocol.Resource, error) {
	return LoadCUEWithValues(path, nil)
}

// LoadCUEWithValues loads resources from a CUE manifest, unifying it with zero
// or more values files first (the openctl analog of Terraform's -var-file).
// Each values file is built as a CUE value and unified with the manifest, so a
// manifest can leave fields abstract (e.g. `spec: cpu: cores: int`) or with
// defaults and a values file fills or overrides them
// (`spec: cpu: cores: 4`). With no values files this is exactly LoadCUE.
//
// Unification is CUE's own: same-path fields combine, a concrete value in the
// values file satisfies an abstract constraint in the manifest, and a conflict
// (two different concrete values) surfaces as a validation error rather than a
// silent last-writer-wins.
func LoadCUEWithValues(path string, valuePaths []string) ([]*protocol.Resource, error) {
	ctx := cuecontext.New()

	value, err := buildValue(ctx, path)
	if err != nil {
		return nil, err
	}
	for _, vp := range valuePaths {
		vv, err := buildValue(ctx, vp)
		if err != nil {
			return nil, fmt.Errorf("values file %s: %w", vp, err)
		}
		value = value.Unify(vv)
	}
	if err := value.Err(); err != nil {
		return nil, fmt.Errorf("CUE build error: %w", err)
	}

	if err := value.Validate(cue.Concrete(true)); err != nil {
		return nil, fmt.Errorf("validation error: %w", err)
	}

	return extractResources(value)
}

// buildValue loads a single CUE file into a cue.Value, with the embedded
// openctl schemas available on the overlay so a manifest can import them.
func buildValue(ctx *cue.Context, path string) (cue.Value, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return cue.Value{}, fmt.Errorf("failed to resolve path: %w", err)
	}
	dir := filepath.Dir(absPath)
	cfg := &load.Config{
		Dir:     dir,
		Overlay: schema.GetOverlay(dir),
	}
	insts := load.Instances([]string{filepath.Base(absPath)}, cfg)
	if len(insts) == 0 {
		return cue.Value{}, fmt.Errorf("no CUE instances found")
	}
	if insts[0].Err != nil {
		return cue.Value{}, fmt.Errorf("failed to load CUE: %w", insts[0].Err)
	}
	v := ctx.BuildInstance(insts[0])
	if err := v.Err(); err != nil {
		return cue.Value{}, fmt.Errorf("CUE build error: %w", err)
	}
	return v, nil
}

func extractResources(v cue.Value) ([]*protocol.Resource, error) {
	var resources []*protocol.Resource

	iter, _ := v.Fields()
	for iter.Next() {
		fv := iter.Value()
		if hasResourceFields(fv) {
			r, err := toResource(fv)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", iter.Selector().String(), err)
			}
			resources = append(resources, r)
		}
	}

	if len(resources) == 0 {
		// Try as single resource
		if hasResourceFields(v) {
			r, err := toResource(v)
			if err != nil {
				return nil, err
			}
			return []*protocol.Resource{r}, nil
		}
	}

	return resources, nil
}

func hasResourceFields(v cue.Value) bool {
	return v.LookupPath(cue.ParsePath("apiVersion")).Exists() &&
		v.LookupPath(cue.ParsePath("kind")).Exists()
}

func toResource(v cue.Value) (*protocol.Resource, error) {
	data, err := v.MarshalJSON()
	if err != nil {
		return nil, err
	}

	var r protocol.Resource
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}

	if r.Metadata.Name == "" {
		return nil, fmt.Errorf("missing metadata.name")
	}

	return &r, nil
}
