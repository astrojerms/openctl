// Package tfhost is openctl's Terraform/OpenTofu provider host: it launches an
// unmodified terraform-provider-* binary over HashiCorp's go-plugin transport
// and drives it with the Terraform plugin protocol v6 (tfplugin6). This is the
// "second implementer" of openctl's provider contract from
// docs/plugin-architecture.md — one adapter unlocks the whole provider
// registry.
//
// Phase A (this file) is the transport: launch a provider, complete the
// go-plugin handshake, and fetch its schema. The openctl providers.Provider
// adapter (mapping Apply/Get/Delete onto Plan/Apply/Read + the provider_state
// store) builds on top.
package tfhost

import (
	"context"
	"fmt"
	"os/exec"

	hclog "github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	"github.com/openctl/openctl/pkg/tfplugin6"
)

// tfHandshake is the go-plugin handshake every Terraform provider expects. The
// values are fixed by the Terraform plugin protocol (mirrored from
// terraform-plugin-go's tf6server) — a provider refuses to start if they don't
// match.
var tfHandshake = goplugin.HandshakeConfig{
	ProtocolVersion:  6,
	MagicCookieKey:   "TF_PLUGIN_MAGIC_COOKIE",
	MagicCookieValue: "d602bf8f470bc67ca7faa0386276bbdd4330efaf76d1a219cb4d6991ca9872b2",
}

// providerPluginName is the go-plugin plugin-set key Terraform providers serve
// under.
const providerPluginName = "provider"

// grpcProviderPlugin is the client half of the go-plugin plugin: it turns a
// gRPC connection into a tfplugin6.ProviderClient. openctl never serves, so the
// server half is a stub.
type grpcProviderPlugin struct {
	goplugin.NetRPCUnsupportedPlugin
}

func (grpcProviderPlugin) GRPCServer(*goplugin.GRPCBroker, *grpc.Server) error {
	return fmt.Errorf("tfhost is a provider client, not a server")
}

func (grpcProviderPlugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	return tfplugin6.NewProviderClient(c), nil
}

// Client is a live connection to a launched Terraform provider process.
type Client struct {
	plugin   *goplugin.Client
	provider tfplugin6.ProviderClient
}

// Launch starts the provider binary at path and completes the tfplugin6
// handshake. The caller must Close the returned Client to reap the process.
func Launch(path string, args ...string) (*Client, error) {
	cmd := exec.Command(path, args...) //nolint:gosec // G204: provider path is operator-configured
	pc := goplugin.NewClient(&goplugin.ClientConfig{
		HandshakeConfig:  tfHandshake,
		Plugins:          goplugin.PluginSet{providerPluginName: grpcProviderPlugin{}},
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
	provider, ok := raw.(tfplugin6.ProviderClient)
	if !ok {
		pc.Kill()
		return nil, fmt.Errorf("unexpected provider client type %T", raw)
	}
	return &Client{plugin: pc, provider: provider}, nil
}

// GetProviderSchema fetches the provider config schema plus the schemas of
// every resource and data source the provider offers.
func (c *Client) GetProviderSchema(ctx context.Context) (*tfplugin6.GetProviderSchema_Response, error) {
	resp, err := c.provider.GetProviderSchema(ctx, &tfplugin6.GetProviderSchema_Request{})
	if err != nil {
		return nil, fmt.Errorf("GetProviderSchema: %w", err)
	}
	return resp, nil
}

// Close terminates the provider process.
func (c *Client) Close() {
	if c.plugin != nil {
		c.plugin.Kill()
	}
}
