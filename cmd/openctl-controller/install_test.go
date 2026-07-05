package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveInstallPathsDarwin(t *testing.T) {
	home := "/Users/test"
	p, err := resolveInstallPathsFor("darwin", home)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		got, want string
	}{
		{p.BinaryPath, filepath.Join(home, "Library", "Application Support", "openctl", "bin", "openctl-controller")},
		{p.AgentDir, filepath.Join(home, "Library", "Application Support", "openctl", "bin", "k3s-agents")},
		{p.UnitPath, filepath.Join(home, "Library", "LaunchAgents", "io.openctl.controller.plist")},
		{p.LogOut, filepath.Join(home, "Library", "Logs", "openctl", "controller.out.log")},
		{p.StateDir, filepath.Join(home, ".openctl", "controller")},
		{p.CLIConfigFile, filepath.Join(home, ".openctl", "config.yaml")},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

func TestResolveInstallPathsLinux(t *testing.T) {
	home := "/home/test"
	p, err := resolveInstallPathsFor("linux", home)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		got, want string
	}{
		{p.BinaryPath, filepath.Join(home, ".local", "share", "openctl", "bin", "openctl-controller")},
		{p.AgentDir, filepath.Join(home, ".local", "share", "openctl", "bin", "k3s-agents")},
		{p.UnitPath, filepath.Join(home, ".config", "systemd", "user", "openctl-controller.service")},
		{p.StateDir, filepath.Join(home, ".openctl", "controller")},
		{p.CLIConfigFile, filepath.Join(home, ".openctl", "config.yaml")},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
	// Linux uses the journal — no file log paths.
	if p.LogOut != "" || p.LogErr != "" {
		t.Errorf("linux should have empty log paths, got out=%q err=%q", p.LogOut, p.LogErr)
	}
}

func TestResolveInstallPathsUnsupported(t *testing.T) {
	if _, err := resolveInstallPathsFor("windows", "C:\\Users\\test"); err == nil {
		t.Error("expected error for unsupported platform")
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

func TestRenderSystemdUnitContainsKeyFields(t *testing.T) {
	p := &installPaths{
		HomeDir:    "/home/test",
		BinaryPath: "/home/test/.local/share/openctl/bin/openctl-controller",
	}
	out, err := renderSystemdUnit(p)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	wantSubstrings := []string{
		"[Unit]",
		"Description=openctl controller",
		"[Service]",
		"Type=simple",
		"ExecStart=" + p.BinaryPath + " serve",
		"WorkingDirectory=" + p.HomeDir,
		"Restart=on-failure",
		"[Install]",
		"WantedBy=default.target",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(got, want) {
			t.Errorf("systemd unit missing %q. Full output:\n%s", want, got)
		}
	}
}

func TestServiceManagerForSelectsByPlatform(t *testing.T) {
	cases := []struct {
		goos, want string
		ok         bool
	}{
		{"darwin", "launchd", true},
		{"linux", "systemd", true},
		{"windows", "", false},
	}
	for _, c := range cases {
		mgr, err := serviceManagerFor(c.goos)
		if c.ok {
			if err != nil {
				t.Errorf("%s: unexpected error %v", c.goos, err)
				continue
			}
			if mgr.name() != c.want {
				t.Errorf("%s: manager = %q, want %q", c.goos, mgr.name(), c.want)
			}
		} else if err == nil {
			t.Errorf("%s: expected error for unsupported platform", c.goos)
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

func TestCopyK3sAgentBinaries(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	names := []string{
		"openctl-k3s-agent-linux-amd64",
		"openctl-k3s-agent-linux-arm64",
		"openctl-k3s-agent-linux-armv7",
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(srcDir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := copyK3sAgentBinaries(srcDir, dstDir); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		path := filepath.Join(dstDir, name)
		data, err := os.ReadFile(path) // #nosec G304 -- test temp path
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != name {
			t.Errorf("%s contents = %q, want %q", name, data, name)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Errorf("%s mode = %o, want 0755", name, info.Mode().Perm())
		}
	}
}

func TestCopyK3sAgentBinariesReportsMissingBuild(t *testing.T) {
	err := copyK3sAgentBinaries(t.TempDir(), t.TempDir())
	if err == nil {
		t.Fatal("expected missing binary error")
	}
	if !strings.Contains(err.Error(), "make build-plugin-k3s-agent-linux") {
		t.Fatalf("error should explain how to build agents, got %v", err)
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
