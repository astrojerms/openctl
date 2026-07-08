package external_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/providers/external"
	"github.com/openctl/openctl/pkg/protocol"
)

// TestExampleProviderEndToEnd builds the reference openctl-example plugin,
// spawns it as a real subprocess, and drives a full CRUD + action lifecycle
// through the external adapter's Load path — exercising spawn, handshake,
// configure, and the on-the-wire protocol against an actual process (the
// pipe-based unit tests cover the in-process path).
func TestExampleProviderEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a plugin binary; skipped under -short")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	ctx := context.Background()
	bin := filepath.Join(t.TempDir(), "openctl-example")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "github.com/openctl/openctl/plugins/example")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build example plugin: %v", err)
	}

	notesDir := t.TempDir()
	cmd := exec.CommandContext(ctx, bin, "plugin-serve")
	cfg := &protocol.ProviderConfig{Defaults: map[string]string{"dir": notesDir}}

	// The example provider is stateless, so no state store is needed.
	prov, hs, client, err := external.Load(ctx, cmd, cfg, nil)
	if err != nil {
		t.Fatalf("load example plugin: %v", err)
	}
	defer func() { _ = client.Close(ctx) }()

	// Handshake surfaced the provider identity and its kinds (Notebook + Note).
	if hs.ProviderName != "example" {
		t.Errorf("provider name = %q, want example", hs.ProviderName)
	}
	kinds := map[string]bool{}
	for _, k := range hs.Kinds {
		if k.Schema == "" {
			t.Errorf("kind %q carries no schema", k.Kind)
		}
		kinds[k.Kind] = true
	}
	if !kinds["Note"] || !kinds["Notebook"] {
		t.Fatalf("expected Note + Notebook kinds, got %+v", hs.Kinds)
	}
	if prov.Name() != "example" {
		t.Errorf("adapter Name = %q", prov.Name())
	}

	// The composite-child "advanced" declaration survives the real subprocess
	// wire round-trip and reaches AdvancedKindDescriber: Note is owned by
	// Notebook.
	adv, ok := prov.(providers.AdvancedKindDescriber)
	if !ok {
		t.Fatal("adapter should implement AdvancedKindDescriber")
	}
	advKinds := adv.AdvancedKinds()
	if len(advKinds) != 1 || advKinds[0].Kind != "Note" || advKinds[0].OwnerKind != "Notebook" {
		t.Fatalf("AdvancedKinds = %+v, want [{Note Notebook ...}]", advKinds)
	}
	if advKinds[0].Note == "" {
		t.Error("advanced Note should carry a note")
	}

	note := &protocol.Resource{APIVersion: "example.openctl.io/v1", Kind: "Note"}
	note.Metadata.Name = "greeting"
	note.Spec = map[string]any{"content": "hello from e2e"}

	// Apply writes a real file.
	if _, err := prov.Apply(ctx, note); err != nil {
		t.Fatalf("apply: %v", err)
	}
	onDisk := filepath.Join(notesDir, "greeting.note")
	data, err := os.ReadFile(onDisk)
	if err != nil {
		t.Fatalf("note file not written: %v", err)
	}
	if string(data) != "hello from e2e" {
		t.Errorf("note content = %q", data)
	}

	// Get reflects observed state.
	got, err := prov.Get(ctx, "Note", "greeting")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Spec["content"] != "hello from e2e" {
		t.Errorf("get content = %v", got.Spec["content"])
	}

	// List finds it.
	list, err := prov.List(ctx, "Note")
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v (len %d), err %v", list, len(list), err)
	}

	// The "touch" action round-trips through Actioner.
	actioner, ok := prov.(providers.Actioner)
	if !ok {
		t.Fatal("adapter should implement Actioner")
	}
	if acts := actioner.Actions("Note"); len(acts) != 1 || acts[0] != "touch" {
		t.Errorf("actions = %v", acts)
	}
	res, err := actioner.DoAction(ctx, "Note", "greeting", "touch")
	if err != nil {
		t.Fatalf("touch: %v", err)
	}
	if res.Message != "touched greeting" {
		t.Errorf("touch message = %q", res.Message)
	}

	// Delete removes the file and Get then maps to NotFoundError.
	if err := prov.Delete(ctx, "Note", "greeting"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(onDisk); !os.IsNotExist(err) {
		t.Errorf("note file still present after delete: %v", err)
	}
	_, err = prov.Get(ctx, "Note", "greeting")
	var nf *providers.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("expected *providers.NotFoundError after delete, got %v", err)
	}
}
