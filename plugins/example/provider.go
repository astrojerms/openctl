package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openctl/openctl/pkg/pluginproto"
	"github.com/openctl/openctl/pkg/protocol"
)

// noteSchema is the CUE schema the provider advertises for its Note kind. It
// describes the whole resource and is compiled standalone by the controller
// (no openctl module imports). The trailing `...` keeps it open so controller-
// managed fields (metadata.labels, status, etc.) don't get rejected.
const noteSchema = `
#Note: {
	apiVersion: "example.openctl.io/v1"
	kind:       "Note"
	metadata: {
		name: string
		...
	}
	spec: {
		// content is the note's body, written verbatim to <dir>/<name>.note.
		content: string
	}
	...
}
`

// notebookSchema is the schema for the composite Notebook kind. A Notebook is
// a Planner parent: it expands (via Plan) into one Note child per page. This is
// the reference example for the composite-child "advanced" mechanism — see the
// Handshake below, where Note is declared with OwnerKind: Notebook so the UI
// flags it "advanced" and nudges toward creating a Notebook.
const notebookSchema = `
#Notebook: {
	apiVersion: "example.openctl.io/v1"
	kind:       "Notebook"
	metadata: {
		name: string
		...
	}
	spec: {
		// pages each expand into one Note child (see Plan). Child note names
		// are "<notebook>-<page>".
		pages: [...{
			name:    string
			content: string
		}]
	}
	...
}
`

const (
	kindNote     = "Note"
	kindNotebook = "Notebook"
	fileExt      = ".note"
	notebookExt  = ".notebook"

	// Owner labels attribute a Plan child back to its composite parent. These
	// are the stable, documented key spellings (mirroring
	// providers.LabelOwnerKind / LabelOwnerName, which a separate-module plugin
	// can't import) that the controller's children-graph reads.
	ownerKindLabel = "openctl.io/owner-kind"
	ownerNameLabel = "openctl.io/owner-name"
)

// provider is the file-backed Note provider. It implements pluginproto.Handler.
type provider struct {
	pluginproto.UnimplementedHandler
	dir string
}

func newProvider() *provider {
	return &provider{dir: "."}
}

// config is the shape the provider expects in the configure bag. It matches
// the JSON of openctl's protocol.ProviderConfig, so `defaults.dir` in
// config.yaml lands here.
type config struct {
	Defaults map[string]string `json:"defaults"`
}

func (p *provider) Handshake(context.Context) (*pluginproto.HandshakeResult, error) {
	return &pluginproto.HandshakeResult{
		ProviderName:    "example",
		ProtocolVersion: pluginproto.ProtocolVersion,
		Capabilities:    []string{pluginproto.CapabilitySchema, pluginproto.CapabilityActions, pluginproto.CapabilityPlan},
		Kinds: []pluginproto.KindInfo{
			{Kind: kindNotebook, Schema: notebookSchema},
			{
				Kind:    kindNote,
				Schema:  noteSchema,
				Actions: []string{"touch"},
				// Note is a composite-child of Notebook: normally produced by a
				// Notebook apply, though it stays directly authorable. OwnerKind +
				// AdvancedNote flow through SchemaService.ListSchemas to the UI's
				// "advanced" chip / create-form banner — no client-side list.
				OwnerKind:    kindNotebook,
				AdvancedNote: "A Note is usually one page of a Notebook, which creates its Notes for you. Author a Note directly only for a standalone note.",
			},
		},
	}, nil
}

func (p *provider) Configure(_ context.Context, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var c config
	if err := json.Unmarshal(raw, &c); err != nil {
		return pluginproto.Unsupported("bad config: " + err.Error())
	}
	if dir := c.Defaults["dir"]; dir != "" {
		p.dir = dir
	}
	if err := os.MkdirAll(p.dir, 0o755); err != nil {
		return fmt.Errorf("create note dir %q: %w", p.dir, err)
	}
	fmt.Fprintf(os.Stderr, "openctl-example: storing notes under %s\n", p.dir)
	return nil
}

func (p *provider) path(name string) string {
	return filepath.Join(p.dir, name+fileExt)
}

