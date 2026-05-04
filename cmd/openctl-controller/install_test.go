package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveInstallPathsUsesHome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	p, err := resolveInstallPaths()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		got, want string
	}{
		{p.BinaryPath, filepath.Join(tmp, "Library", "Application Support", "openctl", "bin", "openctl-controller")},
		{p.PlistPath, filepath.Join(tmp, "Library", "LaunchAgents", "io.openctl.controller.plist")},
		{p.LogOut, filepath.Join(tmp, "Library", "Logs", "openctl", "controller.out.log")},
		{p.StateDir, filepath.Join(tmp, ".openctl", "controller")},
		{p.CLIConfigFile, filepath.Join(tmp, ".openctl", "config.yaml")},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

func TestRenderPlistContainsKeyFields(t *testing.T) {
	p := &installPaths{
		HomeDir:    "/Users/test",
		BinaryPath: "/Users/test/Library/Application Support/openctl/bin/openctl-controller",
		LogOut:     "/Users/test/Library/Logs/openctl/controller.out.log",
		LogErr:     "/Users/test/Library/Logs/openctl/controller.err.log",
	}
	out, err := renderPlist(p)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	wantSubstrings := []string{
		"<key>Label</key>",
		"<string>io.openctl.controller</string>",
		"<string>" + p.BinaryPath + "</string>",
		"<string>serve</string>",
		"<key>RunAtLoad</key>",
		"<true/>",
		"<key>KeepAlive</key>",
		"<string>" + p.LogOut + "</string>",
		"<string>" + p.LogErr + "</string>",
		"<string>" + p.HomeDir + "</string>",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(got, want) {
			t.Errorf("plist missing %q. Full output:\n%s", want, got)
		}
	}
}

func TestEnsureCLIConfigCreatesStubWhenMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := ensureCLIConfig(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- test temp path
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "controller:") {
		t.Errorf("stub missing controller section:\n%s", data)
	}
}

func TestEnsureCLIConfigLeavesExistingFileAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	original := "providers:\n  proxmox:\n    contexts: {}\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureCLIConfig(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path) // #nosec G304 -- test temp path
	if string(data) != original {
		t.Errorf("existing config rewritten — got:\n%s", data)
	}
}

func TestCopyFileAtomicAndExecutable(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(dst) // #nosec G304 -- test temp path
	if string(data) != "hello" {
		t.Errorf("contents = %q, want hello", data)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %o, want 0755", info.Mode().Perm())
	}
	// Atomic-write artifact should be cleaned up.
	if _, err := os.Stat(dst + ".new"); !os.IsNotExist(err) {
		t.Errorf("expected dst.new to be gone, got err=%v", err)
	}
}

func TestRemoveIfExistsIdempotent(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope")
	if err := removeIfExists(missing); err != nil {
		t.Errorf("remove missing: %v", err)
	}
	present := filepath.Join(dir, "yes")
	_ = os.WriteFile(present, nil, 0o600)
	if err := removeIfExists(present); err != nil {
		t.Errorf("remove existing: %v", err)
	}
	if _, err := os.Stat(present); !os.IsNotExist(err) {
		t.Errorf("file should be gone, got err=%v", err)
	}
}
