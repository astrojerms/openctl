package schema

import (
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

// widgetSchema is a standalone plugin-supplied CUE schema: a #Widget
// definition describing the whole resource. spec.size must be a positive int.
const widgetSchema = `
#Widget: {
	apiVersion: "acme.openctl.io/v1"
	kind:       "Widget"
	metadata: {
		name: string
		...
	}
	spec: {
		size: int & >0
	}
	...
}
`

func widget(name string, size int) *protocol.Resource {
	r := &protocol.Resource{APIVersion: "acme.openctl.io/v1", Kind: "Widget"}
	r.Metadata.Name = name
	r.Spec = map[string]any{"size": size}
	return r
}

func TestExternalSchemaValidates(t *testing.T) {
	ResetExternal()
	defer ResetExternal()
	RegisterExternal("acme.openctl.io/v1", "Widget", widgetSchema)

	if err := Validate(widget("w1", 4)); err != nil {
		t.Fatalf("valid widget rejected: %v", err)
	}

	// size must be > 0 — this should fail.
	if err := Validate(widget("w2", 0)); err == nil {
		t.Fatal("expected validation failure for size=0")
	}
}

func TestExternalSchemaStructuredErrors(t *testing.T) {
	ResetExternal()
	defer ResetExternal()
	RegisterExternal("acme.openctl.io/v1", "Widget", widgetSchema)

	errs, err := ValidateStructured(widget("w2", -1))
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	if len(errs) == 0 {
		t.Fatal("expected at least one structured validation error")
	}
}

func TestUnknownKindStillPassesThrough(t *testing.T) {
	ResetExternal()
	defer ResetExternal()
	// No schema registered for this apiVersion+kind — must pass through.
	r := &protocol.Resource{APIVersion: "unknown.openctl.io/v1", Kind: "Mystery"}
	r.Metadata.Name = "x"
	if err := Validate(r); err != nil {
		t.Errorf("unknown kind should pass through, got: %v", err)
	}
}

func TestExternalSchemaInRegistryAndSource(t *testing.T) {
	ResetExternal()
	defer ResetExternal()
	RegisterExternal("acme.openctl.io/v1", "Widget", widgetSchema)

	var found *Info
	for i := range Registry() {
		if Registry()[i].APIVersion == "acme.openctl.io/v1" && Registry()[i].Kind == "Widget" {
			info := Registry()[i]
			found = &info
			break
		}
	}
	if found == nil {
		t.Fatal("external Widget not present in Registry()")
	}
	if found.Provider != "acme" {
		t.Errorf("provider = %q, want acme", found.Provider)
	}
	src, err := SourceFor(*found)
	if err != nil {
		t.Fatalf("SourceFor external: %v", err)
	}
	if string(src) != widgetSchema {
		t.Errorf("SourceFor returned unexpected source")
	}
}

func TestExternalSchemaMissingDefinition(t *testing.T) {
	ResetExternal()
	defer ResetExternal()
	// Source that doesn't define #Widget — validation should surface a
	// loading error, not silently pass.
	RegisterExternal("acme.openctl.io/v1", "Widget", `#Other: {}`)
	if err := Validate(widget("w1", 3)); err == nil {
		t.Fatal("expected error when external schema lacks #Widget definition")
	}
}

func TestEmbeddedSchemaStillWorks(t *testing.T) {
	ResetExternal()
	defer ResetExternal()
	// A built-in kind must still validate via the embedded path after the
	// refactor. VirtualMachine with an obviously-bad spec should fail; the
	// point is that the embedded branch is still reached (not passed through).
	r := &protocol.Resource{APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine"}
	r.Metadata.Name = "vm1"
	r.Spec = map[string]any{"cores": "not-a-number"}
	if err := Validate(r); err == nil {
		t.Fatal("expected embedded VirtualMachine schema to reject a bad spec")
	}
}
