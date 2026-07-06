package external

import (
	"testing"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/providers/providertest"
	"github.com/openctl/openctl/pkg/protocol"
)

// TestExternalAdapterConformance runs the shared providers.Provider
// conformance battery against the external-plugin adapter, driven by the
// in-process testHandler (a stateful CRUD plugin over in-memory pipes). This
// is the ecosystem-widening path: any third-party plugin reaches the
// controller through this adapter, so the adapter must satisfy the same
// baseline contract the compiled-in providers do.
//
// The testHandler updates on re-apply (not no-op-on-existing) and supports
// List, so those capabilities are set accordingly.
func TestExternalAdapterConformance(t *testing.T) {
	providertest.Suite{
		NewProvider: func(t *testing.T) (providers.Provider, func()) {
			return newAdapter(t, newTestHandler("demo"))
		},
		Kind: "Thing",
		Manifest: func(name string) *protocol.Resource {
			r := &protocol.Resource{APIVersion: "demo.openctl.io/v1", Kind: "Thing"}
			r.Metadata.Name = name
			r.Spec = map[string]any{"size": "M"}
			return r
		},
		Capabilities: providertest.Capabilities{SupportsList: true},
	}.Run(t)
}
