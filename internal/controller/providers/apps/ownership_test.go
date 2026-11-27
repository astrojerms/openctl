package apps

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Execute the real ownership protocol against a deterministic Docker executable
// standing in for the daemon; no SSH, Docker daemon or infrastructure is used.
type localOwnershipRunner struct{ env []string }

func (r localOwnershipRunner) Run(ctx context.Context, _ Target, command string, input []byte) (string, error) {
	cmd := exec.CommandContext(ctx, "bash", "-euo", "pipefail", "-c", command)
	cmd.Env = r.env
	cmd.Stdin = strings.NewReader(string(input))
	out, err := cmd.Output()
	return string(out), err
}

func ownershipFixture(t *testing.T) (*Provider, string, locator) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	fake := `#!/bin/bash
set -euo pipefail
printf '%s\n' "$*" >> "$FAKE_DOCKER/trace"
case "$1 $2" in
  'info ') exit 0 ;;
  'ps -aq') if test -f "$FAKE_DOCKER/ids"; then cat "$FAKE_DOCKER/ids"; fi ;;
  'volume inspect')
    test -f "$FAKE_DOCKER/owner"
    if test "${3:-}" = --format; then cat "$FAKE_DOCKER/owner"; fi ;;
  'volume create')
    if test ! -f "$FAKE_DOCKER/owner"; then printf '%s' "${4#io.openctl.owner=}" > "$FAKE_DOCKER/owner"; fi ;;
  'inspect --format') cat "$FAKE_DOCKER/working_dir" ;;
  'rm --force') printf removed > "$FAKE_DOCKER/removed"; : > "$FAKE_DOCKER/ids" ;;
  *) exit 91 ;;
esac
`
	// docker info has one argument, while noun/verb operations have two.
	fake = strings.Replace(fake, `case "$1 $2" in`, `case "$1 ${2:-}" in`, 1)
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	p, _ := testProvider()
	p.runner = localOwnershipRunner{env: append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "FAKE_DOCKER="+dir)}
	return p, dir, locator{Target: Target{Host: "unused"}, Directory: filepath.Join(dir, "application"), Project: "openctl-media"}
}

func TestProjectOwnershipRejectsForeignAndUnmanagedContainers(t *testing.T) {
	for _, tc := range []struct {
		name, owner, ids, workingDir string
		allow                        bool
	}{
		{name: "new", allow: true},
		{name: "same owner retry", owner: owner("test-controller", "ComposeApplication", "media"), allow: true},
		{name: "foreign owner", owner: owner("test-controller", "ComposeApplication", "other"), allow: false},
		{name: "same resource from another controller", owner: owner("other-controller", "ComposeApplication", "media"), allow: false},
		{name: "unmanaged existing project", ids: "123\n", workingDir: "same", allow: false},
		{name: "owned existing project", owner: owner("test-controller", "ComposeApplication", "media"), ids: "123\n", workingDir: "same", allow: true},
		{name: "foreign working directory", owner: owner("test-controller", "ComposeApplication", "media"), ids: "123\n", workingDir: "/elsewhere", allow: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, dir, l := ownershipFixture(t)
			for file, value := range map[string]string{"owner": tc.owner, "ids": tc.ids, "working_dir": tc.workingDir} {
				if value == "" {
					continue
				}
				if value == "same" {
					value = l.Directory
				}
				if err := os.WriteFile(filepath.Join(dir, file), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := p.claimProject(context.Background(), l, owner(p.controllerID, "ComposeApplication", "media"), false)
			if (err == nil) != tc.allow {
				t.Fatalf("claim allowed=%v, error=%v", tc.allow, err)
			}
			if !tc.allow {
				trace, _ := os.ReadFile(filepath.Join(dir, "trace"))
				if strings.Contains(string(trace), "volume create") || strings.Contains(string(trace), "rm --force") {
					t.Fatalf("collision mutated Docker: %s", trace)
				}
			}
		})
	}
}

func TestDeleteRefusesForeignProjectEvenWithOwnedDirectory(t *testing.T) {
	p, dir, l := ownershipFixture(t)
	if err := os.MkdirAll(l.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	for file, value := range map[string]string{
		filepath.Join(l.Directory, ".openctl-owner"): owner("test-controller", "ComposeApplication", "media"),
		filepath.Join(dir, "owner"):                  owner("test-controller", "ComposeApplication", "other"),
		filepath.Join(dir, "ids"):                    "123\n",
	} {
		if err := os.WriteFile(file, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.save(context.Background(), "ComposeApplication", "media", l); err != nil {
		t.Fatal(err)
	}
	if err := p.Delete(context.Background(), "ComposeApplication", "media"); err == nil {
		t.Fatal("foreign project deletion succeeded")
	}
	if _, err := os.Stat(filepath.Join(dir, "removed")); !os.IsNotExist(err) {
		t.Fatal("foreign containers were removed")
	}
	saved, err := p.load(context.Background(), "ComposeApplication", "media")
	if err != nil || saved == nil {
		t.Fatal("failed deletion lost the resumable locator")
	}
}
