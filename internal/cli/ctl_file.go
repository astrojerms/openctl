package cli

import (
	"fmt"
	"io"
	"os"
)

// writePrivateExport reserves a private destination before fetching anything.
// A failed export removes only this invocation's file, never a replacement.
func writePrivateExport(path string, export func(io.Writer) error, completed func() error) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("cannot create export file: %w", err)
	}
	owned, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("cannot inspect export file: %w", err)
	}
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			if current, err := os.Lstat(path); err == nil && os.SameFile(owned, current) {
				_ = os.Remove(path)
			}
		}
	}()
	if err := export(file); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("cannot sync export file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("cannot close export file: %w", err)
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(owned, current) {
		return fmt.Errorf("export destination changed while fetching; inspect it before retrying")
	}
	committed = true
	return completed()
}
