package tfhost_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/providers/providertest"
	"github.com/openctl/openctl/internal/controller/providers/tfhost"
	"github.com/openctl/openctl/internal/controller/providerstate"
	"github.com/openctl/openctl/internal/controller/storage"
	openctlschema "github.com/openctl/openctl/internal/schema"
	"github.com/openctl/openctl/pkg/protocol"
)

// TestTerraformHostConformance runs the shared providers.Provider conformance
// battery against the Terraform host adapter, driven by the in-repo tf-fake
// provider. This is the second ecosystem-widening path (any Terraform/OpenTofu
// provider reaches the controller through tfhost), and it exercises the
// SupportsList=false capability: tfhost has no list API and returns an error
// from List, so the battery must skip — not fail — the enumeration checks.
//
// The fake provider binary is built once; each subtest launches a fresh
// process + state store so subtests stay isolated.
func TestTerraformHostConformance(t *testing.T) {
	bin := buildFakeProvider(t) // skips under -short / when the go toolchain is absent
	openctlschema.ResetExternal()
	t.Cleanup(openctlschema.ResetExternal)

	ctx := context.Background()
	providertest.Suite{
		NewProvider: func(t *testing.T) (providers.Provider, func()) {
			client, err := tfhost.Launch(bin)
			if err != nil {
				t.Fatalf("launch fake provider: %v", err)
			}
			db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				client.Close()
				t.Fatalf("open storage: %v", err)
			}
			store := providerstate.New(db)
			p, err := tfhost.NewProvider(ctx, "fake", client, store, []tfhost.ResourceMapping{
				{Kind: "Thing", TypeName: "fake_thing"},
			})
			if err != nil {
				_ = db.Close()
				client.Close()
				t.Fatalf("NewProvider: %v", err)
			}
			return p, func() {
				_ = db.Close()
				client.Close()
			}
		},
		Kind: "Thing",
		Manifest: func(name string) *protocol.Resource {
			return &protocol.Resource{
				APIVersion: "fake.openctl.io/v1",
				Kind:       "Thing",
				Metadata:   protocol.ResourceMetadata{Name: name},
				Spec:       map[string]any{"name": name, "note": "conformance"},
			}
		},
		// tfhost has no list API (List returns an error by design), and the
		// fake provider updates on re-apply rather than guaranteeing an atomic
		// no-op, so both capability flags are false.
		Capabilities: providertest.Capabilities{SupportsList: false, NoOpOnExisting: false},
	}.Run(t)
}
