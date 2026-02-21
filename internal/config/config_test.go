package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.yaml")

	configContent := `
defaults:
  output: yaml
  timeout: 600

providers:
  proxmox:
    default-context: test
    contexts:
      test:
        endpoint: https://pve.example.com:8006
        node: pve1
        credentials: test-creds
    credentials:
      test-creds:
        tokenId: root@pam!test
        tokenSecret: secret-token
    defaults:
      storage: local-lvm
`
	if err := os.WriteFile(configFile, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := LoadFromFile(configFile)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	if cfg.Defaults.Output != "yaml" {
		t.Errorf("expected output=yaml, got %s", cfg.Defaults.Output)
	}
	if cfg.Defaults.Timeout != 600 {
		t.Errorf("expected timeout=600, got %d", cfg.Defaults.Timeout)
	}

	if _, ok := cfg.Providers["proxmox"]; !ok {
		t.Fatal("expected proxmox provider")
	}

	proxmox := cfg.Providers["proxmox"]
	if proxmox.DefaultContext != "test" {
		t.Errorf("expected default-context=test, got %s", proxmox.DefaultContext)
	}
}

func TestLoadFromFile_NotExists(t *testing.T) {
	cfg, err := LoadFromFile("/nonexistent/config.yaml")
	if err != nil {
		t.Fatalf("LoadFromFile should not error for missing file: %v", err)
	}

	if cfg.Defaults.Output != "table" {
		t.Errorf("expected default output=table, got %s", cfg.Defaults.Output)
	}
	if cfg.Defaults.Timeout != 300 {
		t.Errorf("expected default timeout=300, got %d", cfg.Defaults.Timeout)
	}
}

func TestGetProviderConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.yaml")

	configContent := `
providers:
  proxmox:
    default-context: homelab
    contexts:
      homelab:
        endpoint: https://pve.home.local:8006
        node: pve1
        credentials: home-creds
      work:
        endpoint: https://pve.work.com:8006
        node: node1
        credentials: work-creds
    credentials:
      home-creds:
        tokenId: root@pam!home
        tokenSecret: home-secret
      work-creds:
        tokenId: admin@pve!work
        tokenSecret: work-secret
    defaults:
      storage: local-lvm
      network: vmbr0
`
	if err := os.WriteFile(configFile, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := LoadFromFile(configFile)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	t.Run("default context", func(t *testing.T) {
		providerCfg, err := cfg.GetProviderConfig("proxmox", "")
		if err != nil {
			t.Fatalf("GetProviderConfig failed: %v", err)
		}

		if providerCfg.Endpoint != "https://pve.home.local:8006" {
			t.Errorf("expected homelab endpoint, got %s", providerCfg.Endpoint)
		}
		if providerCfg.TokenID != "root@pam!home" {
			t.Errorf("expected home tokenId, got %s", providerCfg.TokenID)
		}
		if providerCfg.TokenSecret != "home-secret" {
			t.Errorf("expected home tokenSecret, got %s", providerCfg.TokenSecret)
		}
	})

	t.Run("explicit context", func(t *testing.T) {
		providerCfg, err := cfg.GetProviderConfig("proxmox", "work")
		if err != nil {
			t.Fatalf("GetProviderConfig failed: %v", err)
		}

		if providerCfg.Endpoint != "https://pve.work.com:8006" {
			t.Errorf("expected work endpoint, got %s", providerCfg.Endpoint)
		}
		if providerCfg.TokenID != "admin@pve!work" {
			t.Errorf("expected work tokenId, got %s", providerCfg.TokenID)
		}
	})

	t.Run("unknown context", func(t *testing.T) {
		_, err := cfg.GetProviderConfig("proxmox", "unknown")
		if err == nil {
			t.Error("expected error for unknown context")
		}
	})

	t.Run("unknown provider", func(t *testing.T) {
		providerCfg, err := cfg.GetProviderConfig("unknown", "")
		if err != nil {
			t.Fatalf("GetProviderConfig should not error for unknown provider: %v", err)
		}
		if providerCfg.Endpoint != "" {
			t.Error("expected empty config for unknown provider")
		}
	})
}

func TestGetProviderConfig_TokenSecretFile(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.yaml")
	secretFile := filepath.Join(tmpDir, "secret.token")

	if err := os.WriteFile(secretFile, []byte("file-based-secret\n"), 0600); err != nil {
		t.Fatalf("failed to write secret file: %v", err)
	}

	configContent := `
providers:
  proxmox:
    default-context: test
    contexts:
      test:
        endpoint: https://pve.example.com:8006
        credentials: test-creds
    credentials:
      test-creds:
        tokenId: root@pam!test
        tokenSecretFile: ` + secretFile + `
`
	if err := os.WriteFile(configFile, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := LoadFromFile(configFile)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	providerCfg, err := cfg.GetProviderConfig("proxmox", "")
	if err != nil {
		t.Fatalf("GetProviderConfig failed: %v", err)
	}

	if providerCfg.TokenSecret != "file-based-secret" {
		t.Errorf("expected file-based-secret, got %s", providerCfg.TokenSecret)
	}
}

func TestExpandPath(t *testing.T) {
	homeDir, _ := os.UserHomeDir()

	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
		{"~/config", filepath.Join(homeDir, "config")},
		{"~/.openctl/secrets", filepath.Join(homeDir, ".openctl/secrets")},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := ExpandPath(tt.input)
			if err != nil {
				t.Fatalf("ExpandPath failed: %v", err)
			}
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}
