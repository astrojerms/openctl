package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openctl/openctl/pkg/pluginproto"
	"github.com/openctl/openctl/pkg/protocol"
)

const (
	providerName  = "cloudflare"
	apiVersion    = "cloudflare.openctl.io/v1"
	kindDNSRecord = "DNSRecord"
	kindTunnel    = "Tunnel"
)

// provider is the Cloudflare pluginproto Handler. It manages DNS records and
// Cloudflare Tunnels via the REST API v4, persisting server-assigned IDs
// through openctl's opaque state blobs (CapabilityState).
type provider struct {
	pluginproto.UnimplementedHandler
	cfg    cfConfig
	client *cfClient
}

func newProvider() *provider { return &provider{} }

// cfConfig is the shape the provider expects in the configure bag. It matches
// the JSON of openctl's protocol.ProviderConfig: the API token arrives in
// TokenSecret (from a credential's tokenSecretFile), and account/zone defaults
// come from Defaults.
type cfConfig struct {
	Endpoint    string            `json:"endpoint,omitempty"`
	TokenSecret string            `json:"tokenSecret,omitempty"`
	Defaults    map[string]string `json:"defaults,omitempty"`
}

func (p *provider) Handshake(context.Context) (*pluginproto.HandshakeResult, error) {
	return &pluginproto.HandshakeResult{
		ProviderName:    providerName,
		ProtocolVersion: pluginproto.ProtocolVersion,
		Capabilities: []string{
			pluginproto.CapabilitySchema,
			pluginproto.CapabilityState,
			pluginproto.CapabilityActions,
		},
		Kinds: []pluginproto.KindInfo{
			{Kind: kindDNSRecord, Schema: dnsRecordSchema},
			{Kind: kindTunnel, Schema: tunnelSchema, Actions: []string{actionGetToken}},
			{Kind: kindTunnelRoute, Schema: tunnelRouteSchema},
		},
	}, nil
}

func (p *provider) Configure(_ context.Context, raw json.RawMessage) error {
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p.cfg); err != nil {
			return pluginproto.Unsupported("bad config: " + err.Error())
		}
	}
	if p.cfg.TokenSecret == "" {
		return fmt.Errorf("cloudflare: no API token — set a credential with tokenSecretFile in the provider config")
	}
	p.client = newClient(p.cfg.Endpoint, p.cfg.TokenSecret)
	return nil
}

func (p *provider) Apply(ctx context.Context, req pluginproto.ApplyParams) (*pluginproto.ApplyResult, error) {
	m := req.Manifest
	if m == nil {
		return nil, pluginproto.Unsupported("apply: nil manifest")
	}
	switch m.Kind {
	case kindDNSRecord:
		return p.applyDNSRecord(ctx, m, req.State)
	case kindTunnel:
		return p.applyTunnel(ctx, m, req.State)
	case kindTunnelRoute:
		return p.applyTunnelRoute(ctx, m, req.State)
	default:
		return nil, pluginproto.Unsupported("cloudflare handles DNSRecord, Tunnel and TunnelRoute, not " + m.Kind)
	}
}

func (p *provider) Get(ctx context.Context, req pluginproto.GetParams) (*pluginproto.GetResult, error) {
	switch req.Kind {
	case kindDNSRecord:
		return p.getDNSRecord(ctx, req.Name, req.State)
	case kindTunnel:
		return p.getTunnel(ctx, req.Name, req.State)
	case kindTunnelRoute:
		return p.getTunnelRoute(ctx, req.Name, req.State)
	default:
		return nil, pluginproto.Unsupported("cloudflare handles DNSRecord, Tunnel and TunnelRoute, not " + req.Kind)
	}
}

func (p *provider) List(ctx context.Context, kind string) ([]*protocol.Resource, error) {
	switch kind {
	case kindDNSRecord:
		return p.listDNSRecords(ctx)
	case kindTunnel:
		return p.listTunnels(ctx)
	default:
		return nil, nil
	}
}

func (p *provider) Delete(ctx context.Context, req pluginproto.DeleteParams) error {
	switch req.Kind {
	case kindDNSRecord:
		return p.deleteDNSRecord(ctx, req.State)
	case kindTunnel:
		return p.deleteTunnel(ctx, req.State)
	case kindTunnelRoute:
		return p.deleteTunnelRoute(ctx, req.State)
	default:
		return nil
	}
}

const actionGetToken = "get-token"

func (p *provider) DoAction(ctx context.Context, req pluginproto.DoActionParams) (*pluginproto.DoActionResult, error) {
	if req.Kind == kindTunnel && req.Action == actionGetToken {
		return p.tunnelToken(ctx, req.Name)
	}
	return nil, pluginproto.Unsupported(fmt.Sprintf("unknown action %q for %s", req.Action, req.Kind))
}

// --- spec helpers: manifest specs arrive as map[string]any (JSON), so numbers
// are float64 and every field is optional/interface-typed. ---

func specString(spec map[string]any, key string) string {
	s, _ := spec[key].(string)
	return s
}

func specInt(spec map[string]any, key string) (int, bool) {
	switch v := spec[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	default:
		return 0, false
	}
}

func specBool(spec map[string]any, key string) (bool, bool) {
	b, ok := spec[key].(bool)
	return b, ok
}

// ensure the provider satisfies the Handler contract at compile time.
var _ pluginproto.Handler = (*provider)(nil)
