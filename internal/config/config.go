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
	Defaults   Defaults             `yaml:"defaults"`
	Providers  map[string]*Provider `yaml:"providers"`
	Controller *Controller          `yaml:"controller,omitempty"`
	Manifests  *Manifests           `yaml:"manifests,omitempty"`
	Reconciler *Reconciler          `yaml:"reconciler,omitempty"`
	Operations *Operations          `yaml:"operations,omitempty"`
	Templates  *Templates           `yaml:"templates,omitempty"`
}

// Operations configures the controller's operation store. nil/omitted →
// built-in defaults. Like the other controller-behavior blocks, changes
// here only take effect on the next controller start.
type Operations struct {
	// RetainPerResource caps how many completed operation rows the GC keeps
	// per resource; older ones are pruned. 0/omitted → the built-in default
	// (DefaultRetainPerResource). Must be non-negative.
	RetainPerResource int `yaml:"retainPerResource,omitempty"`
}

// DefaultRetainPerResource is the built-in cap on retained completed ops per
// resource, used when config omits operations.retainPerResource. Kept here
// (not in the operations package) so both the controller entrypoint and the
// ConfigService can agree on the fallback without an import cycle.
const DefaultRetainPerResource = 50

// Templates configures where the controller scans for user-authored CUE
// templates, served alongside the compiled-in starters through the same
// TemplateService RPCs. nil/omitted → ~/.openctl/templates is used; a
// missing directory is not an error.
type Templates struct {
	// Dir is the directory scanned for `*.cue` template files. Defaults to
	// ~/.openctl/templates when empty. Tilde is expanded via ExpandPath.
	Dir string `yaml:"dir"`
}

// Reconciler configures the controller's periodic drift checker. When
// nil/omitted the reconciler runs with built-in defaults (enabled,
// 5-minute interval). Set Enabled=false to turn it off entirely.
type Reconciler struct {
	// Enabled toggles the background ticker. Defaults to true when the
	// block is present; omit the whole `reconciler:` section for default
	// behavior. Use Enabled=false explicitly to disable.
	Enabled *bool `yaml:"enabled,omitempty"`
	// Interval is the time between reconcile passes (e.g. "5m", "30s").
	// Defaults to 5m when empty.
	Interval string `yaml:"interval,omitempty"`
}

// Manifests configures the controller's on-disk manifest mirror — the
// directory the controller writes desired state to after each successful
// apply, so users can see (and optionally git-track) what's deployed.
//
// Git fields are read in UI Phase U2.2; v1 of the disk mirror (U2.1) only
// needs Dir. GitOps enables two-way sync (file edits → Apply).
type Manifests struct {
	// Dir is the root directory for the materialized manifests. Defaults to
	// ~/.openctl/manifests when empty. Tilde is expanded via ExpandPath.
	Dir string `yaml:"dir"`
	// Git configures optional git tracking of the manifest dir. nil = git
	// integration off.
	Git *ManifestsGit `yaml:"git,omitempty"`
	// GitOps toggles two-way sync: when true, file edits in Dir are
	// watched via fsnotify and applied back through the controller
	// (Apply of the manifest, source="gitops"). Default off — one-way
	// mirror is the default, and opt-in prevents surprise behavior.
	GitOps *ManifestsGitOps `yaml:"gitops,omitempty"`
}

// ManifestsGitOps configures the fsnotify-driven file→apply loop.
type ManifestsGitOps struct {
	// Enabled turns on the watcher. Off unless explicitly true.
	Enabled bool `yaml:"enabled"`
	// DeleteOnRemove submits a Delete when a manifest file is removed
	// from the mirror. Default false — most users prefer to move
	// files around without triggering resource deletion.
	DeleteOnRemove bool `yaml:"deleteOnRemove"`
}

// ManifestsGit configures git tracking of the manifest directory. When
// Enabled, the controller runs `git init` on first start (if not already a
// repo) and commits after every materialize/delete.
type ManifestsGit struct {
	Enabled bool `yaml:"enabled"`
	// Branch defaults to "main" when empty. Used both for `git init -b` and
	// as the push target.
	Branch string `yaml:"branch"`
	// Remote is the optional remote URL. Empty disables remote push.
	Remote string `yaml:"remote"`
	// PushMode controls when commits are pushed to Remote:
	//   "" or "onCommit" — push after every commit (default if Remote set)
	//   "manual"          — only on explicit RepoService.Push RPC
	//   "periodic"        — background ticker; uses PushInterval
	PushMode string `yaml:"pushMode"`
	// PushInterval is the cadence for "periodic" push mode (e.g. "5m").
	// Parsed as time.Duration; ignored for other modes.
	PushInterval string `yaml:"pushInterval"`
}

// Controller is how the CLI talks to the controller daemon. Empty fields
// fall back to the local-Mac defaults (~/.openctl/controller/...) so a
// freshly-installed local controller works with no config tweaks.
type Controller struct {
	// URL is the controller's gRPC endpoint, optionally with https:// prefix
	// (which is stripped). Defaults to 127.0.0.1:9444.
	URL string `yaml:"url"`
	// TokenFile is the path to the API token file (mode 0600). Defaults to
	// ~/.openctl/controller/token.
	TokenFile string `yaml:"tokenFile"`
	// CAFile is the path to the controller's CA cert in PEM. Defaults to
	// ~/.openctl/controller/tls/ca.crt.
	CAFile string `yaml:"caFile"`
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

// Save writes the current in-memory config to the default path, atomically
// via temp+rename so a crashing writer never leaves a truncated file. Mode
// 0600 because provider credentials live here.
func (c *Config) Save() error {
	paths, err := GetPaths()
	if err != nil {
		return err
	}
	return c.SaveToFile(paths.ConfigFile)
}

// SaveToFile is Save with an explicit target — useful for tests that
// exercise the round-trip against a temp file.
func (c *Config) SaveToFile(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		return fmt.Errorf("mkdir config dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return os.Rename(tmp, path)
}

func dirOf(p string) string {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return "."
	}
	return p[:i]
}
