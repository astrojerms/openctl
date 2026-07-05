// Command tf-fake is a minimal Terraform plugin-protocol-v6 provider used to
// test openctl's Terraform host (internal/controller/providers/tfhost) without
// downloading a real terraform-provider-* binary. It speaks the same
// go-plugin + tfplugin6 gRPC protocol a real provider does, so a client that
// works against it works against the real registry.
//
// It advertises one resource, "fake_thing", with a tiny schema, and implements
// only GetProviderSchema meaningfully (the rest inherit
// UnimplementedProviderServer). Later tfhost phases will grow this fake as the
// Plan/Apply/Read paths come online.
package main

import (
	"context"
	"fmt"
	"os"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	"github.com/openctl/openctl/pkg/tfplugin6"
)

var handshake = goplugin.HandshakeConfig{
	ProtocolVersion:  6,
	MagicCookieKey:   "TF_PLUGIN_MAGIC_COOKIE",
	MagicCookieValue: "d602bf8f470bc67ca7faa0386276bbdd4330efaf76d1a219cb4d6991ca9872b2",
}

// fakeServer implements just enough of the provider server for schema tests.
type fakeServer struct {
	tfplugin6.UnimplementedProviderServer
}

func (fakeServer) GetProviderSchema(context.Context, *tfplugin6.GetProviderSchema_Request) (*tfplugin6.GetProviderSchema_Response, error) {
	strAttr := func(name string, required bool) *tfplugin6.Schema_Attribute {
		return &tfplugin6.Schema_Attribute{
			Name:     name,
			Type:     []byte(`"string"`), // tftypes JSON encoding of the string type
			Required: required,
			Optional: !required,
		}
	}
	return &tfplugin6.GetProviderSchema_Response{
		Provider: &tfplugin6.Schema{
			Version: 0,
			Block: &tfplugin6.Schema_Block{
				Attributes: []*tfplugin6.Schema_Attribute{strAttr("endpoint", false)},
			},
		},
		ResourceSchemas: map[string]*tfplugin6.Schema{
			"fake_thing": {
				Version: 1,
				Block: &tfplugin6.Schema_Block{
					Attributes: []*tfplugin6.Schema_Attribute{
						strAttr("name", true),
						strAttr("note", false),
					},
				},
			},
		},
	}, nil
}

// providerPlugin is the go-plugin server half.
type providerPlugin struct {
	goplugin.NetRPCUnsupportedPlugin
}

// GRPCServer registers the fake provider. The nil return is required by
// go-plugin's GRPCPlugin interface signature.
func (providerPlugin) GRPCServer(_ *goplugin.GRPCBroker, s *grpc.Server) error { //nolint:unparam // signature fixed by go-plugin
	tfplugin6.RegisterProviderServer(s, fakeServer{})
	return nil
}

func (providerPlugin) GRPCClient(context.Context, *goplugin.GRPCBroker, *grpc.ClientConn) (any, error) {
	return nil, fmt.Errorf("tf-fake is a server, not a client")
}

func main() {
	// Refuse to run outside a go-plugin handshake so a human running it
	// directly gets a hint instead of a hang.
	if os.Getenv(handshake.MagicCookieKey) != handshake.MagicCookieValue {
		fmt.Fprintln(os.Stderr, "tf-fake is a Terraform-protocol test provider; run it via the openctl tfhost, not directly")
		os.Exit(2)
	}
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: handshake,
		Plugins:         goplugin.PluginSet{"provider": providerPlugin{}},
		GRPCServer:      goplugin.DefaultGRPCServer,
	})
}
