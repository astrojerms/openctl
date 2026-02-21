package manifest

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/openctl/openctl/pkg/protocol"
)

// Load loads a manifest from a file
func Load(path string) (*protocol.Resource, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest file: %w", err)
	}

	return Parse(data)
}

// Parse parses a manifest from YAML data
func Parse(data []byte) (*protocol.Resource, error) {
	var resource protocol.Resource
	if err := yaml.Unmarshal(data, &resource); err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}

	if resource.APIVersion == "" {
		return nil, fmt.Errorf("manifest missing required field: apiVersion")
	}
	if resource.Kind == "" {
		return nil, fmt.Errorf("manifest missing required field: kind")
	}
	if resource.Metadata.Name == "" {
		return nil, fmt.Errorf("manifest missing required field: metadata.name")
	}

	return &resource, nil
}

// LoadMultiple loads multiple manifests from a file (separated by ---)
func LoadMultiple(path string) ([]*protocol.Resource, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest file: %w", err)
	}

	return ParseMultiple(data)
}

// ParseMultiple parses multiple manifests from YAML data
func ParseMultiple(data []byte) ([]*protocol.Resource, error) {
	docs := strings.Split(string(data), "\n---")

	var resources []*protocol.Resource
	for _, doc := range docs {
		doc = strings.TrimSpace(doc)
		if doc == "" {
			continue
		}

		resource, err := Parse([]byte(doc))
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}

	return resources, nil
}

// ExtractProvider extracts the provider name from an apiVersion
func ExtractProvider(apiVersion string) string {
	parts := strings.Split(apiVersion, ".")
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}
