package pluginproto

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/openctl/openctl/pkg/protocol"
)

// fakeHandler is an in-memory provider used to exercise the protocol without
// spawning a process. It stores resources in a map and records the last
// config it was handed.
type fakeHandler struct {
	UnimplementedHandler
	mu           sync.Mutex
	store        map[string]*protocol.Resource
	lastConfig   json.RawMessage
	supportsPlan bool
}

func newFakeHandler() *fakeHandler {
	return &fakeHandler{store: map[string]*protocol.Resource{}}
}

func (f *fakeHandler) Handshake(context.Context) (*HandshakeResult, error) {
	caps := []string{CapabilityDryRun, CapabilityActions, CapabilityOwnership, CapabilityChildren, CapabilityState}
	if f.supportsPlan {
		caps = append(caps, CapabilityPlan)
	}
	return &HandshakeResult{
		ProviderName:    "fake",
		ProtocolVersion: ProtocolVersion,
		Capabilities:    caps,
		Kinds: []KindInfo{
			{Kind: "Widget", Schema: "#Widget: {}", Actions: []string{"poke"}},
			{Kind: "Gadget", Observed: true},
		},
	}, nil
}

func (f *fakeHandler) Configure(_ context.Context, config json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastConfig = config
	return nil
}

func (f *fakeHandler) Apply(_ context.Context, p ApplyParams) (*ApplyResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := p.Manifest.Kind + "/" + p.Manifest.Metadata.Name
	r := *p.Manifest
	r.Status = map[string]any{"phase": "Ready"}
	f.store[key] = &r
	// Echo state back plus a marker so state round-tripping is observable.
	return &ApplyResult{Resource: &r, State: json.RawMessage(`{"applied":true}`), Private: p.Private}, nil
}

func (f *fakeHandler) Get(_ context.Context, p GetParams) (*GetResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.store[p.Kind+"/"+p.Name]
	if !ok {
		return nil, NotFound(fmt.Sprintf("%s/%s not found", p.Kind, p.Name))
	}
	return &GetResult{Resource: r, State: p.State}, nil
}

func (f *fakeHandler) List(_ context.Context, kind string) ([]*protocol.Resource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*protocol.Resource
	for _, r := range f.store {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeHandler) Delete(_ context.Context, p DeleteParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.store, p.Kind+"/"+p.Name)
	return nil
}

func (f *fakeHandler) Plan(_ context.Context, manifest *protocol.Resource) (*PlanResult, error) {
	child := &protocol.Resource{APIVersion: manifest.APIVersion, Kind: "Widget"}
	child.Metadata.Name = manifest.Metadata.Name + "-child"
	return &PlanResult{Children: []*protocol.Resource{child}}, nil
}

func (f *fakeHandler) DryRun(_ context.Context, manifest *protocol.Resource) (*DryRunResult, error) {
	return &DryRunResult{
		Children: []ChildAction{{Verb: "create", Kind: manifest.Kind, Name: manifest.Metadata.Name}},
		Summary:  "1 to create",
	}, nil
}

func (f *fakeHandler) DoAction(_ context.Context, p DoActionParams) (*DoActionResult, error) {
	return &DoActionResult{Message: "did " + p.Action + " on " + p.Name}, nil
}

func (f *fakeHandler) OwnerOf(_ context.Context, p RefParams) (*OwnerOfResult, error) {
	if p.Name == "owned" {
		return &OwnerOfResult{OwnerKind: "Cluster", OwnerName: "c1", Owned: true}, nil
	}
	return &OwnerOfResult{Owned: false}, nil
}

func (f *fakeHandler) ChildrenOf(_ context.Context, p RefParams) ([]ResourceRef, error) {
	if p.Name == "parent" {
		return []ResourceRef{{APIVersion: "fake.openctl.io/v1", Kind: "Widget", Name: "kid"}}, nil
	}
	return nil, nil
}

// newTestPair wires a Client and a ServeConn Handler back-to-back over two
// in-memory pipes and returns the client plus a teardown func.
func newTestPair(t *testing.T, h Handler) (*Client, func()) {
	t.Helper()
	// clientToServer: client writes requests, server reads.
	c2sR, c2sW := io.Pipe()
	// serverToClient: server writes responses, client reads.
	s2cR, s2cW := io.Pipe()

	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		_ = ServeConn(context.Background(), c2sR, s2cW, h)
		_ = s2cW.Close()
	}()

	client := NewClient(s2cR, c2sW)
	teardown := func() {
		_ = client.Close(context.Background())
		_ = c2sW.Close()
		<-serveDone
	}
	return client, teardown
}

