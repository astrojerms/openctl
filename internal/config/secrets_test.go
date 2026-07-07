package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// The secrets.providers block parses into typed config.
func TestSecretsConfigParses(t *testing.T) {
	body := `
secrets:
  providers:
    - name: vault
      type: vault
      address: https://vault.lan:8200
      tokenSecretFile: vault.token
    - name: bao
      type: vault
      address: https://bao.lan:8200
      tokenSecret: inline-tok
      namespace: team-a
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Secrets == nil || len(cfg.Secrets.Providers) != 2 {
		t.Fatalf("providers = %+v", cfg.Secrets)
	}
	v := cfg.Secrets.Providers[0]
	if v.Name != "vault" || v.Type != "vault" || v.Address != "https://vault.lan:8200" || v.TokenSecretFile != "vault.token" {
		t.Errorf("vault provider = %+v", v)
	}
	if cfg.Secrets.Providers[1].Namespace != "team-a" {
		t.Errorf("namespace = %q", cfg.Secrets.Providers[1].Namespace)
	}
}

// ResolveToken reads the file when set, else the inline token.
func TestResolveToken(t *testing.T) {
	dir := t.TempDir()
	tokPath := filepath.Join(dir, "vault.token")
	if err := os.WriteFile(tokPath, []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fromFile := &SecretProviderConfig{TokenSecretFile: tokPath}
	if got, err := fromFile.ResolveToken(); err != nil || got != "file-token" {
		t.Errorf("file token = %q err=%v, want file-token (trimmed)", got, err)
	}

	inline := &SecretProviderConfig{TokenSecret: "inline"}
	if got, err := inline.ResolveToken(); err != nil || got != "inline" {
		t.Errorf("inline token = %q err=%v", got, err)
	}
}
