package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverInDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Create some test plugin files
	plugins := []string{"openctl-foo", "openctl-bar", "openctl-baz"}
	for _, p := range plugins {
		path := filepath.Join(tmpDir, p)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatalf("failed to create plugin %s: %v", p, err)
		}
	}

	// Create a non-plugin file
	if err := os.WriteFile(filepath.Join(tmpDir, "other-binary"), []byte(""), 0755); err != nil {
		t.Fatalf("failed to create other-binary: %v", err)
	}

	// Create a non-executable plugin
	if err := os.WriteFile(filepath.Join(tmpDir, "openctl-noexec"), []byte(""), 0644); err != nil {
		t.Fatalf("failed to create openctl-noexec: %v", err)
	}

	// Create a directory with plugin prefix
	if err := os.Mkdir(filepath.Join(tmpDir, "openctl-dir"), 0755); err != nil {
		t.Fatalf("failed to create openctl-dir: %v", err)
	}

	discovered, err := discoverInDir(tmpDir)
	if err != nil {
		t.Fatalf("discoverInDir failed: %v", err)
	}

	if len(discovered) != 3 {
		t.Errorf("expected 3 plugins, got %d", len(discovered))
	}

	names := make(map[string]bool)
	for _, p := range discovered {
		names[p.Name] = true
	}

	for _, expected := range []string{"foo", "bar", "baz"} {
		if !names[expected] {
			t.Errorf("expected plugin %s not found", expected)
		}
	}
}

func TestDiscoverInDir_Empty(t *testing.T) {
	tmpDir := t.TempDir()

	discovered, err := discoverInDir(tmpDir)
	if err != nil {
		t.Fatalf("discoverInDir failed: %v", err)
	}

	if len(discovered) != 0 {
		t.Errorf("expected 0 plugins, got %d", len(discovered))
	}
}

func TestDiscoverInDir_NotExists(t *testing.T) {
	_, err := discoverInDir("/nonexistent/directory")
	if err == nil {
		t.Error("expected error for nonexistent directory")
	}
}

func TestFindPlugin(t *testing.T) {
	// Save original PATH and restore after test
	origPath := os.Getenv("PATH")
	defer os.Setenv("PATH", origPath)

	tmpDir := t.TempDir()

	// Create a test plugin
	pluginPath := filepath.Join(tmpDir, "openctl-testplugin")
	if err := os.WriteFile(pluginPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("failed to create plugin: %v", err)
	}

	// Add tmpDir to PATH
	os.Setenv("PATH", tmpDir+":"+origPath)

	plugin, err := FindPlugin("testplugin")
	if err != nil {
		t.Fatalf("FindPlugin failed: %v", err)
	}

	if plugin == nil {
		t.Fatal("expected plugin to be found")
	}

	if plugin.Name != "testplugin" {
		t.Errorf("expected name=testplugin, got %s", plugin.Name)
	}

	if plugin.Path != pluginPath {
		t.Errorf("expected path=%s, got %s", pluginPath, plugin.Path)
	}
}

func TestFindPlugin_NotFound(t *testing.T) {
	plugin, err := FindPlugin("nonexistent-plugin-xyz")
	if err != nil {
		t.Fatalf("FindPlugin should not error: %v", err)
	}

	if plugin != nil {
		t.Error("expected plugin to be nil")
	}
}

func TestPluginNameParsing(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		filename     string
		expectedName string
	}{
		{"openctl-simple", "simple"},
		{"openctl-with-dash", "with-dash"},
		{"openctl-proxmox", "proxmox"},
	}

	for _, tt := range tests {
		path := filepath.Join(tmpDir, tt.filename)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatalf("failed to create plugin %s: %v", tt.filename, err)
		}
	}

	discovered, err := discoverInDir(tmpDir)
	if err != nil {
		t.Fatalf("discoverInDir failed: %v", err)
	}

	names := make(map[string]bool)
	for _, p := range discovered {
		names[p.Name] = true
	}

	for _, tt := range tests {
		if !names[tt.expectedName] {
			t.Errorf("expected plugin name %s for file %s", tt.expectedName, tt.filename)
		}
	}
}
