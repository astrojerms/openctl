package apps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComposePreviewRequiresSupportedToolchainWithoutSavingState(t *testing.T) {
	for _, tc := range []struct {
		version string
		allowed bool
	}{
		{"2.40.3+ds1-0ubuntu1", false},
		{"5.7.9", false},
		{"5.8.0", true},
		{"v5.8.1\n", true},
		{"5.8.1+distribution", true},
		{"6.0.0", true},
		{"5.8.0-rc1", false},
		{"private-version-output", false},
	} {
		t.Run(tc.version, func(t *testing.T) {
			p, state := testProvider()
			p.runner = fakeRunner(func(context.Context, Target, string, []byte) (string, error) {
				return tc.version, nil
			})
			preview, err := p.DryRun(t.Context(), composeResource())
			if (err == nil) != tc.allowed || (preview != nil) != tc.allowed {
				t.Fatalf("preview availability does not follow the supported toolchain: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "private-version-output") {
				t.Fatal("prerequisite diagnostics exposed untrusted command output")
			}
			if len(state.values) != 0 {
				t.Fatal("read-only prerequisite review claimed application ownership")
			}
		})
	}
}

func TestUnsupportedComposePreservesInstalledFiles(t *testing.T) {
	t.Setenv("OPENCTL_COMPOSE_VERSION", "2.40.3")
	p, l, _ := composeFixture(t)
	installed := map[string]string{"compose.yaml": "original Compose declaration\n", ".env": "ORIGINAL=unchanged\n"}
	for name, content := range installed {
		if err := os.WriteFile(filepath.Join(l.Directory, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.applyCompose(t.Context(), l, spec{Compose: "services:\n  web:\n    image: proposed\n", Environment: "NEW=not-published\n"}); err == nil {
		t.Fatal("unsupported Compose accepted a rollout")
	}
	for name, content := range installed {
		got, err := os.ReadFile(filepath.Join(l.Directory, name))
		if err != nil || string(got) != content {
			t.Fatalf("prerequisite failure replaced installed %s: %v", name, err)
		}
	}
	assertNoComposeStages(t, l)
}
