package config

import (
	"os"
	"path/filepath"
)

const (
	// ConfigDirName is the name of the config directory
	ConfigDirName = ".openctl"
	// ConfigFileName is the name of the config file
	ConfigFileName = "config.yaml"
	// PluginsDirName is the name of the plugins directory
	PluginsDirName = "plugins"
	// SecretsDirName is the name of the secrets directory
	SecretsDirName = "secrets"
	// ManifestsDirName is the default subdirectory for the controller's
	// materialized manifest mirror (UI Phase U2). Resolved as
	// ~/.openctl/manifests when no override is set in config.
	ManifestsDirName = "manifests"
	// TemplatesDirName is the default subdirectory the controller scans
	// for user-authored CUE templates. Resolved as ~/.openctl/templates
	// when no override is set in config. Missing dir is not an error —
	// only the compiled-in starters are served.
	TemplatesDirName = "templates"
)

// Paths holds the various paths used by openctl
type Paths struct {
	ConfigDir    string
	ConfigFile   string
	PluginsDir   string
	SecretsDir   string
	ManifestsDir string
	TemplatesDir string
}

// GetPaths returns the paths for the current user
func GetPaths() (*Paths, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	configDir := filepath.Join(homeDir, ConfigDirName)

	return &Paths{
		ConfigDir:    configDir,
		ConfigFile:   filepath.Join(configDir, ConfigFileName),
		PluginsDir:   filepath.Join(configDir, PluginsDirName),
		SecretsDir:   filepath.Join(configDir, SecretsDirName),
		ManifestsDir: filepath.Join(configDir, ManifestsDirName),
		TemplatesDir: filepath.Join(configDir, TemplatesDirName),
	}, nil
}

// EnsureDirectories creates the necessary directories if they don't exist
func (p *Paths) EnsureDirectories() error {
	dirs := []string{p.ConfigDir, p.PluginsDir, p.SecretsDir}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// ExpandPath expands ~ to the user's home directory
func ExpandPath(path string) (string, error) {
	if path == "" {
		return path, nil
	}

	if path[0] != '~' {
		return path, nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(homeDir, path[1:]), nil
}
