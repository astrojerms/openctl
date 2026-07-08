package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl/pkg/pluginproto"
	"github.com/openctl/openctl/pkg/protocol"
)

func noteManifest(name, content string) *protocol.Resource {
	r := &protocol.Resource{APIVersion: "example.openctl.io/v1", Kind: kindNote}
	r.Metadata.Name = name
	r.Spec = map[string]any{"content": content}
	return r
}

func configuredProvider(t *testing.T) *provider {
	t.Helper()
	p := newProvider()
	cfg, _ := json.Marshal(config{Defaults: map[string]string{"dir": t.TempDir()}})
	if err := p.Configure(context.Background(), cfg); err != nil {
		t.Fatalf("configure: %v", err)
	}
	return p
}

func TestHandshakeAdvertisesSchemaAndActions(t *testing.T) {
	hs, err := newProvider().Handshake(context.Background())
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if hs.ProviderName != "example" {
		t.Errorf("name = %q", hs.ProviderName)
	}
	byKind := map[string]pluginproto.KindInfo{}
	for _, k := range hs.Kinds {
		byKind[k.Kind] = k
	}
	note, ok := byKind[kindNote]
	if !ok {
		t.Fatalf("kinds = %+v, missing Note", hs.Kinds)
	}
	if note.Schema == "" {
		t.Error("Note should carry a schema")
	}
	if len(note.Actions) != 1 || note.Actions[0] != "touch" {
		t.Errorf("Note actions = %v", note.Actions)
	}
	// Note is declared a composite-child of Notebook — this is the reference for
	// the advanced-kind handshake field.
	if note.OwnerKind != kindNotebook {
		t.Errorf("Note ownerKind = %q, want Notebook", note.OwnerKind)
	}
	if note.AdvancedNote == "" {
		t.Error("Note should carry an advancedNote")
	}
	nb, ok := byKind[kindNotebook]
	if !ok || nb.Schema == "" {
		t.Fatalf("Notebook kind missing or schemaless: %+v", nb)
	}
	if nb.OwnerKind != "" {
		t.Errorf("Notebook is the parent, should not be advanced (ownerKind %q)", nb.OwnerKind)
	}
	hasPlan := false
	for _, c := range hs.Capabilities {
		if c == pluginproto.CapabilityPlan {
			hasPlan = true
		}
	}
	if !hasPlan {
		t.Errorf("Notebook composite requires CapabilityPlan; caps = %v", hs.Capabilities)
	}
}

