package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl/internal/controller/providerstate"
	"github.com/openctl/openctl/internal/controller/storage"
	openctlschema "github.com/openctl/openctl/internal/schema"
	"github.com/openctl/openctl/pkg/protocol"
)

func TestBuildRegistryLoadsTerraformProvider(t *testing.T) {
	openctlschema.ResetExternal()
	defer openctlschema.ResetExternal()

	bin := buildFakeTerraformProvider(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	configDir := filepath.Join(home, ".openctl")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configFile := filepath.Join(configDir, "config.yaml")
	configBody := fmt.Sprintf(`
providers:
  fake:
    terraform:
      command: %q
      config:
        endpoint: https://registry.example.com
      resources:
        - kind: Thing
          type: fake_thing
`, bin)
	if err := os.WriteFile(configFile, []byte(configBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	defer db.Close()

	registry, names, cleanup, err := buildRegistry(ctx, providerstate.New(db))
	if err != nil {
		t.Fatalf("buildRegistry: %v", err)
	}
	defer cleanup()
	if len(names) != 1 || names[0] != "fake" {
		t.Fatalf("registered names = %v, want [fake]", names)
	}

	prov, err := registry.For("fake.openctl.io/v1")
	if err != nil {
		t.Fatalf("registry.For: %v", err)
	}
	applied, err := prov.Apply(ctx, &protocol.Resource{
		APIVersion: "fake.openctl.io/v1",
		Kind:       "Thing",
		Metadata:   protocol.ResourceMetadata{Name: "from-registry"},
		Spec: map[string]any{
			"name": "from-registry",
		},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, _ := applied.Spec["note"].(string); got != "https://registry.example.com" {
		t.Fatalf("spec.note = %q, want configured endpoint", got)
	}
	if got, _ := applied.Status["id"].(string); got != "fake-from-registry" {
		t.Fatalf("status.id = %q, want fake-from-registry", got)
	}
}

func buildFakeTerraformProvider(t *testing.T) string {
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
