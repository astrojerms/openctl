package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/openctl/openctl/pkg/protocol"
)

// Config represents the openctl configuration file
type Config struct {
	Defaults  Defaults             `yaml:"defaults"`
	Providers map[string]*Provider `yaml:"providers"`
}

// Defaults contains global default settings
type Defaults struct {
	Output  string `yaml:"output"`
	Timeout int    `yaml:"timeout"`
}

// Provider represents a provider configuration
type Provider struct {
	DefaultContext string                 `yaml:"default-context"`
	Contexts       map[string]*Context    `yaml:"contexts"`
	Credentials    map[string]*Credential `yaml:"credentials"`
	Defaults       map[string]string      `yaml:"defaults"`
}

// Context represents a provider context (like a cluster or endpoint)
type Context struct {
	Endpoint    string `yaml:"endpoint"`
	Node        string `yaml:"node"`
	Credentials string `yaml:"credentials"`
}

// Credential represents credentials for a provider
type Credential struct {
	TokenID         string `yaml:"tokenId"`
	TokenSecret     string `yaml:"tokenSecret"`
	TokenSecretFile string `yaml:"tokenSecretFile"`
}

// Load loads the configuration from the default path
func Load() (*Config, error) {
	paths, err := GetPaths()
	if err != nil {
		return nil, err
	}

	return LoadFromFile(paths.ConfigFile)
}

// LoadFromFile loads the configuration from a specific file
func LoadFromFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{
				Defaults: Defaults{
					Output:  "table",
					Timeout: 300,
				},
				Providers: make(map[string]*Provider),
			}, nil
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	if cfg.Providers == nil {
		cfg.Providers = make(map[string]*Provider)
	}

	return &cfg, nil
}

// GetProviderConfig returns the configuration for a specific provider
func (c *Config) GetProviderConfig(providerName, contextName string) (*protocol.ProviderConfig, error) {
	provider, ok := c.Providers[providerName]
	if !ok {
		return &protocol.ProviderConfig{}, nil
	}

	if contextName == "" {
		contextName = provider.DefaultContext
	}

	if contextName == "" {
		return &protocol.ProviderConfig{
			Defaults: provider.Defaults,
		}, nil
	}

	ctx, ok := provider.Contexts[contextName]
	if !ok {
		return nil, fmt.Errorf("context %q not found for provider %q", contextName, providerName)
	}

	cfg := &protocol.ProviderConfig{
		Endpoint: ctx.Endpoint,
		Node:     ctx.Node,
		Defaults: provider.Defaults,
	}

	if ctx.Credentials != "" {
		cred, ok := provider.Credentials[ctx.Credentials]
		if !ok {
			return nil, fmt.Errorf("credentials %q not found for provider %q", ctx.Credentials, providerName)
		}

		cfg.TokenID = cred.TokenID

		if cred.TokenSecret != "" {
			cfg.TokenSecret = cred.TokenSecret
		} else if cred.TokenSecretFile != "" {
			secret, err := readSecretFile(cred.TokenSecretFile)
			if err != nil {
				return nil, fmt.Errorf("failed to read token secret file: %w", err)
			}
			cfg.TokenSecret = secret
		}
	}

	return cfg, nil
}

func readSecretFile(path string) (string, error) {
	expandedPath, err := ExpandPath(path)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(expandedPath)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(data)), nil
}
