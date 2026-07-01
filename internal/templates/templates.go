// Package templates provides parameterized starters — a template
// declares a small set of user-fillable parameters and knows how to
// render itself into a full Resource manifest. The UI surfaces
// templates as one-click "+ New Ubuntu VM" affordances that hide the
// long tail of Proxmox / k3s knobs behind sensible defaults.
//
// MVP: templates are Go-defined and compiled in. A future extension
// can load user-authored CUE templates from ~/.openctl/templates/;
// the RPC surface (List/Get/Render) doesn't care which flavor served
// the request.
package templates

import (
	"fmt"
	"sort"

	"github.com/openctl/openctl/pkg/protocol"
)

// Template is one parameterized starter. Name is the wire-level id
// (kebab-case, unique). Render takes the user's parameter values and
// produces a Resource ready for the normal Apply pipeline.
type Template struct {
	Name        string
	DisplayName string
	Description string
	// APIVersion + Kind of what this template produces. Surfaces in
	// ListTemplates so the UI can group templates by target kind.
	APIVersion string
	Kind       string
	Parameters []ParamDef
	// Render assembles the Resource from parameter values. Should
	// return an error on missing required params or bad types.
	Render func(params map[string]any) (*protocol.Resource, error)
}

// ParamDef describes one user-fillable parameter. Mirrors the shape
// of internal/schema/form.Field so the UI can render templates using
// the same FormField component it uses for editing manifests.
type ParamDef struct {
	Name        string
	Type        string // "string" | "int" | "bool"
	Description string
	Default     any
	Required    bool
	// Enum, when non-empty, constrains a string parameter to the
	// listed values — renders as a select dropdown.
	Enum []string
	// OptionsKind, when non-empty, tells the UI to populate a
	// dropdown from resources of that kind (same mechanism as CUE
	// @options attribute on schema fields).
	OptionsKind string
}

// Registry holds the built-in template set. Simple slice-backed;
// templates.All() returns them sorted by DisplayName.
type Registry struct {
	byName map[string]*Template
	order  []string
}

// NewRegistry constructs a registry from the given templates. Panics
// on duplicate names so config errors surface at startup.
func NewRegistry(ts ...*Template) *Registry {
	r := &Registry{byName: make(map[string]*Template, len(ts))}
	for _, t := range ts {
		if _, exists := r.byName[t.Name]; exists {
			panic(fmt.Sprintf("templates: duplicate registration for %q", t.Name))
		}
		r.byName[t.Name] = t
		r.order = append(r.order, t.Name)
	}
	sort.Slice(r.order, func(i, j int) bool {
		return r.byName[r.order[i]].DisplayName < r.byName[r.order[j]].DisplayName
	})
	return r
}

// All returns the registered templates in stable display order.
func (r *Registry) All() []*Template {
	out := make([]*Template, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.byName[n])
	}
	return out
}

// Get looks up a template by name. Returns nil if not registered.
func (r *Registry) Get(name string) *Template {
	return r.byName[name]
}

// Render is a shortcut for r.Get(name).Render(params) that returns a
// helpful error when the template isn't registered.
func (r *Registry) Render(name string, params map[string]any) (*protocol.Resource, error) {
	t := r.Get(name)
	if t == nil {
		return nil, fmt.Errorf("template %q not found", name)
	}
	if err := validateParams(t.Parameters, params); err != nil {
		return nil, err
	}
	return t.Render(params)
}

// validateParams enforces required + type constraints before the
// template's Render func runs. Keeps every template's Render body free
// of the same boilerplate.
func validateParams(defs []ParamDef, params map[string]any) error {
	for _, def := range defs {
		v, present := params[def.Name]
		if !present {
			if def.Required && def.Default == nil {
				return fmt.Errorf("parameter %q is required", def.Name)
			}
			continue
		}
		switch def.Type {
		case "string":
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("parameter %q: want string, got %T", def.Name, v)
			}
			if len(def.Enum) > 0 && !contains(def.Enum, s) {
				return fmt.Errorf("parameter %q: %q not in %v", def.Name, s, def.Enum)
			}
		case "int":
			switch v.(type) {
			case int, int32, int64, float64:
				// float64 accepted because JSON numbers decode as float64.
			default:
				return fmt.Errorf("parameter %q: want int, got %T", def.Name, v)
			}
		case "bool":
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("parameter %q: want bool, got %T", def.Name, v)
			}
		}
	}
	return nil
}

// getString returns the string value at key, or its declared default
// if missing. Panics on wrong type — Render should have already gone
// through validateParams.
func getString(params map[string]any, defs []ParamDef, key string) string {
	if v, ok := params[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	for _, d := range defs {
		if d.Name == key {
			if s, ok := d.Default.(string); ok {
				return s
			}
			return ""
		}
	}
	return ""
}

// getInt returns the numeric value at key as an int, or the declared
// default if missing. JSON numbers arrive as float64 over the wire.
func getInt(params map[string]any, defs []ParamDef, key string) int {
	if v, ok := params[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int32:
			return int(n)
		case int64:
			return int(n)
		case float64:
			return int(n)
		}
	}
	for _, d := range defs {
		if d.Name == key {
			switch n := d.Default.(type) {
			case int:
				return n
			case int64:
				return int(n)
			case float64:
				return int(n)
			}
		}
	}
	return 0
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// Default returns a registry with all built-in templates.
func Default() *Registry {
	return NewRegistry(
		UbuntuServerVM(),
		SmallK3sCluster(),
	)
}
