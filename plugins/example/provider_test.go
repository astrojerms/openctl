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
	if len(hs.Kinds) != 1 || hs.Kinds[0].Kind != kindNote {
		t.Fatalf("kinds = %+v", hs.Kinds)
	}
	if hs.Kinds[0].Schema == "" {
		t.Error("Note should carry a schema")
	}
	if len(hs.Kinds[0].Actions) != 1 || hs.Kinds[0].Actions[0] != "touch" {
		t.Errorf("actions = %v", hs.Kinds[0].Actions)
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
