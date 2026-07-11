// Package tfhost is openctl's Terraform/OpenTofu provider host: it launches an
// unmodified terraform-provider-* binary over HashiCorp's go-plugin transport
// and drives it with the Terraform plugin protocol. Both protocol 5 (SDKv2
// providers) and protocol 6 (plugin-framework providers) are negotiated at
// launch; the adapter internally speaks tfplugin6 types and converts protocol-5
// messages at the client boundary (client_v5.go). This is the "second
// implementer" of openctl's provider contract from docs/plugin-architecture.md
// — one adapter unlocks the whole provider registry.
//
// This file is the transport: launch a provider, negotiate the protocol, and
// fetch its schema. The openctl providers.Provider adapter (mapping
// Apply/Get/Delete onto Plan/Apply/Read + the provider_state store, encoding
// specs to msgpack DynamicValues and decoding state back — see values.go)
// builds on top.
package tfhost

import (
	"context"
	"fmt"
	"os/exec"

	hclog "github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	"github.com/openctl/openctl/pkg/tfplugin5"
	"github.com/openctl/openctl/pkg/tfplugin6"
)

// magicCookie is the fixed go-plugin handshake secret every Terraform provider
// checks; a provider refuses to start if it doesn't match. It is the same for
// protocol 5 and 6 — the protocol version is negotiated separately.
const magicCookie = "d602bf8f470bc67ca7faa0386276bbdd4330efaf76d1a219cb4d6991ca9872b2"

// providerPluginName is the go-plugin plugin-set key Terraform providers serve
// under.
const providerPluginName = "provider"

// grpcProvider6Plugin / grpcProvider5Plugin are the client halves of the
// go-plugin plugin for each protocol version. openctl never serves, so the
// server halves are stubs. Both are registered so Launch can negotiate whichever
// protocol the launched provider speaks (SDKv2 providers serve 5, framework
// providers serve 6).
type grpcProvider6Plugin struct {
	goplugin.NetRPCUnsupportedPlugin
}

func (grpcProvider6Plugin) GRPCServer(*goplugin.GRPCBroker, *grpc.Server) error {
	return fmt.Errorf("tfhost is a provider client, not a server")
}

func (grpcProvider6Plugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	return tfplugin6.NewProviderClient(c), nil
}

type grpcProvider5Plugin struct {
	goplugin.NetRPCUnsupportedPlugin
}

func (grpcProvider5Plugin) GRPCServer(*goplugin.GRPCBroker, *grpc.Server) error {
	return fmt.Errorf("tfhost is a provider client, not a server")
}

func (grpcProvider5Plugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	return tfplugin5.NewProviderClient(c), nil
}

// tfProvider is the protocol-neutral surface the adapter drives. Both a
// protocol-6 client (passthrough) and a protocol-5 client (converting v5<->v6
// message types) satisfy it, so provider.go stays on tfplugin6 types
// regardless of which protocol the provider actually speaks.
type tfProvider interface {
	GetProviderSchema(context.Context) (*tfplugin6.GetProviderSchema_Response, error)
	ConfigureProvider(context.Context, *tfplugin6.ConfigureProvider_Request) (*tfplugin6.ConfigureProvider_Response, error)
	PlanResourceChange(context.Context, *tfplugin6.PlanResourceChange_Request) (*tfplugin6.PlanResourceChange_Response, error)
	ApplyResourceChange(context.Context, *tfplugin6.ApplyResourceChange_Request) (*tfplugin6.ApplyResourceChange_Response, error)
	ReadResource(context.Context, *tfplugin6.ReadResource_Request) (*tfplugin6.ReadResource_Response, error)
}

// v6direct is the passthrough tfProvider for a native protocol-6 provider.
type v6direct struct{ c tfplugin6.ProviderClient }

func (d v6direct) GetProviderSchema(ctx context.Context) (*tfplugin6.GetProviderSchema_Response, error) {
	return d.c.GetProviderSchema(ctx, &tfplugin6.GetProviderSchema_Request{})
}

func (d v6direct) ConfigureProvider(ctx context.Context, req *tfplugin6.ConfigureProvider_Request) (*tfplugin6.ConfigureProvider_Response, error) {
	return d.c.ConfigureProvider(ctx, req)
}

func (d v6direct) PlanResourceChange(ctx context.Context, req *tfplugin6.PlanResourceChange_Request) (*tfplugin6.PlanResourceChange_Response, error) {
	return d.c.PlanResourceChange(ctx, req)
}

