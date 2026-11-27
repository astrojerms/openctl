package config

import (
	"fmt"
	"os"
	"strings"
	"time"

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
	Secrets    *Secrets             `yaml:"secrets,omitempty"`
	Auth       *Auth                `yaml:"auth,omitempty"`
}

// Auth configures authentication front doors beyond the root token + named
// users. Currently just OIDC (browser SSO).
type Auth struct {
	OIDC *OIDCConfig `yaml:"oidc,omitempty"`
}

// OIDCConfig configures OIDC login: an external IdP mints openctl sessions via
// Authorization Code + PKCE, reusing the existing session/cookie/RBAC layer.
// Absent or Enabled=false → OIDC off (today's behavior exactly).
type OIDCConfig struct {
	Enabled bool `yaml:"enabled"`
	// Issuer is the IdP base URL; discovery reads <issuer>/.well-known/openid-configuration.
	Issuer   string `yaml:"issuer"`
	ClientID string `yaml:"clientID"`
	// ClientSecretFile holds the OAuth client secret (0600). Never inline it.
	ClientSecretFile string `yaml:"clientSecretFile,omitempty"`
	// RedirectURL is this controller's callback URL, registered with the IdP.
	RedirectURL string `yaml:"redirectURL"`
	// RoleClaim is the ID-token claim carrying the role signal (default "groups").
	RoleClaim string `yaml:"roleClaim,omitempty"`
	// RoleMapping maps a claim value → openctl role (viewer|editor|admin).
	RoleMapping map[string]string `yaml:"roleMapping,omitempty"`
	// DefaultRole is granted to an authenticated user matching no mapping.
	// Empty (the default) means deny — fail closed.
	DefaultRole string `yaml:"defaultRole,omitempty"`
	// UsernameClaim becomes Principal.UserID (default "email").
	UsernameClaim string `yaml:"usernameClaim,omitempty"`
}

// ResolveClientSecret reads the OAuth client secret from ClientSecretFile.
func (o *OIDCConfig) ResolveClientSecret() (string, error) {
	if o.ClientSecretFile == "" {
		return "", nil
	}
	return readSecretFile(o.ClientSecretFile)
}

// Secrets configures secret-resolution backends beyond the built-in file/env
// providers (Tier 2 of the $secret feature). Each entry registers a named
// SecretProvider a manifest can reference via {$secret: {provider, key}}.
type Secrets struct {
	Providers []SecretProviderConfig `yaml:"providers"`
}

// SecretProviderConfig declares one configured secret backend.
type SecretProviderConfig struct {
	// Name is the identifier a $secret marker's `provider` field references.
	Name string `yaml:"name"`
	// Type selects the backend implementation: "vault" or "infisical".
	Type string `yaml:"type"`
	// Address is the backend base URL (e.g. https://vault.lan:8200 or
	// https://infisical.lan).
	Address string `yaml:"address"`
	// Token authenticates to the backend. Prefer TokenSecretFile (0600) over
	// an inline TokenSecret — never commit a real token. For Vault this is the
	// Vault token; for Infisical it is the Universal Auth client secret (when
	// ClientID is set) or a static service/access token (when it isn't).
	TokenSecret     string `yaml:"tokenSecret,omitempty"`
	TokenSecretFile string `yaml:"tokenSecretFile,omitempty"`
	// Namespace is an optional Vault Enterprise namespace (X-Vault-Namespace).
	Namespace string `yaml:"namespace,omitempty"`
	// Infisical-only. ClientID enables Universal Auth (machine identity) — the
	// client secret comes from TokenSecret/TokenSecretFile; leave empty to use
	// that token directly as a Bearer service/access token. ProjectID and
	// Environment scope which secrets this named provider reads.
	ClientID    string `yaml:"clientId,omitempty"`
	ProjectID   string `yaml:"projectId,omitempty"`
	Environment string `yaml:"environment,omitempty"`
}

