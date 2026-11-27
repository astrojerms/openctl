package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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

// Parse parses exactly one resource. Additional resources are rejected rather
// than silently dropping all but the first declaration from a YAML stream.
func Parse(data []byte) (*protocol.Resource, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	resource, err := nextManifest(decoder)
	if errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("manifest missing required field: apiVersion")
	}
	if err != nil {
		return nil, err
	}
	if _, err := nextManifest(decoder); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("expected one resource, found multiple YAML documents; apply each resource separately")
	}
	return resource, nil
}

// LoadMultiple loads multiple manifests from a file (separated by ---)
func LoadMultiple(path string) ([]*protocol.Resource, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest file: %w", err)
	}

	return ParseMultiple(data)
}

// ParseMultiple parses a YAML stream, skipping only empty/comment documents.
func ParseMultiple(data []byte) ([]*protocol.Resource, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	resources := make([]*protocol.Resource, 0)
	for {
		resource, err := nextManifest(decoder)
		if errors.Is(err, io.EOF) {
			return resources, nil
		}
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}
}

func nextManifest(decoder *yaml.Decoder) (*protocol.Resource, error) {
	for {
		var document yaml.Node
		if err := decoder.Decode(&document); err != nil {
			if errors.Is(err, io.EOF) {
				return nil, err
			}
			return nil, fmt.Errorf("failed to parse manifest: %w", err)
		}
		if len(document.Content) == 0 || (len(document.Content) == 1 && document.Content[0].ShortTag() == "!!null" && document.Content[0].Value == "") {
			continue
		}
		var resource protocol.Resource
		if err := document.Decode(&resource); err != nil {
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
}

// ExtractProvider extracts the provider name from an apiVersion
func ExtractProvider(apiVersion string) string {
	parts := strings.Split(apiVersion, ".")
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}
