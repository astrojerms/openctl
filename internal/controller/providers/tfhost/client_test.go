package tfhost_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl/internal/controller/providers/tfhost"
)

// buildFakeProvider compiles the tf-fake tfplugin6 provider into a temp binary
// and returns its path. Skips under -short or when the go toolchain is absent.
func buildFakeProvider(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a provider binary; skipped under -short")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	bin := filepath.Join(t.TempDir(), "tf-fake")
	build := exec.Command("go", "build", "-o", bin, "github.com/openctl/openctl/plugins/tf-fake")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build tf-fake: %v", err)
	}
	return bin
}

func TestLaunchAndGetSchema(t *testing.T) {
	bin := buildFakeProvider(t)

	client, err := tfhost.Launch(bin)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer client.Close()

	resp, err := client.GetProviderSchema(context.Background())
	if err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}

	// Provider config schema came through.
	if resp.Provider == nil || resp.Provider.Block == nil {
		t.Fatal("missing provider schema block")
	}

	// The fake advertises exactly one resource, fake_thing, with name + note.
	rs, ok := resp.ResourceSchemas["fake_thing"]
	if !ok {
		t.Fatalf("fake_thing resource schema missing; got %d resource schemas", len(resp.ResourceSchemas))
	}
	if rs.Version != 1 {
		t.Errorf("fake_thing schema version = %d, want 1", rs.Version)
	}
	attrs := map[string]bool{}
	for _, a := range rs.Block.Attributes {
		attrs[a.Name] = true
	}
	if !attrs["name"] || !attrs["note"] {
		t.Errorf("fake_thing attributes = %v, want name + note", attrs)
	}
}

func TestLaunchBadBinary(t *testing.T) {
	// A non-provider binary must fail the handshake, not hang.
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	_, err := tfhost.Launch("/bin/echo")
	if err == nil {
		t.Fatal("expected launch of a non-provider binary to fail")
	}
}
