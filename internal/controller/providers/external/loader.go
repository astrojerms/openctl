package external

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/pkg/pluginproto"
)

// Load spawns the plugin process described by cmd, performs the v2 handshake,
// injects the opaque config bag, and returns a providers.Provider adapter, the
// handshake result (so the caller can register plugin-supplied schemas), and
// the underlying Client. The caller MUST Close the returned Client on
// controller shutdown to reap the plugin process.
//
// config is marshaled verbatim into the configure bag (nil skips configure).
// store persists opaque provider state for stateful plugins (CapabilityState);
// pass nil for stateless plugins. If cmd.Stderr is unset, the plugin's
// diagnostic output is forwarded to the controller's stderr so plugin logs
// surface alongside controller logs.
func Load(ctx context.Context, cmd *exec.Cmd, config any, store StateStore) (providers.Provider, *pluginproto.HandshakeResult, *pluginproto.Client, error) {
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}

	client, err := pluginproto.Spawn(cmd)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("spawn plugin: %w", err)
	}

	hs, err := client.Handshake(ctx)
	if err != nil {
		_ = client.Close(ctx)
		return nil, nil, nil, fmt.Errorf("plugin handshake: %w", err)
	}
	if hs.ProtocolVersion != pluginproto.ProtocolVersion {
		_ = client.Close(ctx)
		return nil, nil, nil, fmt.Errorf("plugin protocol version %q, controller speaks %q",
			hs.ProtocolVersion, pluginproto.ProtocolVersion)
	}
	if hs.ProviderName == "" {
		_ = client.Close(ctx)
		return nil, nil, nil, fmt.Errorf("plugin returned empty provider name")
	}

	if config != nil {
		if err := client.Configure(ctx, config); err != nil {
			_ = client.Close(ctx)
			return nil, nil, nil, fmt.Errorf("configure plugin %q: %w", hs.ProviderName, err)
		}
	}

	return New(client, hs, store), hs, client, nil
}