func mustHandshake(t *testing.T, c *Client) *HandshakeResult {
	t.Helper()
	hs, err := c.Handshake(context.Background())
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	return hs
}

func TestHandshake(t *testing.T) {
	c, done := newTestPair(t, newFakeHandler())
	defer done()

	hs := mustHandshake(t, c)
	if hs.ProviderName != "fake" {
		t.Errorf("ProviderName = %q, want fake", hs.ProviderName)
	}
	if hs.ProtocolVersion != ProtocolVersion {
		t.Errorf("ProtocolVersion = %q, want %q", hs.ProtocolVersion, ProtocolVersion)
	}
	if len(hs.Kinds) != 2 {
		t.Fatalf("Kinds = %d, want 2", len(hs.Kinds))
	}
	if hs.Kinds[0].Schema == "" {
		t.Error("expected Widget to carry a schema")
	}
	if !hs.Kinds[1].Observed {
		t.Error("expected Gadget to be observed-only")
	}
}

func TestConfigure(t *testing.T) {
	h := newFakeHandler()
	c, done := newTestPair(t, h)
	defer done()
	mustHandshake(t, c)

	cfg := map[string]string{"endpoint": "https://example"}
	if err := c.Configure(context.Background(), cfg); err != nil {
		t.Fatalf("configure: %v", err)
	}
	h.mu.Lock()
	got := string(h.lastConfig)
	h.mu.Unlock()
	if got != `{"endpoint":"https://example"}` {
		t.Errorf("lastConfig = %s", got)
	}
}

