package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openctl/openctl/internal/controller/storage"
)

func captureRecoveryOutput(t *testing.T, run func() error) (string, string, error) {
	t.Helper()
	stdout, err := os.CreateTemp(t.TempDir(), "stdout-")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.CreateTemp(t.TempDir(), "stderr-")
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdout, stderr
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()
	runErr := run()
	out, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	errOut, err := os.ReadFile(stderr.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(out), string(errOut), runErr
}

func TestRecoveryCommandsRoundTripAndWarnings(t *testing.T) {
	base := t.TempDir()
	root, state := filepath.Join(base, "source"), filepath.Join(base, "external-state")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte("manifestDir: /external/manifests\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(context.Background(), filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO controller_meta(key,value) VALUES ('migration_controller_id','cli-roundtrip-id')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(base, "backup.tar")
	_, warning, err := captureRecoveryOutput(t, func() error {
		return runBackup([]string{"--output", archive, "--root", root, "--dir", state})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"plaintext credentials", "External SSH keys", "NOT backed up"} {
		if !strings.Contains(warning, want) {
			t.Fatalf("missing backup warning %q", want)
		}
	}
	newRoot := filepath.Join(base, "new-root")
	out, _, err := captureRecoveryOutput(t, func() error {
		return runRestore([]string{"--input", archive, "--root", newRoot})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{newRoot, filepath.Join(newRoot, "controller"), root, state, "NOT started", "NOT rewritten", "before starting", "never run both copies"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing restore guidance %q", want)
		}
	}
	if err := storage.ValidateSnapshot(context.Background(), filepath.Join(newRoot, "controller", "state.db")); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryCommandsRejectMissingAndPositionalArguments(t *testing.T) {
	for _, run := range []func([]string) error{runBackup, runRestore} {
		for _, args := range [][]string{nil, {"unexpected"}, {"--unknown"}} {
			_, _, err := captureRecoveryOutput(t, func() error { return run(args) })
			if err == nil {
				t.Fatalf("accepted invalid recovery arguments: %v", args)
			}
		}
		if _, _, err := captureRecoveryOutput(t, func() error { return run([]string{"--help"}) }); err != nil {
			t.Fatalf("help failed: %v", err)
		}
	}
}
