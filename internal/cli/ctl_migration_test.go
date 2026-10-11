package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

func migrationTestBundle(t *testing.T) *apiv1.MigrationBundle {
	t.Helper()
	spec, err := structpb.NewStruct(map[string]any{"context": "source", "node": "pve1", "vmid": 123.0, "template": map[string]any{"name": "ubuntu"}, "cloudInit": map[string]any{"password": "confidential-password"}})
	if err != nil {
		t.Fatal(err)
	}
	return &apiv1.MigrationBundle{Version: 1, ControllerId: "source-controller", ManifestDigest: strings.Repeat("a", 64), Identity: "vm-identity", Resource: &apiv1.Resource{ApiVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine", Metadata: &apiv1.Metadata{Name: "existing", Annotations: map[string]string{"owner": "original"}}, Spec: spec}}
}

func TestMigrationExportExclusivePrivateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bundle.json")
	bundle := migrationTestBundle(t)
	completed := false
	if err := writeMigrationExport(path, func() (*apiv1.MigrationBundle, error) { return bundle, nil }, func() error { completed = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !completed {
		t.Fatal("completion not reported")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions = %o", info.Mode().Perm())
	}
	loaded, err := loadMigrationBundle(path)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(bundle, loaded) {
		t.Fatal("bundle lost desired intent or ownership evidence")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if err := writeMigrationExport(path, func() (*apiv1.MigrationBundle, error) { called = true; return nil, nil }, func() error { t.Fatal("unexpected completion"); return nil }); err == nil {
		t.Fatal("overwrote existing bundle")
	}
	if called {
		t.Fatal("contacted controller before detecting existing destination")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("existing bundle changed")
	}
}

func TestMigrationExportFailureRemovesOwnedFile(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "failed.json")
		err := writeMigrationExport(path, func() (*apiv1.MigrationBundle, error) {
			if invalid {
				return &apiv1.MigrationBundle{Version: 9}, nil
			}
			return nil, errors.New("unavailable")
		}, func() error { t.Fatal("completion after failure"); return nil })
		if err == nil {
			t.Fatal("expected failure")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("partial export retained: %v", err)
		}
	}
}

func TestMigrationBundleStrictValidation(t *testing.T) {
	valid := migrationTestBundle(t)
	data, err := protojson.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"unknown":        strings.TrimSuffix(string(data), "}") + `,"secretUnknown":"confidential-password"}`,
		"nested unknown": strings.Replace(string(data), `"metadata":`, `"metadata":{"confidential-password":"hidden"},"replacedMetadata":`, 1),
		"trailing":       string(data) + `{}`,
		"malformed":      `{"version":"confidential-password"}`,
	}
	for name, mutate := range map[string]func(*apiv1.MigrationBundle){
		"version":    func(b *apiv1.MigrationBundle) { b.Version = 2 },
		"controller": func(b *apiv1.MigrationBundle) { b.ControllerId = "" },
		"digest":     func(b *apiv1.MigrationBundle) { b.ManifestDigest = "bad" },
		"identity":   func(b *apiv1.MigrationBundle) { b.Identity = "" },
		"resource":   func(b *apiv1.MigrationBundle) { b.Resource = nil },
		"metadata":   func(b *apiv1.MigrationBundle) { b.Resource.Metadata = nil },
		"spec":       func(b *apiv1.MigrationBundle) { b.Resource.Spec = nil },
		"status":     func(b *apiv1.MigrationBundle) { b.Resource.Status = &structpb.Struct{} },
	} {
		b := proto.Clone(valid).(*apiv1.MigrationBundle)
		mutate(b)
		encoded, err := protojson.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		cases[name] = string(encoded)
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bundle.json")
			writeFile(t, path, content)
			if _, err := loadMigrationBundle(path); err == nil {
				t.Fatal("invalid bundle accepted")
			} else if strings.Contains(err.Error(), "confidential-password") {
				t.Fatal("error leaked secret")
			}
		})
	}
}

func TestMigrationAdoptionSingleYAMLAndCUE(t *testing.T) {
	yaml := "apiVersion: proxmox.openctl.io/v1\nkind: VirtualMachine\nmetadata:\n  name: existing\nspec:\n  node: pve1\n  template:\n    name: ubuntu\n  cpu:\n    cores: 2\n  memory:\n    size: 4096\n"
	cue := `apiVersion: "proxmox.openctl.io/v1"
kind: "VirtualMachine"
metadata: name: "existing"
spec: {
 node: "pve1"
 template: name: "ubuntu"
 cpu: cores: 2
 memory: size: 4096
}
`
	for ext, content := range map[string]string{".yaml": yaml, ".cue": cue} {
		t.Run(ext, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "existing"+ext)
			writeFile(t, path, content)
			req, err := loadMigrationAdoption(path)
			if err != nil {
				t.Fatal(err)
			}
			if req.Bundle != nil || req.Resource == nil || req.Resource.Metadata.Name != "existing" || req.Resource.Spec.Fields["cpu"].GetStructValue().Fields["cores"].GetNumberValue() != 2 {
				t.Fatal("manifest intent not preserved")
			}
			if _, err := loadMigrationBundle(path); err == nil {
				t.Fatal("forget accepted manifest without source evidence")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "multiple.yaml")
	writeFile(t, path, yaml+"---\n"+yaml)
	if _, err := loadMigrationAdoption(path); err == nil {
		t.Fatal("multiple manifests silently accepted")
	}
	writeFile(t, path, "apiVersion: proxmox.openctl.io/v1\nkind: VirtualMachine\nmetadata:\n  name: bad\nspec:\n  cpu:\n    cores: confidential-password\n")
	if _, err := loadMigrationAdoption(path); err == nil {
		t.Fatal("invalid schema accepted")
	} else if strings.Contains(err.Error(), "confidential-password") {
		t.Fatal("validation leaked secret")
	}
}
