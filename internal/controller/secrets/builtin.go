package secrets

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RegisterBuiltins registers the two providers available with no
// configuration: "file" (reads a secret from a file under secretsDir, mode
// 0600) and "env" (reads a named environment variable). secretsDir is
// typically <state-dir>/secrets. Configured backends (Vault, cloud secret
// managers) register separately on top of these.
func RegisterBuiltins(reg *Registry, secretsDir string) {
	reg.Register(&FileProvider{root: secretsDir})
	reg.Register(&EnvProvider{})
}

// FileProvider resolves a key as a path relative to a fixed root directory,
// mirroring the tokenSecretFile convention (a 0600 file under the state dir).
// The key is confined to root — absolute paths and "../" escapes are rejected
// so a manifest can't read arbitrary files off the controller host.
type FileProvider struct {
	root string
}

func (p *FileProvider) Name() string { return builtinFileProvider }

func (p *FileProvider) Resolve(_ context.Context, key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("empty file key")
	}
	if filepath.IsAbs(key) {
		return "", fmt.Errorf("file key %q must be relative to the secrets dir, not absolute", key)
	}
	// Confine the resolved path to root: reject any key that escapes it.
	clean := filepath.Clean(filepath.Join(p.root, key))
	rootClean := filepath.Clean(p.root)
	if clean != rootClean && !strings.HasPrefix(clean, rootClean+string(os.PathSeparator)) {
		return "", fmt.Errorf("file key %q escapes the secrets dir", key)
	}
	b, err := os.ReadFile(clean) // #nosec G304 -- path confined to the controller-owned secrets dir above
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("not found at %s", clean)
		}
		return "", fmt.Errorf("read %s: %w", clean, err)
	}
	// Trim a single trailing newline so `printf 'x' > f` and `echo x > f`
	// both yield "x" — the common ways a user writes a secret file.
	return strings.TrimRight(string(b), "\n"), nil
}

// EnvProvider resolves a key as the name of an environment variable on the
// controller process. Good for CI-injected secrets.
type EnvProvider struct{}

func (p *EnvProvider) Name() string { return builtinEnvProvider }

func (p *EnvProvider) Resolve(_ context.Context, key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("empty env key")
	}
	v, ok := os.LookupEnv(key)
	if !ok {
		return "", fmt.Errorf("environment variable %q not set", key)
	}
	return v, nil
}
