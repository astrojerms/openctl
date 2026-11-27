package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestAuthorityValidation(t *testing.T) {
	for _, tt := range []struct{ name, body, mode, want string }{
		{"default", "", "mirror", ""},
		{"audit", "manifests:\n  git:\n    enabled: true\n", "mirror", ""},
		{"source", "manifests:\n  mode: gitops\n  gitops:\n    remote: /upstream\n    pull:\n      prune: true\n", "gitops", ""},
		{"legacy watcher", "manifests:\n  gitops:\n    enabled: true\n", "", "requires manifests.mode: gitops"},
		{"legacy pull", "manifests:\n  gitops:\n    pull:\n      enabled: true\n", "", "requires manifests.mode: gitops"},
		{"mixed", "manifests:\n  mode: gitops\n  git:\n    enabled: true\n  gitops:\n    remote: /upstream\n", "", "remove manifests.git"},
		{"disabled mixed", "manifests:\n  mode: gitops\n  git: {}\n  gitops:\n    remote: /upstream\n", "", "remove manifests.git"},
		{"missing remote", "manifests:\n  mode: gitops\n", "", "requires manifests.gitops.remote"},
		{"unknown", "manifests:\n  mode: both\n", "", "choose mirror"},
		{"disabled watcher", "manifests:\n  mode: gitops\n  gitops:\n    remote: /upstream\n    enabled: false\n", "", "enables the watcher"},
		{"disabled pull", "manifests:\n  mode: gitops\n  gitops:\n    remote: /upstream\n    pull:\n      enabled: false\n", "", "remove the pull block"},
		{"bad interval", "manifests:\n  mode: gitops\n  gitops:\n    remote: /upstream\n    pull:\n      interval: nope\n", "", "positive duration"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadFromFile(path)
			if tt.want != "" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("error=%v, want %q", err, tt.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ManifestMode() != tt.mode {
				t.Fatalf("mode=%q, want %q", cfg.ManifestMode(), tt.mode)
			}
		})
	}
}