func TestApplyGetDeleteRoundTrip(t *testing.T) {
	c, done := newTestPair(t, newFakeHandler())
	defer done()
	mustHandshake(t, c)
	ctx := context.Background()

	m := &protocol.Resource{APIVersion: "fake.openctl.io/v1", Kind: "Widget"}
	m.Metadata.Name = "w1"
	m.Spec = map[string]any{"size": 3}

	ar, err := c.Apply(ctx, ApplyParams{Manifest: m, Private: json.RawMessage(`"secret"`)})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if ar.Resource.Status["phase"] != "Ready" {
		t.Errorf("apply status = %v", ar.Resource.Status)
	}
	if string(ar.State) != `{"applied":true}` {
		t.Errorf("state blob = %s", ar.State)
	}
	if string(ar.Private) != `"secret"` {
		t.Errorf("private blob not round-tripped: %s", ar.Private)
	}

	gr, err := c.Get(ctx, GetParams{Kind: "Widget", Name: "w1"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if gr.Resource.Metadata.Name != "w1" {
		t.Errorf("get name = %q", gr.Resource.Metadata.Name)
	}

	list, err := c.List(ctx, "Widget")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}

	if err := c.Delete(ctx, DeleteParams{Kind: "Widget", Name: "w1"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := c.Get(ctx, GetParams{Kind: "Widget", Name: "w1"}); err == nil {
		t.Fatal("expected NotFound after delete")
	}
}

func TestGetNotFoundCode(t *testing.T) {
	c, done := newTestPair(t, newFakeHandler())
	defer done()
	mustHandshake(t, c)

	_, err := c.Get(context.Background(), GetParams{Kind: "Widget", Name: "ghost"})
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if e.Code != CodeNotFound {
		t.Errorf("code = %q, want %q", e.Code, CodeNotFound)
	}
}

func TestOptionalMethods(t *testing.T) {
	h := newFakeHandler()
	h.supportsPlan = true
	c, done := newTestPair(t, h)
	defer done()
	mustHandshake(t, c)
	ctx := context.Background()

	m := &protocol.Resource{APIVersion: "fake.openctl.io/v1", Kind: "Cluster"}
	m.Metadata.Name = "c1"

	plan, err := c.Plan(ctx, m)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Children) != 1 || plan.Children[0].Metadata.Name != "c1-child" {
		t.Errorf("plan children = %+v", plan.Children)
	}

	dr, err := c.DryRun(ctx, m)
	if err != nil {
		t.Fatalf("dryRun: %v", err)
	}
	if dr.Summary != "1 to create" {
		t.Errorf("dryRun summary = %q", dr.Summary)
	}

	act, err := c.DoAction(ctx, DoActionParams{Kind: "Widget", Name: "w1", Action: "poke"})
	if err != nil {
		t.Fatalf("doAction: %v", err)
	}
	if act.Message != "did poke on w1" {
		t.Errorf("action message = %q", act.Message)
	}

	owner, err := c.OwnerOf(ctx, RefParams{Kind: "Widget", Name: "owned"})
	if err != nil {
		t.Fatalf("ownerOf: %v", err)
	}
	if !owner.Owned || owner.OwnerName != "c1" {
		t.Errorf("owner = %+v", owner)
	}

	kids, err := c.ChildrenOf(ctx, RefParams{Kind: "Cluster", Name: "parent"})
	if err != nil {
		t.Fatalf("childrenOf: %v", err)
	}
	if len(kids) != 1 || kids[0].Name != "kid" {
		t.Errorf("children = %+v", kids)
	}
}

func TestUnsupportedOptionalMethod(t *testing.T) {
	// The bare UnimplementedHandler-backed provider supports nothing optional.
	h := &minimalHandler{}
	c, done := newTestPair(t, h)
	defer done()
	mustHandshake(t, c)

	m := &protocol.Resource{APIVersion: "min.openctl.io/v1", Kind: "Thing"}
	_, err := c.Plan(context.Background(), m)
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeUnsupported {
		t.Fatalf("plan err = %v, want CodeUnsupported", err)
	}
}

// minimalHandler implements only the required methods via UnimplementedHandler.
type minimalHandler struct{ UnimplementedHandler }

func (minimalHandler) Handshake(context.Context) (*HandshakeResult, error) {
	return &HandshakeResult{ProviderName: "min", ProtocolVersion: ProtocolVersion}, nil
}
func (minimalHandler) Apply(_ context.Context, p ApplyParams) (*ApplyResult, error) {
	return &ApplyResult{Resource: p.Manifest}, nil
}
func (minimalHandler) Get(context.Context, GetParams) (*GetResult, error) {
	return nil, NotFound("nope")
}
func (minimalHandler) List(context.Context, string) ([]*protocol.Resource, error) { return nil, nil }
func (minimalHandler) Delete(context.Context, DeleteParams) error                 { return nil }

func TestConcurrentCallsCorrelate(t *testing.T) {
	c, done := newTestPair(t, newFakeHandler())
	defer done()
	mustHandshake(t, c)
	ctx := context.Background()

	// Seed distinct resources, then fetch them concurrently and verify each
	// call gets its own response (ID correlation under load).
	const n = 20
	for i := range n {
		m := &protocol.Resource{APIVersion: "fake.openctl.io/v1", Kind: "Widget"}
		m.Metadata.Name = fmt.Sprintf("w%d", i)
		if _, err := c.Apply(ctx, ApplyParams{Manifest: m}); err != nil {
			t.Fatalf("seed apply: %v", err)
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("w%d", i)
			gr, err := c.Get(ctx, GetParams{Kind: "Widget", Name: name})
			if err != nil {
				errs <- fmt.Errorf("get %s: %w", name, err)
				return
			}
			if gr.Resource.Metadata.Name != name {
				errs <- fmt.Errorf("got %q, want %q", gr.Resource.Metadata.Name, name)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestCallAfterCloseFails(t *testing.T) {
	c, done := newTestPair(t, newFakeHandler())
	mustHandshake(t, c)
	done() // closes the client and server

	_, err := c.Get(context.Background(), GetParams{Kind: "Widget", Name: "w1"})
	if err == nil {
		t.Fatal("expected error calling a closed client")
	}
}

func TestContextCancellation(t *testing.T) {
	// A handler that blocks forever on Get; the client's ctx cancel must
	// unblock the call.
	h := &blockingHandler{release: make(chan struct{})}
	c, done := newTestPair(t, h)
	defer done()
	defer close(h.release)
	mustHandshake(t, c)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.Get(ctx, GetParams{Kind: "Widget", Name: "w1"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}

type blockingHandler struct {
	UnimplementedHandler
	release chan struct{}
}

func (blockingHandler) Handshake(context.Context) (*HandshakeResult, error) {
	return &HandshakeResult{ProviderName: "block", ProtocolVersion: ProtocolVersion}, nil
}
func (blockingHandler) Apply(_ context.Context, p ApplyParams) (*ApplyResult, error) {
	return &ApplyResult{Resource: p.Manifest}, nil
}
func (h *blockingHandler) Get(context.Context, GetParams) (*GetResult, error) {
	<-h.release
	return nil, NotFound("released")
}
func (blockingHandler) List(context.Context, string) ([]*protocol.Resource, error) { return nil, nil }
func (blockingHandler) Delete(context.Context, DeleteParams) error                 { return nil }