func (d v6direct) ApplyResourceChange(ctx context.Context, req *tfplugin6.ApplyResourceChange_Request) (*tfplugin6.ApplyResourceChange_Response, error) {
	return d.c.ApplyResourceChange(ctx, req)
}

func (d v6direct) ReadResource(ctx context.Context, req *tfplugin6.ReadResource_Request) (*tfplugin6.ReadResource_Response, error) {
	return d.c.ReadResource(ctx, req)
}

// Client is a live connection to a launched Terraform provider process.
type Client struct {
	plugin  *goplugin.Client
	impl    tfProvider
	version int
}

// Launch starts the provider binary at path and negotiates the Terraform plugin
// protocol (5 or 6). The caller must Close the returned Client to reap the
// process.
func Launch(path string, args ...string) (*Client, error) {
	cmd := exec.Command(path, args...) //nolint:gosec // G204: provider path is operator-configured
	pc := goplugin.NewClient(&goplugin.ClientConfig{
		HandshakeConfig: goplugin.HandshakeConfig{
			MagicCookieKey:   "TF_PLUGIN_MAGIC_COOKIE",
			MagicCookieValue: magicCookie,
		},
		// Offer both protocol versions; go-plugin negotiates the one the
		// provider serves.
		VersionedPlugins: map[int]goplugin.PluginSet{
			5: {providerPluginName: grpcProvider5Plugin{}},
			6: {providerPluginName: grpcProvider6Plugin{}},
		},
		Cmd:              cmd,
		AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
		// Terraform providers are noisy on stderr; keep the controller log
		// clean unless something goes wrong.
		Logger: hclog.New(&hclog.LoggerOptions{Name: "tfhost", Level: hclog.Warn}),
	})

	rpc, err := pc.Client()
	if err != nil {
		pc.Kill()
		return nil, fmt.Errorf("start provider %q: %w", path, err)
	}
	raw, err := rpc.Dispense(providerPluginName)
	if err != nil {
		pc.Kill()
		return nil, fmt.Errorf("dispense provider: %w", err)
	}

	var impl tfProvider
	switch client := raw.(type) {
	case tfplugin6.ProviderClient:
		impl = v6direct{c: client}
	case tfplugin5.ProviderClient:
		impl = v5adapter{c: client}
	default:
		pc.Kill()
		return nil, fmt.Errorf("unexpected provider client type %T", raw)
	}
	return &Client{plugin: pc, impl: impl, version: pc.NegotiatedVersion()}, nil
}

// ProtocolVersion reports the negotiated Terraform plugin protocol (5 or 6).
func (c *Client) ProtocolVersion() int { return c.version }

// GetProviderSchema fetches the provider config schema plus the schemas of
// every resource and data source the provider offers.
func (c *Client) GetProviderSchema(ctx context.Context) (*tfplugin6.GetProviderSchema_Response, error) {
	resp, err := c.impl.GetProviderSchema(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetProviderSchema: %w", err)
	}
	return resp, nil
}

// ConfigureProvider sends the provider-level configuration Terraform core
// would normally deliver before resource operations.
func (c *Client) ConfigureProvider(ctx context.Context, req *tfplugin6.ConfigureProvider_Request) (*tfplugin6.ConfigureProvider_Response, error) {
	resp, err := c.impl.ConfigureProvider(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("ConfigureProvider: %w", err)
	}
	return resp, nil
}

// PlanResourceChange asks the provider to turn a proposed resource state into
// the concrete planned state Terraform would later apply.
func (c *Client) PlanResourceChange(ctx context.Context, req *tfplugin6.PlanResourceChange_Request) (*tfplugin6.PlanResourceChange_Response, error) {
	resp, err := c.impl.PlanResourceChange(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("PlanResourceChange: %w", err)
	}
	return resp, nil
}

// ApplyResourceChange applies a planned resource state and returns the new
// opaque provider state plus private blob.
func (c *Client) ApplyResourceChange(ctx context.Context, req *tfplugin6.ApplyResourceChange_Request) (*tfplugin6.ApplyResourceChange_Response, error) {
	resp, err := c.impl.ApplyResourceChange(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("ApplyResourceChange: %w", err)
	}
	return resp, nil
}

// ReadResource refreshes a prior resource state through the provider.
func (c *Client) ReadResource(ctx context.Context, req *tfplugin6.ReadResource_Request) (*tfplugin6.ReadResource_Response, error) {
	resp, err := c.impl.ReadResource(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("ReadResource: %w", err)
	}
	return resp, nil
}

// Close terminates the provider process.
func (c *Client) Close() {
	if c.plugin != nil {
		c.plugin.Kill()
	}
}
