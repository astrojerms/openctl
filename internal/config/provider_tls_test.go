package config

import (
	"path/filepath"
	"testing"
)

func TestProviderContextTrustResolvesWithoutChangingSiblingPolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := &Config{Providers: map[string]*Provider{"proxmox": {DefaultContext: "verified", Contexts: map[string]*Context{
		"verified":          {Endpoint: "https://verified.example:8006", CAFile: "~/pki/proxmox.pem"},
		"explicit-insecure": {Endpoint: "https://legacy.example:8006", InsecureSkipVerify: true},
	}}}}
	contexts, defaultContext, err := cfg.ProviderContextConfigs("proxmox")
	if err != nil {
		t.Fatal(err)
	}
	if defaultContext != "verified" || contexts["verified"].CAFile != filepath.Join(home, "pki", "proxmox.pem") || contexts["verified"].InsecureSkipVerify {
		t.Fatal("verified context lost its expanded controller-local trust")
	}
	if contexts["explicit-insecure"].CAFile != "" || !contexts["explicit-insecure"].InsecureSkipVerify {
		t.Fatal("explicit per-context insecure opt-in was lost or shared")
	}
	if cfg.Providers["proxmox"].Contexts["verified"].CAFile != "~/pki/proxmox.pem" {
		t.Fatal("resolving context mutated persisted configuration")
	}
}
