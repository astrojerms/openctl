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
	ctx := cuecontext.New()

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve path: %w", err)
	}

	dir := filepath.Dir(absPath)
	cfg := &load.Config{
		Dir:     dir,
		Overlay: schema.GetOverlay(dir),
	}

	insts := load.Instances([]string{filepath.Base(absPath)}, cfg)
	if len(insts) == 0 {
		return nil, fmt.Errorf("no CUE instances found")
	}
	if insts[0].Err != nil {
		return nil, fmt.Errorf("failed to load CUE: %w", insts[0].Err)
	}

	value := ctx.BuildInstance(insts[0])
	if err := value.Err(); err != nil {
		return nil, fmt.Errorf("CUE build error: %w", err)
	}

	if err := value.Validate(cue.Concrete(true)); err != nil {
		return nil, fmt.Errorf("validation error: %w", err)
	}

	return extractResources(value)
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
