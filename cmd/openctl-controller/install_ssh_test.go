package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSSHTarget(t *testing.T) {
	cases := []struct {
		raw                string
		wantUser, wantHost string
		wantPort           int
		wantErr            bool
	}{
		{"ssh://root@host.lan", "root", "host.lan", 22, false},
		{"ssh://admin@10.0.0.5:2222", "admin", "10.0.0.5", 2222, false},
		{"http://host", "", "", 0, true}, // wrong scheme
		{"ssh://host", "", "", 0, false}, // user defaults to $USER (set below)
		{"ssh://root@host:notaport", "", "", 0, true},
	}
	t.Setenv("USER", "deployer")
	for _, c := range cases {
		got, err := parseSSHTarget(c.raw)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: expected error", c.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error %v", c.raw, err)
			continue
		}
		if c.raw == "ssh://host" {
			// user came from $USER
			if got.User != "deployer" {
				t.Errorf("%q: user = %q, want deployer (from $USER)", c.raw, got.User)
			}
			continue
		}
		if got.User != c.wantUser || got.Host != c.wantHost || got.Port != c.wantPort {
			t.Errorf("%q: got %+v, want user=%s host=%s port=%d", c.raw, got, c.wantUser, c.wantHost, c.wantPort)
		}
	}
}

func TestParseSSHTargetMissingUser(t *testing.T) {
	t.Setenv("USER", "")
	if _, err := parseSSHTarget("ssh://host.lan"); err == nil {
		t.Error("expected error when no user and $USER unset")
	}
}

func TestResolveSSHKeyPrefersFlag(t *testing.T) {
	if got, _ := resolveSSHKey("/custom/key"); got != "/custom/key" {
		t.Errorf("key = %q, want /custom/key", got)
	}
}

func TestResolveSSHKeyFindsDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(sshDir, "id_ed25519")
	if err := os.WriteFile(keyPath, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveSSHKey("")
	if err != nil {
		t.Fatal(err)
	}
	if got != keyPath {
		t.Errorf("key = %q, want %q", got, keyPath)
	}
}

func TestResolveSSHKeyNoneFound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := resolveSSHKey(""); err == nil {
		t.Error("expected error when no default key exists")
	}
}

// fakeRunner records the commands and uploads issued by deployController.
type fakeRunner struct {
	unameOut string
	runErr   error
	commands []string
	uploads  map[string][]byte
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{unameOut: "x86_64\n", uploads: map[string][]byte{}}
}

func (f *fakeRunner) Run(cmd string) (string, error) {
	f.commands = append(f.commands, "run:"+cmd)
	if strings.Contains(cmd, "uname") {
		return f.unameOut, f.runErr
	}
	return "", f.runErr
}

func (f *fakeRunner) RunSudo(cmd string) (string, error) {
	f.commands = append(f.commands, "sudo:"+cmd)
	return "", f.runErr
}

func (f *fakeRunner) UploadSudo(data []byte, path string, _ os.FileMode) error {
	f.commands = append(f.commands, "upload:"+path)
	f.uploads[path] = data
	return nil
}

func TestDetectRemoteArch(t *testing.T) {
	cases := []struct {
		uname, want string
		wantErr     bool
	}{
		{"x86_64\n", "amd64", false},
		{"aarch64\n", "arm64", false},
		{"armv7l\n", "", true},
	}
	for _, c := range cases {
		r := newFakeRunner()
		r.unameOut = c.uname
		got, err := detectRemoteArch(r)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: expected error", c.uname)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q: got %q, %v; want %q", c.uname, got, err, c.want)
		}
	}
}

func TestRenderSystemdSystemUnit(t *testing.T) {
	out, err := renderSystemdSystemUnit()
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{
		"Description=openctl controller",
		"ExecStart=" + remoteBinPath + " serve --dir " + remoteStateDir,
		"--listen 0.0.0.0:9444",
		"--http-listen 0.0.0.0:9445",
		"Restart=on-failure",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("system unit missing %q. Full:\n%s", want, got)
		}
	}
}

func TestFindLinuxControllerBinary(t *testing.T) {
	// Put a fake binary in <tmp>/bin and run from <tmp>.
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := controllerBin + "-linux-amd64"
	if err := os.WriteFile(filepath.Join(binDir, name), []byte("elf"), 0o755); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	got, err := findLinuxControllerBinary("amd64")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if filepath.Base(got) != name {
		t.Errorf("found %q, want basename %q", got, name)
	}

	if _, err := findLinuxControllerBinary("arm64"); err == nil {
		t.Error("expected error for missing arm64 binary")
	}
}

func TestDeployControllerSequence(t *testing.T) {
	r := newFakeRunner()
	binary := []byte("BINARY")
	unit := []byte("UNIT")
	if err := deployController(r, binary, unit); err != nil {
		t.Fatalf("deploy: %v", err)
	}

	// Binary and unit landed at the right paths with the right content.
	if string(r.uploads[remoteBinPath]) != "BINARY" {
		t.Errorf("binary upload = %q", r.uploads[remoteBinPath])
	}
	if string(r.uploads[remoteUnitPath]) != "UNIT" {
		t.Errorf("unit upload = %q", r.uploads[remoteUnitPath])
	}

	// The command sequence: mkdir, (uploads), daemon-reload, enable --now, in order.
	joined := strings.Join(r.commands, "\n")
	for _, want := range []string{
		"sudo:mkdir -p " + remoteStateDir,
		"upload:" + remoteBinPath,
		"upload:" + remoteUnitPath,
		"sudo:systemctl daemon-reload",
		"sudo:systemctl enable --now " + remoteUnitName,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("command sequence missing %q. Got:\n%s", want, joined)
		}
	}
	// daemon-reload must come before enable.
	reloadIdx := indexOf(r.commands, "sudo:systemctl daemon-reload")
	enableIdx := indexOf(r.commands, "sudo:systemctl enable --now "+remoteUnitName)
	if reloadIdx < 0 || enableIdx < 0 || reloadIdx > enableIdx {
		t.Errorf("daemon-reload (%d) should precede enable (%d)", reloadIdx, enableIdx)
	}
}

func TestDeployControllerStopsOnError(t *testing.T) {
	r := newFakeRunner()
	r.runErr = fmt.Errorf("boom")
	if err := deployController(r, []byte("b"), []byte("u")); err == nil {
		t.Fatal("expected error to propagate from the first RunSudo")
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
