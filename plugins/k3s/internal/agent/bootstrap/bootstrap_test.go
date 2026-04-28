package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeArch(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{"x86_64", "amd64", false},
		{"amd64", "amd64", false},
		{"aarch64", "arm64", false},
		{"arm64", "arm64", false},
		{"armv7l", "armv7", false},
		{"armv7", "armv7", false},
		{"i386", "", true},
		{"riscv64", "", true},
		{"", "", true},
	}
	for _, c := range cases {
		got, err := normalizeArch(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("normalizeArch(%q): want error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeArch(%q): unexpected error: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("normalizeArch(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestResolveBinaryFindsArchSpecificFile(t *testing.T) {
	dir := t.TempDir()
	for _, arch := range []string{"amd64", "arm64", "armv7"} {
		p := filepath.Join(dir, "openctl-k3s-agent-linux-"+arch)
		if err := os.WriteFile(p, []byte("stub"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	i := &Installer{BinaryDir: dir}
	got, err := i.resolveBinary("arm64")
	if err != nil {
		t.Fatalf("resolveBinary: %v", err)
	}
	want := filepath.Join(dir, "openctl-k3s-agent-linux-arm64")
	if got != want {
		t.Errorf("resolveBinary = %q, want %q", got, want)
	}
}

func TestResolveBinaryReportsAllCandidatesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	i := &Installer{BinaryDir: dir}
	_, err := i.resolveBinary("amd64")
	if err == nil {
		t.Fatal("want error for missing binary")
	}
	// Error message should mention the dir we searched and the env var override.
	msg := err.Error()
	if !strings.Contains(msg, dir) {
		t.Errorf("error %q should mention search dir %q", msg, dir)
	}
	if !strings.Contains(msg, envAgentBinaryDir) {
		t.Errorf("error %q should mention env var %q", msg, envAgentBinaryDir)
	}
}

func TestResolveBinaryRespectsEnvVar(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "openctl-k3s-agent-linux-amd64")
	if err := os.WriteFile(p, []byte("stub"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envAgentBinaryDir, dir)

	i := &Installer{} // no explicit BinaryDir
	got, err := i.resolveBinary("amd64")
	if err != nil {
		t.Fatalf("resolveBinary: %v", err)
	}
	if got != p {
		t.Errorf("resolveBinary = %q, want %q", got, p)
	}
}

func TestEmbeddedUnitsArePopulated(t *testing.T) {
	if len(SystemdUnit) == 0 {
		t.Error("SystemdUnit is empty")
	}
	if len(OpenRCInit) == 0 {
		t.Error("OpenRCInit is empty")
	}
}