// ResolveToken returns the backend token, reading TokenSecretFile when set
// (preferred) else the inline TokenSecret. Empty when neither is configured
// (some backends authenticate ambiently, e.g. via instance identity).
func (s *SecretProviderConfig) ResolveToken() (string, error) {
	if s.TokenSecretFile != "" {
		return readSecretFile(s.TokenSecretFile)
	}
	return s.TokenSecret, nil
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

// Manifests selects SQLite audit-mirror or upstream-repository authority.
type Manifests struct {
	// Dir is the root directory for the materialized manifests. Defaults to
	// ~/.openctl/manifests when empty. Tilde is expanded via ExpandPath.
	Dir  string `yaml:"dir"`
	Mode string `yaml:"mode,omitempty"`
	// Git configures optional git tracking of the manifest dir. nil = git
	// integration off.
	Git *ManifestsGit `yaml:"git,omitempty"`
	// GitOps configures upstream-authored input in gitops mode.
	GitOps *ManifestsGitOps `yaml:"gitops,omitempty"`
}

// ManifestsGitOps configures the fsnotify-driven file→apply loop.
type ManifestsGitOps struct {
	Remote string `yaml:"remote"`
	// Enabled is a legacy switch. Mode now enables the watcher; false is
	// rejected rather than silently overriding the selected authority.
	Enabled *bool `yaml:"enabled,omitempty"`
	// DeleteOnRemove submits a Delete when a manifest file is removed
	// from the mirror. Default false — most users prefer to move
	// files around without triggering resource deletion.
	DeleteOnRemove bool `yaml:"deleteOnRemove"`
	// Pull enables periodic synchronization when present. Remote is the
	// upstream repository in this block, never the mirror's push remote.
	Pull *ManifestsGitOpsPull `yaml:"pull,omitempty"`
}

// ManifestsGitOpsPull configures the git-as-source pull loop.
type ManifestsGitOpsPull struct {
	// Enabled is retained for migration; false requires removing Pull.
	Enabled *bool `yaml:"enabled,omitempty"`
	// Interval defaults to 1m; invalid or nonpositive values are rejected.
	Interval string `yaml:"interval"`
	// Prune enables repo-wide pruning: after a pull, resources whose manifest
	// file was removed from the repo are deleted, making the repo the desired
	// SET (not just an additive source). Default OFF — this deletes real
	// infrastructure. Heavily guarded: never touches composite children or
	// resources whose latest apply was cli/ui-sourced.
	Prune bool `yaml:"prune"`
	// Webhook enables a push-triggered reconcile: an HTTP endpoint on the
	// controller's HTTP gateway that a GitHub (or compatible) push webhook
	// calls to trigger an immediate pull+reconcile instead of waiting for the
	// next interval. nil = off. Requires the pull block and a remote.
	Webhook *ManifestsGitOpsWebhook `yaml:"webhook,omitempty"`
}

// ManifestsGitOpsWebhook configures the push-triggered reconcile endpoint.
type ManifestsGitOpsWebhook struct {
	// Enabled mounts the webhook route on the HTTP gateway. Off unless true.
	Enabled bool `yaml:"enabled"`
	// Secret is the shared secret for HMAC-SHA256 verification of the
	// X-Hub-Signature-256 header (GitHub's scheme). Strongly recommended:
	// when empty, the endpoint accepts any POST (only acceptable on a trusted
	// network). Prefer a base.#Secret reference over an inline value.
	Secret string `yaml:"secret"`
	// Path is the route the webhook is mounted at. Defaults to
	// "/gitops/webhook" when empty.
	Path string `yaml:"path"`
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

	// Command, when set, marks this provider as an external plugin: the
	// controller spawns `Command Args...`, speaks the v2 pluginproto protocol
	// to it, and registers the resulting adapter as a Provider. The built-in
	// providers (proxmox, k3s) are registered first and take precedence, so a
	// Command on one of those names is ignored. Endpoint/token/defaults for
	// the provider's default context are passed to the plugin via configure.
	Command string `yaml:"command,omitempty"`
	// Args are the arguments passed to Command (e.g. ["plugin-serve"]).
	Args []string `yaml:"args,omitempty"`

	// Terraform, when set, marks this provider as a Terraform/OpenTofu
	// provider hosted by openctl's tfhost adapter. The controller launches
	// Terraform.Command Args..., configures the provider with Terraform.Config,
	// and exposes the configured Resources as openctl Kinds.
	Terraform *TerraformProvider `yaml:"terraform,omitempty"`
}

// TerraformProvider configures one Terraform/OpenTofu provider binary hosted
// by the controller over the tfplugin6 protocol.
type TerraformProvider struct {
	Command   string                 `yaml:"command"`
	Args      []string               `yaml:"args,omitempty"`
	Config    map[string]any         `yaml:"config,omitempty"`
	Resources []TerraformResourceMap `yaml:"resources"`
}

// TerraformResourceMap binds an openctl Kind to the provider's Terraform
// resource type name, for example Kind "Bucket" to type "aws_s3_bucket".
type TerraformResourceMap struct {
	Kind string `yaml:"kind"`
	Type string `yaml:"type"`
}

// Context represents a provider context (like a cluster or endpoint)
type Context struct {
	Endpoint    string                     `yaml:"endpoint"`
	Node        string                     `yaml:"node"`
	Credentials string                     `yaml:"credentials"`
	SnippetSSH  *protocol.SnippetSSHConfig `yaml:"snippetSSH,omitempty"`
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
	if err := cfg.validateManifestsMode(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// ManifestMode returns the single authority selected for desired manifests.
func (c *Config) ManifestMode() string {
	if c == nil || c.Manifests == nil || c.Manifests.Mode == "" {
		return "mirror"
	}
	return c.Manifests.Mode
}

func (c *Config) validateManifestsMode() error {
	m := c.Manifests
	if m == nil {
		return nil
	}
	switch c.ManifestMode() {
	case "mirror":
		if m.GitOps != nil {
			return fmt.Errorf("manifests.gitops requires manifests.mode: gitops; set mode: gitops and remove manifests.git, or remove manifests.gitops to retain the SQLite audit mirror")
		}
	case "gitops":
		if m.Git != nil {
			return fmt.Errorf("gitops mode does not auto-commit or push desired specs; remove manifests.git and set manifests.gitops.remote")
		}
		if m.GitOps == nil || strings.TrimSpace(m.GitOps.Remote) == "" {
			return fmt.Errorf("manifests.mode: gitops requires manifests.gitops.remote (the upstream source repository)")
		}
		if m.GitOps.Enabled != nil && !*m.GitOps.Enabled {
			return fmt.Errorf("manifests.mode: gitops enables the watcher; remove manifests.gitops.enabled: false or select mode: mirror and remove manifests.gitops")
		}
		if p := m.GitOps.Pull; p != nil {
			if p.Enabled != nil && !*p.Enabled {
				return fmt.Errorf("manifests.gitops.pull is enabled by its presence; remove the pull block to disable pulling, or remove enabled: false")
			}
			if p.Interval != "" {
				d, err := time.ParseDuration(p.Interval)
				if err != nil || d <= 0 {
					return fmt.Errorf("manifests.gitops.pull.interval must be a positive duration (for example 1m)")
				}
			}
		}
	default:
		return fmt.Errorf("invalid manifests.mode %q: choose mirror (SQLite authority) or gitops (upstream repository authority)", m.Mode)
	}
	return nil
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

	if ctx.SnippetSSH != nil {
		snippetSSH := *ctx.SnippetSSH
		identityFile, err := ExpandPath(snippetSSH.IdentityFile)
		if err != nil {
			return nil, fmt.Errorf("resolve snippet SSH identity file: %w", err)
		}
		snippetSSH.IdentityFile = identityFile
		cfg.SnippetSSH = &snippetSSH
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

// ProviderContextConfigs resolves every named context of a provider into a
// ProviderConfig, keyed by context name, and returns the provider's default
// context name alongside. Providers with no contexts yield an empty map.
//
// This is the multi-endpoint counterpart to GetProviderConfig: the controller
// uses it to build one client per Proxmox endpoint, so a single set of
// resources can span endpoints by selecting one via a per-manifest context.
func (c *Config) ProviderContextConfigs(providerName string) (map[string]*protocol.ProviderConfig, string, error) {
	provider, ok := c.Providers[providerName]
	if !ok {
		return map[string]*protocol.ProviderConfig{}, "", nil
	}
	out := make(map[string]*protocol.ProviderConfig, len(provider.Contexts))
	for name := range provider.Contexts {
		cfg, err := c.GetProviderConfig(providerName, name)
		if err != nil {
			return nil, "", fmt.Errorf("resolve context %q: %w", name, err)
		}
		out[name] = cfg
	}
	return out, provider.DefaultContext, nil
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