func TestApplyGetListDelete(t *testing.T) {
	p := configuredProvider(t)
	ctx := context.Background()

	ar, err := p.Apply(ctx, pluginproto.ApplyParams{Manifest: noteManifest("hello", "world")})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if ar.Resource.Status["bytes"] != 5 {
		t.Errorf("status bytes = %v, want 5", ar.Resource.Status["bytes"])
	}
	// File actually exists on disk.
	if _, err := os.Stat(filepath.Join(p.dir, "hello.note")); err != nil {
		t.Fatalf("note file not written: %v", err)
	}

	gr, err := p.Get(ctx, pluginproto.GetParams{Kind: kindNote, Name: "hello"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if gr.Resource.Spec["content"] != "world" {
		t.Errorf("content = %v", gr.Resource.Spec["content"])
	}

	list, err := p.List(ctx, kindNote)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v (%d), err %v", list, len(list), err)
	}

	if err := p.Delete(ctx, pluginproto.DeleteParams{Kind: kindNote, Name: "hello"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := p.Get(ctx, pluginproto.GetParams{Kind: kindNote, Name: "hello"}); err == nil {
		t.Fatal("expected NotFound after delete")
	}
}

func notebookManifest(name string, pages ...[2]string) *protocol.Resource {
	r := &protocol.Resource{APIVersion: "example.openctl.io/v1", Kind: kindNotebook}
	r.Metadata.Name = name
	var pl []any
	for _, pg := range pages {
		pl = append(pl, map[string]any{"name": pg[0], "content": pg[1]})
	}
	r.Spec = map[string]any{"pages": pl}
	return r
}

func TestNotebookApplyCreatesChildNotes(t *testing.T) {
	p := configuredProvider(t)
	ctx := context.Background()

	nb := notebookManifest("journal", [2]string{"day1", "hello"}, [2]string{"day2", "world"})
	ar, err := p.Apply(ctx, pluginproto.ApplyParams{Manifest: nb})
	if err != nil {
		t.Fatalf("apply notebook: %v", err)
	}
	if ar.Resource.Status["pages"] != 2 {
		t.Errorf("status pages = %v, want 2", ar.Resource.Status["pages"])
	}

	// Child notes exist on disk under the "<notebook>-<page>" naming and carry
	// the page content.
	for _, tc := range []struct{ name, content string }{
		{"journal-day1", "hello"}, {"journal-day2", "world"},
	} {
		gr, err := p.Get(ctx, pluginproto.GetParams{Kind: kindNote, Name: tc.name})
		if err != nil {
			t.Fatalf("get child note %q: %v", tc.name, err)
		}
		if gr.Resource.Spec["content"] != tc.content {
			t.Errorf("child %q content = %v, want %q", tc.name, gr.Resource.Spec["content"], tc.content)
		}
	}

	// The Notebook lists as a Notebook; the children list as Notes.
	nbs, err := p.List(ctx, kindNotebook)
	if err != nil || len(nbs) != 1 {
		t.Fatalf("list notebooks = %d (%v), err %v", len(nbs), nbs, err)
	}
	notes, err := p.List(ctx, kindNote)
	if err != nil || len(notes) != 2 {
		t.Fatalf("list notes = %d, want 2 (marker must not count), err %v", len(notes), err)
	}

	// Delete removes the notebook and its children.
	if err := p.Delete(ctx, pluginproto.DeleteParams{Kind: kindNotebook, Name: "journal"}); err != nil {
		t.Fatalf("delete notebook: %v", err)
	}
	if _, err := p.Get(ctx, pluginproto.GetParams{Kind: kindNotebook, Name: "journal"}); err == nil {
		t.Error("expected NotFound after notebook delete")
	}
	if notes, _ := p.List(ctx, kindNote); len(notes) != 0 {
		t.Errorf("child notes should be gone after notebook delete, got %d", len(notes))
	}
}

func TestNotebookPlanExpandsToLabeledNotes(t *testing.T) {
	p := configuredProvider(t)
	nb := notebookManifest("journal", [2]string{"day1", "hello"}, [2]string{"day2", "world"})
	res, err := p.Plan(context.Background(), nb)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(res.Children) != 2 {
		t.Fatalf("plan children = %d, want 2", len(res.Children))
	}
	c := res.Children[0]
	if c.Kind != kindNote || c.Metadata.Name != "journal-day1" {
		t.Errorf("child[0] = %s/%s, want Note/journal-day1", c.Kind, c.Metadata.Name)
	}
	if c.Metadata.Labels[ownerKindLabel] != kindNotebook || c.Metadata.Labels[ownerNameLabel] != "journal" {
		t.Errorf("child[0] owner labels = %v, want Notebook/journal", c.Metadata.Labels)
	}
	// Plan only handles Notebook.
	if _, err := p.Plan(context.Background(), noteManifest("x", "y")); err == nil {
		t.Error("Plan on a Note should be unsupported")
	}
}

func TestGetMissingReturnsNotFound(t *testing.T) {
	p := configuredProvider(t)
	_, err := p.Get(context.Background(), pluginproto.GetParams{Kind: kindNote, Name: "ghost"})
	var e *pluginproto.Error
	if !errors.As(err, &e) || e.Code != pluginproto.CodeNotFound {
		t.Fatalf("err = %v, want CodeNotFound", err)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	p := configuredProvider(t)
	if err := p.Delete(context.Background(), pluginproto.DeleteParams{Kind: kindNote, Name: "never-existed"}); err != nil {
		t.Errorf("delete of missing note should be nil, got %v", err)
	}
}

func TestTouchAction(t *testing.T) {
	p := configuredProvider(t)
	ctx := context.Background()
	if _, err := p.Apply(ctx, pluginproto.ApplyParams{Manifest: noteManifest("n", "x")}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	res, err := p.DoAction(ctx, pluginproto.DoActionParams{Kind: kindNote, Name: "n", Action: "touch"})
	if err != nil {
		t.Fatalf("touch: %v", err)
	}
	if res.Message != "touched n" {
		t.Errorf("message = %q", res.Message)
	}
	// Touch on a missing note is NotFound.
	if _, err := p.DoAction(ctx, pluginproto.DoActionParams{Kind: kindNote, Name: "ghost", Action: "touch"}); err == nil {
		t.Error("touch on missing note should error")
	}
}
