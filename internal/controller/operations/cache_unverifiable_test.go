package operations

import (
	"context"
	"errors"
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

// Like a command-driven app, this provider can observe placement and health,
// but its Get cannot read back all private deployment inputs.
type unverifiableProvider struct {
	fakeProvider
	content string
	fail    bool
}

func (*unverifiableProvider) CanCacheApply() bool { return false }

func (p *unverifiableProvider) Apply(ctx context.Context, r *protocol.Resource) (*protocol.Resource, error) {
	p.content = r.Spec["content"].(string)
	if p.fail {
		return nil, errors.New("rollout interrupted after remote configuration changed")
	}
	return p.Get(ctx, r.Kind, r.Metadata.Name)
}

func (p *unverifiableProvider) Get(_ context.Context, kind, name string) (*protocol.Resource, error) {
	return &protocol.Resource{
		APIVersion: "fake.openctl.io/v1", Kind: kind,
		Metadata: protocol.ResourceMetadata{Name: name},
		Spec:     map[string]any{"target": "guest"},
		Status:   map[string]any{"servedContent": p.content},
	}, nil
}

func (*unverifiableProvider) DriftSpecs(_ string, desired, observed map[string]any) (map[string]any, map[string]any) {
	return map[string]any{"target": desired["target"]}, observed
}

func TestUnverifiableApplyRecoversPreviousIntentAfterPartialRollout(t *testing.T) {
	p := &unverifiableProvider{fakeProvider: fakeProvider{name: "fake", kinds: []string{"FakeKind"}}}
	_, d := newDispatcherWithManifests(t, p)
	ctx := context.Background()
	original := &protocol.Resource{
		APIVersion: "fake.openctl.io/v1", Kind: "FakeKind",
		Metadata: protocol.ResourceMetadata{Name: "application"},
		Spec:     map[string]any{"target": "guest", "content": "v1"},
	}
	if _, err := d.ApplyManifest(ctx, original); err != nil {
		t.Fatal(err)
	}
	partial := *original
	partial.Spec = map[string]any{"target": "guest", "content": "v2"}
	p.fail = true
	if _, err := d.ApplyManifest(ctx, &partial); err == nil {
		t.Fatal("expected partial rollout failure")
	}
	if p.content != "v2" {
		t.Fatal("fixture did not produce the partial remote effect")
	}
	p.fail = false
	recovered, err := d.ApplyManifest(ctx, original)
	if err != nil {
		t.Fatal(err)
	}
	if got := recovered.Status["servedContent"]; got != "v1" {
		t.Fatalf("reapplying the previous saved intent left partial content %v", got)
	}
}
