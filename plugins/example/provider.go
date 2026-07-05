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

const (
	kindNote = "Note"
	fileExt  = ".note"
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
		Capabilities:    []string{pluginproto.CapabilitySchema, pluginproto.CapabilityActions},
		Kinds: []pluginproto.KindInfo{
			{Kind: kindNote, Schema: noteSchema, Actions: []string{"touch"}},
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
	if m == nil || m.Kind != kindNote {
		return nil, pluginproto.Unsupported("example provider only handles Note")
	}
	content, _ := m.Spec["content"].(string)
	path := p.path(m.Metadata.Name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // world-readable note file is intentional
		return nil, fmt.Errorf("write note: %w", err)
	}
	return &pluginproto.ApplyResult{Resource: p.observe(m.Metadata.Name, content, path)}, nil
}

func (p *provider) Get(_ context.Context, req pluginproto.GetParams) (*pluginproto.GetResult, error) {
	if req.Kind != kindNote {
		return nil, pluginproto.Unsupported("example provider only handles Note")
	}
	path := p.path(req.Name)
	data, err := os.ReadFile(path) //nolint:gosec // path is derived from a controller-supplied resource name
	if err != nil {
		if os.IsNotExist(err) {
			return nil, pluginproto.NotFound(fmt.Sprintf("Note %q not found", req.Name))
		}
		return nil, fmt.Errorf("read note: %w", err)
	}
	return &pluginproto.GetResult{Resource: p.observe(req.Name, string(data), path)}, nil
}

func (p *provider) List(_ context.Context, kind string) ([]*protocol.Resource, error) {
	if kind != kindNote {
		return nil, nil
	}
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list notes: %w", err)
	}
	var out []*protocol.Resource
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), fileExt) {
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
	return out, nil
}

func (p *provider) Delete(_ context.Context, req pluginproto.DeleteParams) error {
	if req.Kind != kindNote {
		return nil
	}
	err := os.Remove(p.path(req.Name))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete note: %w", err)
	}
	return nil // idempotent: missing == deleted
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

// ensure the provider satisfies the Handler contract at compile time.
var _ pluginproto.Handler = (*provider)(nil)