func (p *provider) Apply(_ context.Context, req pluginproto.ApplyParams) (*pluginproto.ApplyResult, error) {
	m := req.Manifest
	if m == nil {
		return nil, pluginproto.Unsupported("apply: nil manifest")
	}
	switch m.Kind {
	case kindNote:
		content, _ := m.Spec["content"].(string)
		path := p.path(m.Metadata.Name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // world-readable note file is intentional
			return nil, fmt.Errorf("write note: %w", err)
		}
		return &pluginproto.ApplyResult{Resource: p.observe(m.Metadata.Name, content, path)}, nil
	case kindNotebook:
		return p.applyNotebook(m)
	default:
		return nil, pluginproto.Unsupported("example provider only handles Note and Notebook")
	}
}

// applyNotebook writes each page as a child Note file and a marker file listing
// the children so Get/List/Delete can find them later. The plugin creates its
// children directly (there is no controller-side auto-expansion for external
// composites); Plan below feeds the same child set to the UI's children graph.
func (p *provider) applyNotebook(m *protocol.Resource) (*pluginproto.ApplyResult, error) {
	pages := notebookPages(m.Spec)
	names := make([]string, 0, len(pages))
	for _, pg := range pages {
		child := childNoteName(m.Metadata.Name, pg.name)
		if err := os.WriteFile(p.path(child), []byte(pg.content), 0o644); err != nil { //nolint:gosec // world-readable note file is intentional
			return nil, fmt.Errorf("write notebook page %q: %w", pg.name, err)
		}
		names = append(names, child)
	}
	marker, err := json.Marshal(names)
	if err != nil {
		return nil, fmt.Errorf("encode notebook: %w", err)
	}
	if err := os.WriteFile(p.notebookPath(m.Metadata.Name), marker, 0o644); err != nil { //nolint:gosec // marker file, no secrets
		return nil, fmt.Errorf("write notebook: %w", err)
	}
	return &pluginproto.ApplyResult{Resource: p.observeNotebook(m.Metadata.Name, names)}, nil
}

// Plan implements composite expansion: a Notebook fans out into one Note child
// per page, each labeled back to its parent. openctl calls this to render the
// Notebook -> Note edges in the UI children graph.
func (p *provider) Plan(_ context.Context, manifest *protocol.Resource) (*pluginproto.PlanResult, error) {
	if manifest == nil || manifest.Kind != kindNotebook {
		return nil, pluginproto.Unsupported("example provider only plans Notebook")
	}
	var children []*protocol.Resource
	for _, pg := range notebookPages(manifest.Spec) {
		child := &protocol.Resource{
			APIVersion: "example.openctl.io/v1",
			Kind:       kindNote,
			Spec:       map[string]any{"content": pg.content},
		}
		child.Metadata.Name = childNoteName(manifest.Metadata.Name, pg.name)
		child.Metadata.Labels = map[string]string{
			ownerKindLabel: kindNotebook,
			ownerNameLabel: manifest.Metadata.Name,
		}
		children = append(children, child)
	}
	return &pluginproto.PlanResult{Children: children}, nil
}

func (p *provider) Get(_ context.Context, req pluginproto.GetParams) (*pluginproto.GetResult, error) {
	switch req.Kind {
	case kindNote:
		path := p.path(req.Name)
		data, err := os.ReadFile(path) //nolint:gosec // path is derived from a controller-supplied resource name
		if err != nil {
			if os.IsNotExist(err) {
				return nil, pluginproto.NotFound(fmt.Sprintf("Note %q not found", req.Name))
			}
			return nil, fmt.Errorf("read note: %w", err)
		}
		return &pluginproto.GetResult{Resource: p.observe(req.Name, string(data), path)}, nil
	case kindNotebook:
		names, err := p.readNotebook(req.Name)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, pluginproto.NotFound(fmt.Sprintf("Notebook %q not found", req.Name))
			}
			return nil, err
		}
		return &pluginproto.GetResult{Resource: p.observeNotebook(req.Name, names)}, nil
	default:
		return nil, pluginproto.Unsupported("example provider only handles Note and Notebook")
	}
}

func (p *provider) List(_ context.Context, kind string) ([]*protocol.Resource, error) {
	if kind != kindNote && kind != kindNotebook {
		return nil, nil
	}
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list %s: %w", kind, err)
	}
	var out []*protocol.Resource
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch kind {
		case kindNotebook:
			if !strings.HasSuffix(e.Name(), notebookExt) {
				continue
			}
			name := strings.TrimSuffix(e.Name(), notebookExt)
			names, err := p.readNotebook(name)
			if err != nil {
				continue
			}
			out = append(out, p.observeNotebook(name, names))
		case kindNote:
			// .notebook files also end in a superstring of no note ext, but guard
			// explicitly so a marker file is never mistaken for a note.
			if !strings.HasSuffix(e.Name(), fileExt) || strings.HasSuffix(e.Name(), notebookExt) {
				continue
			}
			name := strings.TrimSuffix(e.Name(), fileExt)
			path := p.path(name)
			data, err := os.ReadFile(path) //nolint:gosec // path is within the configured note dir
			if err != nil {
				continue
			}
			out = append(out, p.observe(name, string(data), path))
		}
	}
	return out, nil
}

