package server

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/openctl/openctl/internal/config"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

// Defaults surfaced when a config block is omitted. Kept in sync with the
// controller entrypoint (reconciler.DefaultInterval, config.DefaultRetainPerResource)
// so Get returns the same values the controller would actually run with.
const (
	defaultReconcilerInterval = "5m"
)

// configHandler implements apiv1.ConfigServiceServer. Reads and
// writes provider credentials in ~/.openctl/config.yaml. Never
// returns secrets over the wire — has_secret is the only signal the
// UI gets. Writing an empty token_secret preserves whatever's there
// (so the UI can edit endpoint/tokenId without re-typing).
type configHandler struct {
	apiv1.UnimplementedConfigServiceServer
}

func newConfigHandler() *configHandler { return &configHandler{} }

func (h *configHandler) ListProviders(_ context.Context, _ *apiv1.ListProvidersRequest) (*apiv1.ListProvidersResponse, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load config: %v", err)
	}
	out := &apiv1.ListProvidersResponse{}
	for name, p := range cfg.Providers {
		out.Providers = append(out.Providers, providerEntry(name, p))
	}
	return out, nil
}

func (h *configHandler) UpsertProvider(_ context.Context, req *apiv1.UpsertProviderRequest) (*apiv1.UpsertProviderResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load config: %v", err)
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]*config.Provider{}
	}
	p := cfg.Providers[req.GetName()]
	if p == nil {
		// Fresh provider: seed with a single "default" context +
		// credential. Matches the shape the UI understands.
		p = &config.Provider{
			DefaultContext: "default",
			Contexts:       map[string]*config.Context{},
			Credentials:    map[string]*config.Credential{},
			Defaults:       map[string]string{},
		}
		cfg.Providers[req.GetName()] = p
	}
	if p.Contexts == nil {
		p.Contexts = map[string]*config.Context{}
	}
	if p.Credentials == nil {
		p.Credentials = map[string]*config.Credential{}
	}
	if p.DefaultContext == "" {
		p.DefaultContext = "default"
	}
	ctxName := p.DefaultContext
	credName := "default"
	if p.Contexts[ctxName] == nil {
		p.Contexts[ctxName] = &config.Context{Credentials: credName}
	}
	p.Contexts[ctxName].Endpoint = req.GetEndpoint()
	if p.Credentials[credName] == nil {
		p.Credentials[credName] = &config.Credential{}
	}
	p.Credentials[credName].TokenID = req.GetTokenId()
	// Only overwrite the secret when a new one is supplied. Empty
	// means "preserve whatever's already on file" — the UI never
	// receives the current value, so re-sending empty on an edit
	// mustn't clobber a stored secret.
	if req.GetTokenSecret() != "" {
		p.Credentials[credName].TokenSecret = req.GetTokenSecret()
		// If a tokenSecretFile was in use, clear it — inline wins so
		// the yaml on disk reflects reality.
		p.Credentials[credName].TokenSecretFile = ""
	}
	if err := cfg.Save(); err != nil {
		return nil, status.Errorf(codes.Internal, "save config: %v", err)
	}
	return &apiv1.UpsertProviderResponse{Provider: providerEntry(req.GetName(), p)}, nil
}

func (h *configHandler) DeleteProvider(_ context.Context, req *apiv1.DeleteProviderRequest) (*apiv1.DeleteProviderResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load config: %v", err)
	}
	delete(cfg.Providers, req.GetName())
	if err := cfg.Save(); err != nil {
		return nil, status.Errorf(codes.Internal, "save config: %v", err)
	}
	return &apiv1.DeleteProviderResponse{}, nil
}

// GetControllerConfig returns the editable controller tunables with defaults
// filled in for omitted blocks, so the UI always renders concrete values.
func (h *configHandler) GetControllerConfig(_ context.Context, _ *apiv1.GetControllerConfigRequest) (*apiv1.GetControllerConfigResponse, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load config: %v", err)
	}
	return &apiv1.GetControllerConfigResponse{
		Config:          controllerConfigEntry(cfg),
		RestartRequired: true,
	}, nil
}

