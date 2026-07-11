package external_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/providers/external"
	"github.com/openctl/openctl/pkg/protocol"
)

// TestCloudflarePluginHandshakeE2E builds the openctl-cloudflare plugin, spawns
// it as a real subprocess, and drives handshake + configure through the
// external adapter's Load path. It asserts the wire contract (identity, kinds,
// capabilities-derived interfaces) without touching the real Cloudflare API —
// the CRUD logic is unit-tested against a fake API in the plugin package.
func TestCloudflarePluginHandshakeE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a plugin binary; skipped under -short")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	ctx := context.Background()
	bin := filepath.Join(t.TempDir(), "openctl-cloudflare")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "github.com/openctl/openctl/plugins/cloudflare")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build cloudflare plugin: %v", err)
	}

	cmd := exec.CommandContext(ctx, bin, "plugin-serve")
	// Configure requires a token (it builds the API client); a dummy suffices
	// since we never call the API here.
	cfg := &protocol.ProviderConfig{
		TokenSecret: "dummy-token",
		Defaults:    map[string]string{"zoneId": "z", "accountId": "a"},
	}

	prov, hs, client, err := external.Load(ctx, cmd, cfg, nil)
	if err != nil {
		t.Fatalf("load cloudflare plugin: %v", err)
	}
	defer func() { _ = client.Close(ctx) }()

	if hs.ProviderName != "cloudflare" {
		t.Errorf("provider name = %q, want cloudflare", hs.ProviderName)
	}
	kinds := map[string]bool{}
	for _, k := range hs.Kinds {
		if k.Schema == "" {
			t.Errorf("kind %q has no schema over the wire", k.Kind)
		}
		kinds[k.Kind] = true
	}
	if !kinds["DNSRecord"] || !kinds["Tunnel"] {
		t.Fatalf("expected DNSRecord + Tunnel kinds, got %+v", hs.Kinds)
	}
	if prov.Name() != "cloudflare" {
		t.Errorf("adapter Name = %q", prov.Name())
	}

	// The tunnel get-token action must surface through the Actioner interface.
	actioner, ok := prov.(providers.Actioner)
	if !ok {
		t.Fatal("adapter should implement Actioner (tunnel get-token)")
	}
	if acts := actioner.Actions("Tunnel"); len(acts) != 1 || acts[0] != "get-token" {
		t.Errorf("Tunnel actions = %v, want [get-token]", acts)
	}
}
