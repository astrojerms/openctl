package secrets

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func newFileRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	dir := t.TempDir()
	reg := NewRegistry()
	RegisterBuiltins(reg, dir)
	return reg, dir
}

func writeSecret(t *testing.T, dir, name, val string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(val), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
}

// A $secret with the file sugar resolves to the file contents.
func TestResolve_FileSugar(t *testing.T) {
	reg, dir := newFileRegistry(t)
	writeSecret(t, dir, "db.pw", "hunter2\n") // trailing newline trimmed

	in := map[string]any{
		"cloudInit": map[string]any{
			"user":     "ubuntu",
			"password": map[string]any{SecretMarker: map[string]any{"file": "db.pw"}},
		},
	}
	out, err := New(reg).Resolve(context.Background(), in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	ci := out["cloudInit"].(map[string]any)
	if ci["password"] != "hunter2" {
		t.Errorf("password = %v, want hunter2", ci["password"])
	}
	if ci["user"] != "ubuntu" {
		t.Errorf("non-secret field mangled: user = %v", ci["user"])
	}
}

// The env sugar resolves to the environment variable.
func TestResolve_EnvSugar(t *testing.T) {
	reg, _ := newFileRegistry(t)
	t.Setenv("DB01_PASSWORD", "s3cret")

	in := map[string]any{"password": map[string]any{SecretMarker: map[string]any{"env": "DB01_PASSWORD"}}}
	out, err := New(reg).Resolve(context.Background(), in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if out["password"] != "s3cret" {
		t.Errorf("password = %v, want s3cret", out["password"])
	}
}

// The canonical {provider, key} form resolves the same as the sugar.
func TestResolve_CanonicalForm(t *testing.T) {
	reg, dir := newFileRegistry(t)
	writeSecret(t, dir, "tok", "abc")

	in := map[string]any{"t": map[string]any{SecretMarker: map[string]any{"provider": "file", "key": "tok"}}}
	out, err := New(reg).Resolve(context.Background(), in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if out["t"] != "abc" {
		t.Errorf("t = %v, want abc", out["t"])
	}
}

// Resolve does not mutate its input (the raw manifest must survive intact for
// persistence).
func TestResolve_DoesNotMutateInput(t *testing.T) {
	reg, dir := newFileRegistry(t)
	writeSecret(t, dir, "db.pw", "hunter2")

	marker := map[string]any{SecretMarker: map[string]any{"file": "db.pw"}}
	in := map[string]any{"password": marker}

	if _, err := New(reg).Resolve(context.Background(), in); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// The original marker map must be untouched — this is what gets persisted.
	got := in["password"].(map[string]any)
	if !reflect.DeepEqual(got, map[string]any{SecretMarker: map[string]any{"file": "db.pw"}}) {
		t.Errorf("input mutated: %+v", got)
	}
}

// Nested arrays and objects are traversed.
func TestResolve_NestedAndArrays(t *testing.T) {
	reg, dir := newFileRegistry(t)
	writeSecret(t, dir, "a", "AAA")
	writeSecret(t, dir, "b", "BBB")

	in := map[string]any{
		"list": []any{
			map[string]any{"x": map[string]any{SecretMarker: map[string]any{"file": "a"}}},
			map[string]any{"y": map[string]any{SecretMarker: map[string]any{"file": "b"}}},
		},
	}
	out, err := New(reg).Resolve(context.Background(), in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	list := out["list"].([]any)
	if list[0].(map[string]any)["x"] != "AAA" || list[1].(map[string]any)["y"] != "BBB" {
		t.Errorf("nested/array secrets not resolved: %+v", list)
	}
}

// An unknown provider is a loud error, not a silent empty value.
func TestResolve_UnknownProvider(t *testing.T) {
	reg, _ := newFileRegistry(t)
	in := map[string]any{"p": map[string]any{SecretMarker: map[string]any{"provider": "vault", "key": "x"}}}
	if _, err := New(reg).Resolve(context.Background(), in); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

// A missing file / unset env is a loud error.
func TestResolve_MissingSource(t *testing.T) {
	reg, _ := newFileRegistry(t)
	in := map[string]any{"p": map[string]any{SecretMarker: map[string]any{"file": "nope"}}}
	if _, err := New(reg).Resolve(context.Background(), in); err == nil {
		t.Fatal("expected error for missing file")
	}
}

// A malformed marker (no provider/key, no sugar) errors.
func TestResolve_MalformedMarker(t *testing.T) {
	reg, _ := newFileRegistry(t)
	in := map[string]any{"p": map[string]any{SecretMarker: map[string]any{"nonsense": "x"}}}
	if _, err := New(reg).Resolve(context.Background(), in); err == nil {
		t.Fatal("expected error for malformed marker")
	}
}

// The file provider confines keys to its root — traversal escapes are rejected.
func TestFileProvider_RejectsTraversal(t *testing.T) {
	reg, dir := newFileRegistry(t)
	// A secret sitting outside the root that a traversal would try to reach.
	parent := filepath.Dir(dir)
	if err := os.WriteFile(filepath.Join(parent, "outside"), []byte("leak"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	for _, key := range []string{"../outside", "/etc/passwd"} {
		in := map[string]any{"p": map[string]any{SecretMarker: map[string]any{"file": key}}}
		if _, err := New(reg).Resolve(context.Background(), in); err == nil {
			t.Errorf("key %q should be rejected", key)
		}
	}
}

// HasSecrets detects markers without resolving; false for a secret-free spec.
func TestHasSecrets(t *testing.T) {
	with := map[string]any{"a": map[string]any{"b": map[string]any{SecretMarker: map[string]any{"file": "x"}}}}
	if !HasSecrets(with) {
		t.Error("HasSecrets = false, want true")
	}
	without := map[string]any{"a": map[string]any{"b": "plain"}, "list": []any{1, "two"}}
	if HasSecrets(without) {
		t.Error("HasSecrets = true, want false")
	}
	if HasSecrets(nil) {
		t.Error("HasSecrets(nil) = true, want false")
	}
}

// A spec with no secrets round-trips unchanged.
func TestResolve_NoSecretsPassThrough(t *testing.T) {
	reg, _ := newFileRegistry(t)
	in := map[string]any{"cpu": map[string]any{"cores": float64(4)}, "name": "vm0"}
	out, err := New(reg).Resolve(context.Background(), in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Errorf("secret-free spec changed: %+v", out)
	}
}
