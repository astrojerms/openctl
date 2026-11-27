package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/openctl/openctl/internal/controller/recovery"
)

const recoveryWarning = "WARNING: backup archives contain plaintext credentials, tokens, secrets, and TLS private keys. Keep them private; use encrypted offsite storage externally. External SSH keys, manifests, secret files, and application data outside the OpenCtl root/state directory are NOT backed up."

func runBackup(args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	output := fs.String("output", "", "new sensitive backup archive (required; never overwritten)")
	root := fs.String("root", filepath.Join(home, ".openctl"), "complete OpenCtl config/data root")
	dir := fs.String("dir", "", "controller state directory (default ROOT/controller)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *output == "" || fs.NArg() != 0 {
		return fmt.Errorf("backup requires --output FILE and accepts only flags")
	}
	fmt.Fprintln(os.Stderr, recoveryWarning)
	result, err := recovery.Backup(context.Background(), recovery.Options{Root: *root, StateDir: *dir, Archive: *output})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Offline backup created: %s\nOpenCtl root: %s\nController state: %s\n", *output, result.Root, result.StateDir)
	return nil
}

func runRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	input := fs.String("input", "", "sensitive backup archive (required)")
	root := fs.String("root", "", "NEW nonexistent OpenCtl root (required)")
	dir := fs.String("dir", "", "NEW controller state directory (default archived root-relative path, or ROOT/controller)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *input == "" || *root == "" || fs.NArg() != 0 {
		return fmt.Errorf("restore requires --input FILE --root NEW_ROOT and accepts only flags")
	}
	fmt.Fprintln(os.Stderr, recoveryWarning)
	result, err := recovery.Restore(context.Background(), recovery.Options{Root: *root, StateDir: *dir, Archive: *input})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Offline restore complete. Controller NOT started; no infrastructure changed.\nOpenCtl root: %s\nController state: %s\nArchived root: %s\nArchived state: %s\nConfigured paths and provider values were preserved, NOT rewritten. Review external SSH keys, manifests, secret files, application data, and all archived absolute paths before starting. Starting this restored controller may resume queued operations or reconciliation against the original infrastructure; never run both copies.\n", result.Root, result.StateDir, result.OriginalRoot, result.OriginalStateDir)
	return nil
}
