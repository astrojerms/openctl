package plugin

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/openctl/openctl/internal/config"
)

const pluginPrefix = "openctl-"

// Plugin represents a discovered plugin
type Plugin struct {
	Name string
	Path string
}

// Discover finds all openctl plugins in PATH and ~/.openctl/plugins/
func Discover() ([]*Plugin, error) {
	seen := make(map[string]bool)
	var plugins []*Plugin

	paths, err := config.GetPaths()
	if err != nil {
		return nil, err
	}

	pluginsFromDir, err := discoverInDir(paths.PluginsDir)
	if err == nil {
		for _, p := range pluginsFromDir {
			if !seen[p.Name] {
				seen[p.Name] = true
				plugins = append(plugins, p)
			}
		}
	}

	pathEnv := os.Getenv("PATH")
	pathDirs := filepath.SplitList(pathEnv)

	for _, dir := range pathDirs {
		pluginsFromDir, err := discoverInDir(dir)
		if err != nil {
			continue
		}
		for _, p := range pluginsFromDir {
			if !seen[p.Name] {
				seen[p.Name] = true
				plugins = append(plugins, p)
			}
		}
	}

	return plugins, nil
}

// FindPlugin finds a specific plugin by name
func FindPlugin(name string) (*Plugin, error) {
	plugins, err := Discover()
	if err != nil {
		return nil, err
	}

	for _, p := range plugins {
		if p.Name == name {
			return p, nil
		}
	}

	return nil, nil
}

func discoverInDir(dir string) ([]*Plugin, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var plugins []*Plugin

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasPrefix(name, pluginPrefix) {
			continue
		}

		path := filepath.Join(dir, name)
		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.Mode()&0111 == 0 {
			continue
		}

		pluginName := strings.TrimPrefix(name, pluginPrefix)
		pluginName = strings.TrimSuffix(pluginName, ".exe")

		plugins = append(plugins, &Plugin{
			Name: pluginName,
			Path: path,
		})
	}

	return plugins, nil
}
