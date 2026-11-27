package instance

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestExclusiveLockAndRetainedFile(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := Acquire(dir); err == nil {
		_ = second.Close()
		t.Fatal("second acquisition succeeded while kernel lock was held")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, Filename)); err != nil {
		t.Fatalf("lock file must remain to avoid split-inode locking: %v", err)
	}
	second, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
}

func TestLockRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, Filename)); err != nil {
		t.Fatal(err)
	}
	if lock, err := Acquire(dir); err == nil {
		_ = lock.Close()
		t.Fatal("lock followed symlink")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "untouched" {
		t.Fatal("symlink target changed")
	}
}

func TestLockAcrossProcesses(t *testing.T) {
	if dir := os.Getenv("OPENCTL_TEST_LOCK_DIR"); dir != "" {
		_, err := Acquire(dir)
		blocked := os.Getenv("OPENCTL_TEST_LOCK_BLOCKED") == "true"
		if (err != nil) != blocked {
			os.Exit(2)
		}
		// Deliberately exit without Close: kernel ownership must disappear.
		os.Exit(0)
	}
	dir := t.TempDir()
	lock, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	for _, blocked := range []bool{true, false} {
		if !blocked {
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestLockAcrossProcesses$")
		expect := "false"
		if blocked {
			expect = "true"
		}
		cmd.Env = append(os.Environ(), "OPENCTL_TEST_LOCK_DIR="+dir, "OPENCTL_TEST_LOCK_BLOCKED="+expect)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cross-process lock expectation failed: %v: %s", err, output)
		}
	}
	released, err := Acquire(dir)
	if err != nil {
		t.Fatalf("exited child retained lock: %v", err)
	}
	defer released.Close()
}
