package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

type migrationAdoptionRPC struct {
	apiv1.UnimplementedResourceServiceServer
	calls    atomic.Int32
	request  atomic.Pointer[apiv1.AdoptResourceRequest]
	replaced bool
}

func (s *migrationAdoptionRPC) AdoptResource(_ context.Context, req *apiv1.AdoptResourceRequest) (*apiv1.AdoptResourceResponse, error) {
	s.calls.Add(1)
	s.request.Store(req)
	if s.replaced {
		return nil, status.Error(codes.FailedPrecondition, "live identity differs from reviewed identity")
	}
	return &apiv1.AdoptResourceResponse{}, nil
}

func TestMigrationAdoptionReviewedIdentity(t *testing.T) {
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
			// No controller configuration: missing preconditions must fail
			// locally, not attempt to dial a controller.
			previous := globalConfig
			globalConfig = nil
			t.Cleanup(func() { globalConfig = previous })
			for _, flags := range [][]string{nil, {"--expected-identity", ""}, {"--expected-identity", " \t\n"}} {
				cmd := newCtlAdoptCommand()
				cmd.SetArgs(append([]string{"-f", path}, flags...))
				cmd.SetOut(new(bytes.Buffer))
				cmd.SetErr(new(bytes.Buffer))
				if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--expected-identity is required") {
					t.Fatalf("missing reviewed identity: %v", err)
				}
			}
			for _, replaced := range []bool{false, true} {
				handler := &migrationAdoptionRPC{replaced: replaced}
				configureControllerRPC(t, func(server *grpc.Server) {
					apiv1.RegisterResourceServiceServer(server, handler)
				})
				identity := " opaque:fingerprint / exact \t"
				cmd := newCtlAdoptCommand()
				cmd.SetArgs([]string{"-f", path, "--provider-context", "destination", "--expected-identity", identity})
				var output bytes.Buffer
				cmd.SetOut(&output)
				cmd.SetErr(new(bytes.Buffer))
				err := cmd.Execute()
				req := handler.request.Load()
				if handler.calls.Load() != 1 || req == nil || req.ExpectedIdentity != identity || req.ProviderContext != "destination" || req.Resource == nil {
					t.Fatal("reviewed identity or desired intent did not reach isolated RPC exactly")
				}
				if replaced {
					if err == nil || !strings.Contains(err.Error(), "FailedPrecondition") || strings.Contains(output.String(), "Resource adopted") {
						t.Fatalf("replacement reported as success: %v, %q", err, output.String())
					}
				} else if err != nil || !strings.Contains(output.String(), "Resource adopted") {
					t.Fatalf("adoption did not succeed: %v, %q", err, output.String())
				}
			}
		})
	}
}

func TestMigrationAdoptionBundleIdentity(t *testing.T) {
	bundle := migrationTestBundle(t)
	data, err := protojson.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "bundle.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, identity := range []string{"", "different", " " + bundle.Identity} {
		previous := globalConfig
		globalConfig = nil
		cmd := newCtlAdoptCommand()
		cmd.SetArgs([]string{"-f", path, "--expected-identity", identity})
		cmd.SetOut(new(bytes.Buffer))
		cmd.SetErr(new(bytes.Buffer))
		err := cmd.Execute()
		globalConfig = previous
		if err == nil || !strings.Contains(err.Error(), "must exactly match") {
			t.Fatalf("bundle mismatch not rejected locally: %v", err)
		}
	}
	for _, explicit := range []bool{false, true} {
		handler := &migrationAdoptionRPC{}
		configureControllerRPC(t, func(server *grpc.Server) {
			apiv1.RegisterResourceServiceServer(server, handler)
		})
		args := []string{"-f", path}
		if explicit {
			args = append(args, "--expected-identity", bundle.Identity)
		}
		cmd := newCtlAdoptCommand()
		cmd.SetArgs(args)
		cmd.SetOut(new(bytes.Buffer))
		cmd.SetErr(new(bytes.Buffer))
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		req := handler.request.Load()
		if handler.calls.Load() != 1 || req == nil || !proto.Equal(req.Bundle, bundle) {
			t.Fatal("bundle not forwarded intact")
		}
		expected := ""
		if explicit {
			expected = bundle.Identity
		}
		if req.ExpectedIdentity != expected {
			t.Fatal("explicit bundle precondition not preserved")
		}
	}
}
