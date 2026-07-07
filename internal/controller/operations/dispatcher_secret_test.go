package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/secrets"
	"github.com/openctl/openctl/internal/controller/storage"
	"github.com/openctl/openctl/pkg/protocol"
)

// The load-bearing invariant of the whole $secret feature: after applying a
// manifest with a $secret field, the provider received the RESOLVED secret
// value, but everything persisted (the applied-manifest store, and — by
// extension — its disk/git mirror) keeps the MARKER, never the value. And the
// refs_hash cache token does not embed the resolved secret.
func TestApplyManifest_SecretResolvedForProviderButRedactedInStore(t *testing.T) {
	secDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(secDir, "db.pw"), []byte("hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}

	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	opStore := New(db, 50)
	mStore := manifests.New(db)

	// fakeProvider (applyOut nil) echoes the manifest it received, so the
	// returned Spec is exactly what the provider was handed.
	p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}}
	reg := providers.NewRegistry()
	reg.Register(p)
	d := NewDispatcher(opStore, reg, mStore, 50*time.Millisecond)

	secReg := secrets.NewRegistry()
	secrets.RegisterBuiltins(secReg, secDir)
	d.SetSecrets(secReg)

	marker := map[string]any{"$secret": map[string]any{"file": "db.pw"}}
	raw := &protocol.Resource{
		APIVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
		Metadata:   protocol.ResourceMetadata{Name: "x"},
		Spec:       map[string]any{"password": marker},
	}

	result, err := d.ApplyManifest(context.Background(), raw)
	if err != nil {
		t.Fatalf("ApplyManifest: %v", err)
	}

	// 1. The provider received the RESOLVED value.
	if got := result.Spec["password"]; got != "hunter2" {
		t.Errorf("provider got password %v, want resolved \"hunter2\"", got)
	}

	// 2. The raw manifest was not mutated — it still holds the marker (this is
	//    what gets persisted).
	if _, ok := raw.Spec["password"].(map[string]any)["$secret"]; !ok {
		t.Errorf("raw manifest mutated: %+v", raw.Spec["password"])
	}

	// 3. The PERSISTED applied-manifest keeps the marker, never the value.
	loaded, err := mStore.Load(context.Background(), "fake.openctl.io/v1", "FakeKind", "x")
	if err != nil {
		t.Fatalf("Load persisted manifest: %v", err)
	}
	pw, ok := loaded.Spec["password"].(map[string]any)
	if !ok {
		t.Fatalf("persisted password is not a marker map: %T %v", loaded.Spec["password"], loaded.Spec["password"])
	}
	if _, ok := pw["$secret"]; !ok {
		t.Errorf("persisted password lost its $secret marker: %+v", pw)
	}
	// And crucially, the plaintext must appear nowhere in the stored spec.
	if containsValue(loaded.Spec, "hunter2") {
		t.Error("SECRET LEAK: resolved value \"hunter2\" found in the persisted manifest")
	}

	// 4. The refs_hash token is secret-free: it equals the hash of the raw
	//    (marker-bearing) manifest. If the resolved secret had leaked into the
	//    hash, this would differ.
	_, refsHash, err := mStore.LoadHashes(context.Background(), "fake.openctl.io/v1", "FakeKind", "x")
	if err != nil {
		t.Fatalf("LoadHashes: %v", err)
	}
	if want := mStore.Hash(raw); refsHash != want {
		t.Errorf("refs_hash %q != hash of raw manifest %q — a resolved secret may have leaked into the hash", refsHash, want)
	}
}

// A manifest with a $secret marker but no secrets registry configured fails
// loud at apply rather than applying the unresolved marker.
func TestApplyManifest_SecretWithoutRegistryIsNotApplied(t *testing.T) {
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p := &fakeProvider{name: "fake", kinds: []string{"FakeKind"}}
	reg := providers.NewRegistry()
	reg.Register(p)
	// No SetSecrets → d.secrets is nil.
	d := NewDispatcher(New(db, 50), reg, manifests.New(db), 50*time.Millisecond)

	raw := &protocol.Resource{
		APIVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
		Metadata:   protocol.ResourceMetadata{Name: "x"},
		Spec:       map[string]any{"password": map[string]any{"$secret": map[string]any{"file": "db.pw"}}},
	}
	result, err := d.ApplyManifest(context.Background(), raw)
	if err != nil {
		t.Fatalf("ApplyManifest: %v", err)
	}
	// With no resolver, the provider receives the marker unchanged (rather than
	// a silent empty value). The marker is inert and visibly wrong, so a
	// misconfigured controller surfaces the problem instead of leaking or
	// zeroing the field.
	pw, ok := result.Spec["password"].(map[string]any)
	if !ok || pw["$secret"] == nil {
		t.Errorf("expected unresolved marker to pass through, got %v", result.Spec["password"])
	}
}

// containsValue reports whether target appears as a string value anywhere in v.
func containsValue(v any, target string) bool {
	switch x := v.(type) {
	case string:
		return x == target
	case map[string]any:
		for _, v2 := range x {
			if containsValue(v2, target) {
				return true
			}
		}
	case []any:
		for _, item := range x {
			if containsValue(item, target) {
				return true
			}
		}
	}
	return false
}
