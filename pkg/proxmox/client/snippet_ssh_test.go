package client

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestUploadSnippetRejectsUnsafeInputs(t *testing.T) {
	for _, tc := range []struct {
		name, host, user, storage, filename string
	}{
		{"host option", "-oProxyCommand=touch", "root", "local", "vm.yaml"},
		{"host shell", "node;touch /tmp/bad", "root", "local", "vm.yaml"},
		{"host user override", "other@node", "root", "local", "vm.yaml"},
		{"user option", "node", "-bad", "local", "vm.yaml"},
		{"user shell", "node", "root;true", "local", "vm.yaml"},
		{"storage traversal", "node", "root", "../local", "vm.yaml"},
		{"storage volume injection", "node", "root", "local:images", "vm.yaml"},
		{"filename traversal", "node", "root", "local", "../vm.yaml"},
		{"filename absolute", "node", "root", "local", "/tmp/vm.yaml"},
		{"filename option", "node", "root", "local", "-vm.yaml"},
		{"filename shell", "node", "root", "local", "vm';touch bad.yaml"},
		{"filename newline", "node", "root", "local", "vm\n.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := New("https://unused.invalid", "", "")
			c.SetSnippetSSH(map[string]string{"pve": tc.host}, tc.user, "")
			if err := c.UploadSnippet(context.Background(), "pve", tc.storage, tc.filename, "secret"); err == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
}

func TestUploadSnippetRequiresExplicitNode(t *testing.T) {
	c := New("https://pve1:8006", "", "")
	hosts := map[string]string{"pve1": "node1"}
	c.SetSnippetSSH(hosts, "", "")
	hosts["pve2"] = "node2"
	if err := c.UploadSnippet(context.Background(), "pve2", "local", "vm.yaml", "secret"); err == nil || !strings.Contains(err.Error(), "snippetSSH.hosts") {
		t.Fatalf("missing explicit mapping should require configuration, got %v", err)
	}
}

// Run the production remote shell against isolated real files. Only pvesm's
// storage lookup is supplied locally; creation, writes, permissions, cleanup and
// publication use the actual shell/filesystem tools rather than an SSH echo.
func runSnippetScript(t *testing.T, destination, content string, expected int, failWrite bool) error {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the Proxmox remote script requires Linux tools, including GNU mv -T")
	}
	bin := t.TempDir()
	resolver := "#!/bin/sh\n[ \"$1\" = path ] && [ \"$2\" = local:snippets/vm.yaml ] || exit 1\nprintf '%s\\n' \"$SNIPPET_TEST_PATH\"\n"
	if err := os.WriteFile(filepath.Join(bin, "pvesm"), []byte(resolver), 0755); err != nil {
		t.Fatal(err)
	}
	if failWrite {
		if err := os.WriteFile(filepath.Join(bin, "cat"), []byte("#!/bin/sh\nprintf partial\nexit 1\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-c", snippetUploadScript, "sh", "local:snippets/vm.yaml", strconv.Itoa(expected))
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "SNIPPET_TEST_PATH="+destination)
	cmd.Stdin = strings.NewReader(content)
	return cmd.Run()
}

func TestSnippetRemoteAtomicReplacement(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mounted share ' with spaces", "snippets")
	if err := os.MkdirAll(dir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "vm.yaml")
	if err := os.WriteFile(path, []byte("previous"), 0600); err != nil {
		t.Fatal(err)
	}
	content := "#cloud-config\nquoted: 'value'\n\x00binary\n"
	if err := runSnippetScript(t, path, content, len(content), false); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != content {
		t.Fatalf("exact content not published: %q, %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatalf("snippet permissions: %v, %v", info, err)
	}
	info, err = os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0750 {
		t.Fatalf("existing directory permissions changed: %v, %v", info, err)
	}
	assertNoSnippetTemps(t, dir)
}

func TestSnippetRemoteFailurePreservesDestination(t *testing.T) {
	for _, tc := range []struct {
		name      string
		expected  int
		failWrite bool
	}{
		{"short input", 100, false},
		{"failed write", 7, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "vm.yaml")
			if err := os.WriteFile(path, []byte("previous"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := runSnippetScript(t, path, "partial", tc.expected, tc.failWrite); err == nil {
				t.Fatal("incomplete write succeeded")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != "previous" {
				t.Fatalf("existing destination was modified: %q, %v", got, err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("existing destination permissions changed: %v, %v", info, err)
			}
			assertNoSnippetTemps(t, dir)
		})
	}
}

func TestSnippetRemoteRejectsRelativeStoragePath(t *testing.T) {
	if err := runSnippetScript(t, "relative/snippets/vm.yaml", "content", 7, false); err == nil {
		t.Fatal("relative storage path accepted")
	}
}

func assertNoSnippetTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".openctl-snippet.") {
			t.Fatalf("temporary file leaked: %s", entry.Name())
		}
	}
}

func TestSnippetShellQuotePreservesLiteralWords(t *testing.T) {
	for _, value := range []string{"", "single'quote", "spaces and\nnewlines", "$(exit 91); exit 92", snippetUploadScript} {
		cmd := exec.Command("sh", "-c", "printf '%s' "+shellQuote(value))
		got, err := cmd.Output()
		if err != nil || string(got) != value {
			t.Fatalf("remote shell did not preserve literal word %q: %q, %v", value, got, err)
		}
	}
}

func TestSnippetRemoteDoesNotWriteIntoDestinationDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vm.yaml")
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := runSnippetScript(t, path, "content", 7, false); err == nil {
		t.Fatal("directory accepted as snippet destination")
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		t.Fatalf("destination directory modified: %v, %v", entries, err)
	}
	assertNoSnippetTemps(t, dir)
}
