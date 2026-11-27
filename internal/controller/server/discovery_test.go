package server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/providers"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

type discoveryProvider struct {
	fakeProvider
	connectionErr error
}

func (p *discoveryProvider) DiscoveryContexts() []providers.DiscoveryContext {
	return []providers.DiscoveryContext{{Name: "site", Endpoint: "https://pve", Default: true}}
}
func (p *discoveryProvider) Discover(context.Context, string) ([]providers.DiscoveredResource, error) {
	return []providers.DiscoveredResource{{Context: "site", Node: "pve1", VMID: 101, Identity: "immutable", Resource: safetyManifest("vm", 1), Adoptable: true}}, nil
}
func (p *discoveryProvider) TestConnection(context.Context, string) error { return p.connectionErr }

func TestViewerDiscoveryDoesNotAdoptAndHonorsAdoptionSafeguards(t *testing.T) {
	p := &discoveryProvider{}
	reg := providers.NewRegistry()
	reg.Register(p)
	store := safetyStore(t)
	h := newResourceHandler(reg, nil, nil, store)
	ctx := ctxWithRole(auth.RoleViewer)
	contexts, err := h.ListDiscoveryContexts(ctx, &apiv1.ListDiscoveryContextsRequest{ApiVersion: "fake.openctl.io/v1"})
	if err != nil || len(contexts.GetContexts()) != 1 || !contexts.Contexts[0].DefaultContext {
		t.Fatalf("contexts=%+v err=%v", contexts, err)
	}
	out, err := h.DiscoverResources(ctx, &apiv1.DiscoverResourcesRequest{ApiVersion: "fake.openctl.io/v1", Context: "site"})
	if err != nil || len(out.GetResources()) != 1 || !out.Resources[0].Adoptable || out.Resources[0].Managed {
		t.Fatalf("inventory=%+v err=%v", out, err)
	}
	tracked, err := store.Load(context.Background(), "fake.openctl.io/v1", "FakeKind", "vm")
	if err != nil || tracked != nil || len(p.applied) != 0 || len(p.deletes) != 0 {
		t.Fatal("discovery changed management or infrastructure")
	}
	h.gitopsEnabled = true
	out, err = h.DiscoverResources(ctx, &apiv1.DiscoverResourcesRequest{ApiVersion: "fake.openctl.io/v1", Context: "site"})
	if err != nil || out.Resources[0].Adoptable || !strings.Contains(out.Resources[0].Reason, "GitOps") {
		t.Fatalf("GitOps adoption safeguard missing: %+v %v", out, err)
	}
}

func TestConnectionFailureDoesNotExposeUpstreamSecrets(t *testing.T) {
	p := &discoveryProvider{connectionErr: fmt.Errorf("https://user:SECRET@example.invalid")}
	reg := providers.NewRegistry()
	reg.Register(p)
	h := newResourceHandler(reg, nil, nil, safetyStore(t))
	out, err := h.TestDiscoveryConnection(ctxWithRole(auth.RoleViewer), &apiv1.TestDiscoveryConnectionRequest{ApiVersion: "fake.openctl.io/v1", Context: "site"})
	if err != nil || out.Connected || strings.Contains(out.Message, "SECRET") {
		t.Fatalf("unsafe connection response=%+v err=%v", out, err)
	}
}
