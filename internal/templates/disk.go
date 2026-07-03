package templates

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/load"

	"github.com/openctl/openctl/internal/log"
	"github.com/openctl/openctl/internal/schema"
	"github.com/openctl/openctl/pkg/protocol"
)

// LoadFromDir scans dir for `*.cue` template files and returns the ones that
// parse. A missing directory is not an error (returns nil) — the controller
// simply serves only the compiled-in starters. A single malformed template
// is logged and skipped rather than failing the whole load, so one typo in a
// user file can't take down the controller's TemplateService.
func LoadFromDir(dir string) ([]*Template, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read templates dir %s: %w", dir, err)
	}
	// Stable order so registry display order is deterministic across restarts.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var out []*Template
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".cue" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		t, err := loadCUETemplate(path)
		if err != nil {
			log.Info("templates: skipping %s: %v", path, err)
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

// cueParamDef mirrors ParamDef for JSON extraction from the CUE
// `template.parameters` list. Field names match the CUE-side keys.
type cueParamDef struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Default     any      `json:"default"`
	Required    bool     `json:"required"`
	Enum        []string `json:"enum"`
	OptionsKind string   `json:"optionsKind"`
}

// cueTemplateMeta is the concrete `template:` metadata block a user CUE
// template must declare. It is JSON-extracted before any params are filled,
// so it must not reference `params`.
type cueTemplateMeta struct {
	Name        string        `json:"name"`
	DisplayName string        `json:"displayName"`
	Description string        `json:"description"`
	APIVersion  string        `json:"apiVersion"`
	Kind        string        `json:"kind"`
	Parameters  []cueParamDef `json:"parameters"`
}

// loadCUETemplate parses a single user template file. The file must declare a
// concrete `template:` block (metadata + parameter list) and a `resource:`
// block that references a `params:` struct for the user-fillable values. The
// returned Template's Render closure re-parses the file and fills the params
// on each call, which keeps rendering concurrency-safe (no shared cue.Value /
// cue.Context) at the cost of a re-parse — fine for a user-initiated action.
func loadCUETemplate(path string) (*Template, error) {
	v, err := buildCUEFile(path)
	if err != nil {
		return nil, err
	}
	tmplVal := v.LookupPath(cue.ParsePath("template"))
	if !tmplVal.Exists() {
		return nil, fmt.Errorf("missing top-level `template:` block")
	}
	data, err := tmplVal.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("`template:` block must be concrete: %w", err)
	}
	var meta cueTemplateMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("decode `template:` block: %w", err)
	}
	if meta.Name == "" {
		return nil, fmt.Errorf("`template.name` is required")
	}
	if !v.LookupPath(cue.ParsePath("resource")).Exists() {
		return nil, fmt.Errorf("missing top-level `resource:` block")
	}

	defs := make([]ParamDef, 0, len(meta.Parameters))
	for _, p := range meta.Parameters {
		if p.Type == "" {
			p.Type = "string"
		}
		// cueParamDef and ParamDef have identical field layout; the JSON
		// tags (present only on cueParamDef) are ignored by the conversion.
		defs = append(defs, ParamDef(p))
	}

	displayName := meta.DisplayName
	if displayName == "" {
		displayName = meta.Name
	}

	return &Template{
		Name:        meta.Name,
		DisplayName: displayName,
		Description: meta.Description,
		APIVersion:  meta.APIVersion,
		Kind:        meta.Kind,
		Parameters:  defs,
		Render: func(params map[string]any) (*protocol.Resource, error) {
			return renderCUETemplate(path, defs, params)
		},
	}, nil
}

// renderCUETemplate fills the user's parameter values (layered over the
// declared defaults) into the file's `params:` field and extracts the
// concrete `resource:` value as a manifest.
func renderCUETemplate(path string, defs []ParamDef, params map[string]any) (*protocol.Resource, error) {
	v, err := buildCUEFile(path)
	if err != nil {
		return nil, err
	}
	// Defaults first, then user-provided values win.
	filled := make(map[string]any, len(defs))
	for _, d := range defs {
		if d.Default != nil {
			filled[d.Name] = d.Default
		}
	}
	maps.Copy(filled, params)

	ctx := v.Context()
	paramsVal := ctx.Encode(filled)
	if err := paramsVal.Err(); err != nil {
		return nil, fmt.Errorf("encode params: %w", err)
	}
	unified := v.FillPath(cue.ParsePath("params"), paramsVal)
	if err := unified.Err(); err != nil {
		return nil, fmt.Errorf("fill params: %w", err)
	}
	resVal := unified.LookupPath(cue.ParsePath("resource"))
	if err := resVal.Validate(cue.Concrete(true)); err != nil {
		return nil, fmt.Errorf("rendered `resource:` is not concrete (check parameters): %w", err)
	}
	return cueValueToResource(resVal)
}

// buildCUEFile loads a CUE file with the embedded provider schemas overlaid
// (so templates can import/validate against them), without the whole-file
// concreteness check — `params`/`resource` are intentionally incomplete
// until Render fills them.
func buildCUEFile(path string) (cue.Value, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return cue.Value{}, fmt.Errorf("resolve path: %w", err)
	}
	dir := filepath.Dir(absPath)
	insts := load.Instances([]string{filepath.Base(absPath)}, &load.Config{
		Dir:     dir,
		Overlay: schema.GetOverlay(dir),
	})
	if len(insts) == 0 {
		return cue.Value{}, fmt.Errorf("no CUE instances found")
	}
	if insts[0].Err != nil {
		return cue.Value{}, fmt.Errorf("load CUE: %w", insts[0].Err)
	}
	v := cuecontext.New().BuildInstance(insts[0])
	if err := v.Err(); err != nil {
		return cue.Value{}, fmt.Errorf("build CUE: %w", err)
	}
	return v, nil
}

// cueValueToResource marshals a concrete CUE value into a protocol.Resource,
// mirroring internal/manifest's toResource (kept local to avoid an import
// cycle and to require metadata.name here specifically).
func cueValueToResource(v cue.Value) (*protocol.Resource, error) {
	data, err := v.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var r protocol.Resource
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if r.Metadata.Name == "" {
		return nil, fmt.Errorf("rendered resource is missing metadata.name")
	}
	return &r, nil
}
