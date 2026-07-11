package tfhost_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl/internal/controller/providers/tfhost"
	"github.com/openctl/openctl/internal/controller/providerstate"
	"github.com/openctl/openctl/internal/controller/storage"
	openctlschema "github.com/openctl/openctl/internal/schema"
	"github.com/openctl/openctl/pkg/protocol"
)

// TestRealProviderTimeStatic drives the *published* hashicorp/time provider
// (a plugin-framework, protocol-6 provider that returns msgpack DynamicValues)
// end-to-end through the tfhost adapter. Unlike the in-repo tf-fake, this proves
// the msgpack decode + typed encoding work against a provider openctl did not
// author.
//
// It is gated on TFHOST_E2E_PROVIDER_TIME pointing at a terraform-provider-time
// binary, so CI stays hermetic. Fetch one with, e.g.:
//
//	curl -sSLo /tmp/tpt.zip https://releases.hashicorp.com/terraform-provider-time/0.12.1/terraform-provider-time_0.12.1_$(go env GOOS)_$(go env GOARCH).zip
//	unzip -o /tmp/tpt.zip -d /tmp
//	TFHOST_E2E_PROVIDER_TIME=/tmp/terraform-provider-time_v0.12.1_x5 go test ./internal/controller/providers/tfhost/ -run TestRealProviderTimeStatic -v
func TestRealProviderTimeStatic(t *testing.T) {
	bin := os.Getenv("TFHOST_E2E_PROVIDER_TIME")
	if bin == "" {
		t.Skip("set TFHOST_E2E_PROVIDER_TIME to a terraform-provider-time binary to run this")
	}

	openctlschema.ResetExternal()
	defer openctlschema.ResetExternal()

	client, err := tfhost.Launch(bin)
	if err != nil {
		t.Fatalf("launch time provider: %v", err)
	}
	defer client.Close()

	if v := client.ProtocolVersion(); v != 5 {
		t.Fatalf("negotiated protocol %d, want 5 for the SDKv2 time provider", v)
	}

	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	defer db.Close()

	p, err := tfhost.NewProvider(ctx, "time", client, providerstate.New(db), []tfhost.ResourceMapping{
		{Kind: "Static", TypeName: "time_static"},
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	const rfc3339 = "2020-02-12T06:36:42Z"
	applied, err := p.Apply(ctx, &protocol.Resource{
		APIVersion: "time.openctl.io/v1",
		Kind:       "Static",
		Metadata:   protocol.ResourceMetadata{Name: "moment"},
		Spec:       map[string]any{"rfc3339": rfc3339},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// rfc3339 is Optional+Computed -> configurable -> spec; the derived fields
	// (year, unix, ...) are Computed-only -> status. These come back as msgpack
	// numbers, decoded to float64.
	if got, _ := applied.Spec["rfc3339"].(string); got != rfc3339 {
		t.Errorf("spec.rfc3339 = %q, want %q", got, rfc3339)
	}
	if got, _ := applied.Status["year"].(float64); got != 2020 {
		t.Errorf("status.year = %v, want 2020", applied.Status["year"])
	}
	if _, ok := applied.Status["unix"].(float64); !ok {
		t.Errorf("status.unix missing/!number: %v", applied.Status["unix"])
	}
	if _, ok := applied.Status["id"].(string); !ok {
		t.Errorf("status.id missing: %v", applied.Status["id"])
	}

	// Get refreshes from the persisted msgpack state.
	read, err := p.Get(ctx, "Static", "moment")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got, _ := read.Status["year"].(float64); got != 2020 {
		t.Errorf("Get status.year = %v, want 2020", read.Status["year"])
	}

	if err := p.Delete(ctx, "Static", "moment"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
