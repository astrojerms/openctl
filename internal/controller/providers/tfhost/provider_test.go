package tfhost_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/providers/tfhost"
	"github.com/openctl/openctl/internal/controller/providerstate"
	"github.com/openctl/openctl/internal/controller/storage"
	openctlschema "github.com/openctl/openctl/internal/schema"
	"github.com/openctl/openctl/pkg/protocol"
)

func TestProviderAdapterApplyGetDelete(t *testing.T) {
	openctlschema.ResetExternal()
	defer openctlschema.ResetExternal()

	bin := buildFakeProvider(t)

	client, err := tfhost.Launch(bin)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	defer db.Close()

	store := providerstate.New(db)
	p, err := tfhost.NewProvider(ctx, "fake", client, store, []tfhost.ResourceMapping{
		{Kind: "Thing", TypeName: "fake_thing"},
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if _, ok := findSchema("fake.openctl.io/v1", "Thing"); !ok {
		t.Fatal("NewProvider did not register generated external schema")
	}

	manifest := &protocol.Resource{
		APIVersion: "fake.openctl.io/v1",
		Kind:       "Thing",
		Metadata:   protocol.ResourceMetadata{Name: "alpha"},
		Spec: map[string]any{
			"name": "alpha",
			"note": "via adapter",
		},
	}
	applied, err := p.Apply(ctx, manifest)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	assertResource(t, applied, "fake.openctl.io/v1", "Thing", "alpha", "via adapter", "fake-alpha")

	state, private, schemaVersion, err := store.LoadState(ctx, "fake.openctl.io/v1", "Thing", "alpha")
	if err != nil {
		t.Fatalf("LoadState after Apply: %v", err)
	}
	if len(state) == 0 {
		t.Fatal("Apply did not persist opaque provider state")
	}
	if got := string(private); got != "state:fake-alpha" {
		t.Fatalf("private after Apply = %q, want state:fake-alpha", got)
	}
	if schemaVersion != 1 {
		t.Fatalf("schema version after Apply = %d, want 1", schemaVersion)
	}

	read, err := p.Get(ctx, "Thing", "alpha")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	assertResource(t, read, "fake.openctl.io/v1", "Thing", "alpha", "via adapter", "fake-alpha")

	_, private, _, err = store.LoadState(ctx, "fake.openctl.io/v1", "Thing", "alpha")
	if err != nil {
		t.Fatalf("LoadState after Get: %v", err)
	}
	if got := string(private); got != "read:fake-alpha" {
		t.Fatalf("private after Get = %q, want read:fake-alpha", got)
	}

	if err := p.Delete(ctx, "Thing", "alpha"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	state, private, schemaVersion, err = store.LoadState(ctx, "fake.openctl.io/v1", "Thing", "alpha")
	if err != nil {
		t.Fatalf("LoadState after Delete: %v", err)
	}
	if len(state) != 0 || len(private) != 0 || schemaVersion != 0 {
		t.Fatalf("state after Delete = len(state)%d len(private)%d schemaVersion%d, want empty", len(state), len(private), schemaVersion)
	}

	if _, err := p.Get(ctx, "Thing", "alpha"); err == nil {
		t.Fatal("Get after Delete succeeded, want NotFound")
	} else {
		var notFound *providers.NotFoundError
		if !errors.As(err, &notFound) {
			t.Fatalf("Get after Delete error = %T %v, want NotFoundError", err, err)
		}
	}

	// Delete remains idempotent after state has already been removed.
	if err := p.Delete(ctx, "Thing", "alpha"); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
}

func TestProviderAdapterRequiresMappedSchema(t *testing.T) {
	openctlschema.ResetExternal()
	defer openctlschema.ResetExternal()

	bin := buildFakeProvider(t)

	client, err := tfhost.Launch(bin)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	defer db.Close()

	_, err = tfhost.NewProvider(ctx, "fake", client, providerstate.New(db), []tfhost.ResourceMapping{
		{Kind: "Missing", TypeName: "fake_missing"},
	})
	if err == nil {
		t.Fatal("NewProvider succeeded for a Terraform type missing from provider schema")
	}
}

func assertResource(t *testing.T, r *protocol.Resource, apiVersion, kind, name, note, id string) {
	t.Helper()
	if r.APIVersion != apiVersion || r.Kind != kind || r.Metadata.Name != name {
		t.Fatalf("resource identity = %s %s %s, want %s %s %s", r.APIVersion, r.Kind, r.Metadata.Name, apiVersion, kind, name)
	}
	if got, _ := r.Spec["name"].(string); got != name {
		t.Fatalf("spec.name = %q, want %q", got, name)
	}
	if got, _ := r.Spec["note"].(string); got != note {
		t.Fatalf("spec.note = %q, want %q", got, note)
	}
	if got, _ := r.Status["id"].(string); got != id {
		t.Fatalf("status.id = %q, want %q", got, id)
	}
	if got, _ := r.Status["phase"].(string); got != "Ready" {
		t.Fatalf("status.phase = %q, want Ready", got)
	}
}
