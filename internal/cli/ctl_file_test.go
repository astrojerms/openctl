package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateExportDoesNotRemoveOrConfirmReplacement(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failFetch bool
	}{{"successful fetch", false}, {"failed fetch", true}} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "result")
			completed := false
			err := writePrivateExport(path, func(out io.Writer) error {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("someone else's result"), 0600); err != nil {
					t.Fatal(err)
				}
				if tc.failFetch {
					return errors.New("controller connection lost")
				}
				_, err := io.WriteString(out, "our returned payload")
				return err
			}, func() error { completed = true; return nil })
			if err == nil || completed {
				t.Fatal("reported success for a replaced destination")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "someone else's result" {
				t.Fatal("modified or removed a file not owned by this invocation")
			}
		})
	}
}
