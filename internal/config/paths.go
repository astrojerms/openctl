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
)

// Paths holds the various paths used by openctl
type Paths struct {
	ConfigDir  string
	ConfigFile string
	PluginsDir string
	SecretsDir string
}

// GetPaths returns the paths for the current user
func GetPaths() (*Paths, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	configDir := filepath.Join(homeDir, ConfigDirName)

	return &Paths{
		ConfigDir:  configDir,
		ConfigFile: filepath.Join(configDir, ConfigFileName),
		PluginsDir: filepath.Join(configDir, PluginsDirName),
		SecretsDir: filepath.Join(configDir, SecretsDirName),
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
