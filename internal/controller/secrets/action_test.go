package secrets

import (
	"context"
	"errors"
	"testing"
)

func TestActionProviderResolve(t *testing.T) {
	ctx := context.Background()
	var gotAV, gotKind, gotName, gotAction string
	invoke := func(_ context.Context, av, kind, name, action string) (*ActionOutput, error) {
		gotAV, gotKind, gotName, gotAction = av, kind, name, action
		return &ActionOutput{DownloadContent: "the-token", Message: "run it", URL: "https://x"}, nil
	}
	p := NewActionProvider(invoke)

	// Default field is the download content (where get-token puts the token).
	got, err := p.Resolve(ctx, "cloudflare.openctl.io/v1/Tunnel/home#get-token")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "the-token" {
		t.Errorf("value = %q, want the-token", got)
	}
	if gotAV != "cloudflare.openctl.io/v1" || gotKind != "Tunnel" || gotName != "home" || gotAction != "get-token" {
		t.Errorf("invoke args = %q/%q/%q/%q", gotAV, gotKind, gotName, gotAction)
	}

	// Explicit field selectors.
	if got, _ := p.Resolve(ctx, "d/v/K/n#a:message"); got != "run it" {
		t.Errorf("message field = %q", got)
	}
	if got, _ := p.Resolve(ctx, "d/v/K/n#a:url"); got != "https://x" {
		t.Errorf("url field = %q", got)
	}
}

func TestActionProviderResolveErrors(t *testing.T) {
	ctx := context.Background()
	ok := NewActionProvider(func(_ context.Context, _, _, _, _ string) (*ActionOutput, error) {
		return &ActionOutput{DownloadContent: "t"}, nil
	})

	badKeys := []string{
		"no-hash",             // missing '#'
		"only/three/segs#act", // ref not 4 segments
		"d/v/K/n#",            // empty action
		"d//K/n#act",          // empty version segment
	}
	for _, k := range badKeys {
		if _, err := ok.Resolve(ctx, k); err == nil {
			t.Errorf("key %q should error", k)
		}
	}

	// Unknown field.
	if _, err := ok.Resolve(ctx, "d/v/K/n#a:bogus"); err == nil {
		t.Error("unknown field should error")
	}

	// Empty value from the action.
	empty := NewActionProvider(func(_ context.Context, _, _, _, _ string) (*ActionOutput, error) {
		return &ActionOutput{}, nil
	})
	if _, err := empty.Resolve(ctx, "d/v/K/n#a"); err == nil {
		t.Error("empty download content should error")
	}

	// Invoker failure propagates.
	failing := NewActionProvider(func(_ context.Context, _, _, _, _ string) (*ActionOutput, error) {
		return nil, errors.New("boom")
	})
	if _, err := failing.Resolve(ctx, "d/v/K/n#a"); err == nil {
		t.Error("invoker error should propagate")
	}

	// Not wired.
	if _, err := (&ActionProvider{}).Resolve(ctx, "d/v/K/n#a"); err == nil {
		t.Error("nil invoker should error")
	}
}

// TestActionProviderThroughResolver proves the provider works end-to-end via
// the $secret resolver, and (implicitly) that only value substitution happens —
// the caller persists the raw marker separately (dispatcher discipline).
func TestActionProviderThroughResolver(t *testing.T) {
	reg := NewRegistry()
	reg.Register(NewActionProvider(func(_ context.Context, _, _, _, _ string) (*ActionOutput, error) {
		return &ActionOutput{DownloadContent: "resolved-token"}, nil
	}))

	spec := map[string]any{
		"values": map[string]any{
			"token": map[string]any{"$secret": map[string]any{
				"provider": "action",
				"key":      "cloudflare.openctl.io/v1/Tunnel/home#get-token",
			}},
		},
	}
	out, err := New(reg).Resolve(context.Background(), spec)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	vals := out["values"].(map[string]any)
	if vals["token"] != "resolved-token" {
		t.Errorf("token = %v, want resolved-token", vals["token"])
	}
}