// UpdateControllerConfig merges the submitted tunables into config.yaml and
// saves. Validates the reconcile interval as a Go duration and rejects a
// negative retention. Only the reconciler + operations blocks are touched;
// everything else in the file is preserved.
func (h *configHandler) UpdateControllerConfig(_ context.Context, req *apiv1.UpdateControllerConfigRequest) (*apiv1.UpdateControllerConfigResponse, error) {
	in := req.GetConfig()
	if in == nil {
		return nil, status.Error(codes.InvalidArgument, "config is required")
	}
	interval := in.GetReconcilerInterval()
	if interval != "" {
		if _, perr := time.ParseDuration(interval); perr != nil {
			return nil, status.Errorf(codes.InvalidArgument, "reconciler_interval %q is not a valid duration: %v", interval, perr)
		}
	}
	if in.GetOpRetainPerResource() < 0 {
		return nil, status.Error(codes.InvalidArgument, "op_retain_per_resource must be non-negative")
	}

	cfg, err := config.Load()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load config: %v", err)
	}

	// Reconciler block: always materialize it so an explicit enabled=false or
	// a custom interval round-trips (the pointer distinguishes unset).
	enabled := in.GetReconcilerEnabled()
	if cfg.Reconciler == nil {
		cfg.Reconciler = &config.Reconciler{}
	}
	cfg.Reconciler.Enabled = &enabled
	cfg.Reconciler.Interval = interval // "" is fine — means "use default"

	// Operations block: 0 means "use the default", so drop the block to keep
	// the file clean rather than persisting a misleading literal 0.
	retain := int(in.GetOpRetainPerResource())
	if retain > 0 {
		cfg.Operations = &config.Operations{RetainPerResource: retain}
	} else {
		cfg.Operations = nil
	}

	if err := cfg.Save(); err != nil {
		return nil, status.Errorf(codes.Internal, "save config: %v", err)
	}
	return &apiv1.UpdateControllerConfigResponse{
		Config:          controllerConfigEntry(cfg),
		RestartRequired: true,
	}, nil
}

// controllerConfigEntry projects the controller-behavior blocks of a Config
// into the wire shape, filling built-in defaults for omitted blocks.
func controllerConfigEntry(cfg *config.Config) *apiv1.ControllerConfig {
	out := &apiv1.ControllerConfig{
		ReconcilerEnabled:   true,
		ReconcilerInterval:  defaultReconcilerInterval,
		OpRetainPerResource: int32(config.DefaultRetainPerResource),
	}
	if cfg == nil {
		return out
	}
	if r := cfg.Reconciler; r != nil {
		if r.Enabled != nil {
			out.ReconcilerEnabled = *r.Enabled
		}
		if r.Interval != "" {
			out.ReconcilerInterval = r.Interval
		}
	}
	if o := cfg.Operations; o != nil && o.RetainPerResource > 0 {
		out.OpRetainPerResource = int32(o.RetainPerResource)
	}
	return out
}

// providerEntry projects a config.Provider into the wire shape,
// scrubbing secrets. Returns endpoint/tokenId from the default
// context+credential; empty when the provider has no default set.
func providerEntry(name string, p *config.Provider) *apiv1.ProviderEntry {
	entry := &apiv1.ProviderEntry{Name: name}
	if p == nil {
		return entry
	}
	ctxName := p.DefaultContext
	if ctxName == "" {
		return entry
	}
	ctx := p.Contexts[ctxName]
	if ctx == nil {
		return entry
	}
	entry.Endpoint = ctx.Endpoint
	if ctx.Credentials == "" {
		return entry
	}
	cred := p.Credentials[ctx.Credentials]
	if cred == nil {
		return entry
	}
	entry.TokenId = cred.TokenID
	entry.HasSecret = cred.TokenSecret != "" || cred.TokenSecretFile != ""
	entry.UsesSecretFile = cred.TokenSecretFile != ""
	return entry
}