func (p *provider) Delete(_ context.Context, req pluginproto.DeleteParams) error {
	switch req.Kind {
	case kindNote:
		if err := os.Remove(p.path(req.Name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete note: %w", err)
		}
		return nil // idempotent: missing == deleted
	case kindNotebook:
		// Delete the notebook's child notes, then the marker. Idempotent.
		names, err := p.readNotebook(req.Name)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		for _, child := range names {
			if err := os.Remove(p.path(child)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("delete notebook page %q: %w", child, err)
			}
		}
		if err := os.Remove(p.notebookPath(req.Name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete notebook: %w", err)
		}
		return nil
	default:
		return nil
	}
}

// DoAction implements the "touch" action: bump the note file's mtime.
func (p *provider) DoAction(_ context.Context, req pluginproto.DoActionParams) (*pluginproto.DoActionResult, error) {
	if req.Kind != kindNote || req.Action != "touch" {
		return nil, pluginproto.Unsupported("unknown action")
	}
	path := p.path(req.Name)
	if _, err := os.Stat(path); err != nil {
		return nil, pluginproto.NotFound(fmt.Sprintf("Note %q not found", req.Name))
	}
	// os.Chtimes with a zero time uses the current time on most platforms; use
	// an explicit-but-deterministic approach: read+rewrite bumps mtime without
	// needing a clock the provider can trust.
	data, err := os.ReadFile(path) //nolint:gosec // path is within the configured note dir
	if err != nil {
		return nil, fmt.Errorf("touch note: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // world-readable note file is intentional
		return nil, fmt.Errorf("touch note: %w", err)
	}
	return &pluginproto.DoActionResult{Message: "touched " + req.Name}, nil
}

// observe builds the observed Resource for a note, stamping status with the
// on-disk path and a byte count so drift is visible in the UI.
func (p *provider) observe(name, content, path string) *protocol.Resource {
	r := &protocol.Resource{
		APIVersion: "example.openctl.io/v1",
		Kind:       kindNote,
		Spec:       map[string]any{"content": content},
		Status: map[string]any{
			"path":  path,
			"bytes": len(content),
		},
	}
	r.Metadata.Name = name
	return r
}

// --- Notebook helpers ---

// page is one entry in a Notebook's spec.pages.
type page struct {
	name    string
	content string
}

// notebookPages extracts the typed page list from a Notebook spec, skipping
// malformed or unnamed entries.
func notebookPages(spec map[string]any) []page {
	raw, _ := spec["pages"].([]any)
	out := make([]page, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		if name == "" {
			continue
		}
		content, _ := m["content"].(string)
		out = append(out, page{name: name, content: content})
	}
	return out
}

// childNoteName is the deterministic name of the Note a notebook page expands
// into: "<notebook>-<page>".
func childNoteName(notebook, pageName string) string { return notebook + "-" + pageName }

func (p *provider) notebookPath(name string) string {
	return filepath.Join(p.dir, name+notebookExt)
}

// readNotebook returns the child note names recorded in a notebook's marker
// file. The os.IsNotExist error is returned as-is so callers can map it to
// NotFound.
func (p *provider) readNotebook(name string) ([]string, error) {
	data, err := os.ReadFile(p.notebookPath(name)) //nolint:gosec // path is within the configured note dir
	if err != nil {
		return nil, err
	}
	var names []string
	if err := json.Unmarshal(data, &names); err != nil {
		return nil, fmt.Errorf("decode notebook %q: %w", name, err)
	}
	return names, nil
}

// observeNotebook builds the observed Resource for a notebook: its page count
// and the names of the child notes it owns.
func (p *provider) observeNotebook(name string, children []string) *protocol.Resource {
	r := &protocol.Resource{
		APIVersion: "example.openctl.io/v1",
		Kind:       kindNotebook,
		Status: map[string]any{
			"pages": len(children),
			"notes": children,
		},
	}
	r.Metadata.Name = name
	return r
}

// ensure the provider satisfies the Handler contract at compile time.
var _ pluginproto.Handler = (*provider)(nil)
