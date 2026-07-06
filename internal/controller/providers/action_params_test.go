package providers

import (
	"context"
	"testing"
)

// plainActioner implements only Actioner (no params). Records whether DoAction
// was called.
type plainActioner struct {
	fakeProvider
	called bool
}

func (p *plainActioner) Actions(kind string) []string {
	if kind == "Widget" {
		return []string{"poke"}
	}
	return nil
}
func (p *plainActioner) DoAction(_ context.Context, _, _, _ string) (*ActionResult, error) {
	p.called = true
	return &ActionResult{Message: "poked"}, nil
}

// paramActioner implements ParameterizedActioner and records the params it got.
type paramActioner struct {
	fakeProvider
	gotParams map[string]string
}

func (p *paramActioner) Actions(kind string) []string {
	if kind == "Cluster" {
		return []string{"upgrade"}
	}
	return nil
}
func (p *paramActioner) DoAction(_ context.Context, _, _, _ string) (*ActionResult, error) {
	return nil, nil // not used — ParameterizedActioner path is preferred
}
func (p *paramActioner) DoActionWithParams(_ context.Context, _, _, _ string, params map[string]string) (*ActionResult, error) {
	p.gotParams = params
	return &ActionResult{Message: "upgrading to " + params["version"]}, nil
}

// A ParameterizedActioner receives the invocation's parameters.
func TestDoAction_ParameterizedActionerGetsParams(t *testing.T) {
	p := &paramActioner{fakeProvider: fakeProvider{name: "k3s", kinds: []string{"Cluster"}}}
	r := NewRegistry()
	r.Register(p)

	res, err := r.DoAction(context.Background(), "k3s.openctl.io/v1", "Cluster", "dev", "upgrade",
		map[string]string{"version": "v1.30.5+k3s1"})
	if err != nil {
		t.Fatalf("DoAction: %v", err)
	}
	if p.gotParams["version"] != "v1.30.5+k3s1" {
		t.Errorf("provider got params %v, want version=v1.30.5+k3s1", p.gotParams)
	}
	if res.Message != "upgrading to v1.30.5+k3s1" {
		t.Errorf("result message = %q", res.Message)
	}
}

// A provider implementing only Actioner is still callable; params are dropped,
// not an error (backward-compatible).
func TestDoAction_PlainActionerIgnoresParams(t *testing.T) {
	p := &plainActioner{fakeProvider: fakeProvider{name: "acme", kinds: []string{"Widget"}}}
	r := NewRegistry()
	r.Register(p)

	res, err := r.DoAction(context.Background(), "acme.openctl.io/v1", "Widget", "w0", "poke",
		map[string]string{"ignored": "yes"})
	if err != nil {
		t.Fatalf("DoAction on a plain Actioner: %v", err)
	}
	if !p.called {
		t.Error("plain Actioner.DoAction was not called")
	}
	if res.Message != "poked" {
		t.Errorf("result message = %q, want poked", res.Message)
	}
}

// nil params is fine (the common no-arg action case).
func TestDoAction_NilParams(t *testing.T) {
	p := &paramActioner{fakeProvider: fakeProvider{name: "k3s", kinds: []string{"Cluster"}}}
	r := NewRegistry()
	r.Register(p)

	if _, err := r.DoAction(context.Background(), "k3s.openctl.io/v1", "Cluster", "dev", "upgrade", nil); err != nil {
		t.Fatalf("DoAction with nil params: %v", err)
	}
}
