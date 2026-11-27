package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openctl/openctl/internal/config"
	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/storage"
	"github.com/openctl/openctl/pkg/protocol"
)

func modeGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestManifestAuthorityStartupAndFastForward(t *testing.T) {
	ctx := context.Background()
	upstream := t.TempDir()
	modeGit(t, upstream, "init", "-b", "main")
	modeGit(t, upstream, "config", "user.name", "test")
	modeGit(t, upstream, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(upstream, "README"), []byte("upstream\n"), 0600); err != nil {
		t.Fatal(err)
	}
	modeGit(t, upstream, "add", ".")
	modeGit(t, upstream, "commit", "-m", "initial")
	for _, mode := range []string{"mirror", "gitops"} {
		t.Run(mode, func(t *testing.T) {
			db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store := manifests.New(db)
			resource := &protocol.Resource{APIVersion: "test.openctl.io/v1", Kind: "Thing", Metadata: protocol.ResourceMetadata{Name: "gone"}, Spec: map[string]any{"value": "old"}}
			if err := store.Save(ctx, resource); err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{Manifests: &config.Manifests{Dir: filepath.Join(t.TempDir(), "tree"), Mode: mode}}
			if mode == "gitops" {
				cfg.Manifests.GitOps = &config.ManifestsGitOps{Remote: upstream}
			} else {
				cfg.Manifests.Git = &config.ManifestsGit{Enabled: true}
			}
			sink, mirror, repo, err := buildManifestStorage(ctx, store, cfg)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(mirror.Root(), mirror.RelPathFor(resource.APIVersion, resource.Kind, resource.Metadata.Name))
			_, statErr := os.Stat(path)
			if mode == "mirror" {
				if statErr != nil {
					t.Fatalf("mirror startup did not revive audit file: %v", statErr)
				}
				if err := sink.Save(ctx, resource); err != nil {
					t.Fatal(err)
				}
				if modeGit(t, mirror.Root(), "rev-list", "--count", "HEAD") != "1" {
					t.Fatal("mirror apply did not commit")
				}
				return
			}
			if !os.IsNotExist(statErr) {
				t.Fatalf("gitops revived removed file: %v", statErr)
			}
			before := modeGit(t, mirror.Root(), "rev-parse", "HEAD")
			resource.Spec["value"] = "applied"
			if err := sink.Save(ctx, resource); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("gitops apply wrote upstream tree: %v", err)
			}
			if got := modeGit(t, mirror.Root(), "rev-parse", "HEAD"); got != before {
				t.Fatal("gitops apply auto-committed")
			}
			if err := os.WriteFile(filepath.Join(upstream, "README"), []byte("next\n"), 0600); err != nil {
				t.Fatal(err)
			}
			modeGit(t, upstream, "add", ".")
			modeGit(t, upstream, "commit", "-m", "next")
			if err := repo.Pull(ctx); err != nil {
				t.Fatalf("fast-forward after apply: %v", err)
			}
			if modeGit(t, mirror.Root(), "rev-parse", "HEAD") != modeGit(t, upstream, "rev-parse", "HEAD") {
				t.Fatal("pull did not reach upstream")
			}
			pruner := manifests.NewPruner(store, mirror.Root(), func(ctx context.Context, av, kind, name string) error {
				return store.Delete(ctx, av, kind, name)
			})
			pruned, err := pruner.Prune(ctx)
			if err != nil || len(pruned) != 1 {
				t.Fatalf("prune after pull: %v, resources=%v", err, pruned)
			}
			if stored, err := store.Load(ctx, resource.APIVersion, resource.Kind, resource.Metadata.Name); err != nil || stored != nil {
				t.Fatalf("pruned state remains: %v, %v", stored, err)
			}
			if _, _, _, err := buildManifestStorage(ctx, store, cfg); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("restart revived removed file: %v", err)
			}
		})
	}
}

func TestServeRejectsInvalidManifestAuthorityBeforeStartup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".openctl"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".openctl", "config.yaml"), []byte("manifests:\n  gitops:\n    pull:\n      enabled: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(home, "controller-state")
	err := runServe([]string{"--dir", state})
	if err == nil || !strings.Contains(err.Error(), "requires manifests.mode: gitops") {
		t.Fatalf("startup silently accepted invalid config: %v", err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("startup touched controller state before config validation: %v", err)
	}
}

func TestRegistryRejectsExternalAppsProvider(t *testing.T) {
	for _, pc := range []*config.Provider{
		{Command: "/must-not-be-executed"},
		{Terraform: &config.TerraformProvider{}},
	} {
		cfg := &config.Config{Providers: map[string]*config.Provider{"apps": pc}}
		_, _, cleanup, err := buildRegistryFromConfig(context.Background(), nil, cfg)
		cleanup()
		if err == nil || !strings.Contains(err.Error(), "reserved") || !strings.Contains(err.Error(), "rename") {
			t.Fatalf("reserved apps provider must produce actionable error before loading: %v", err)
		}
	}
}
