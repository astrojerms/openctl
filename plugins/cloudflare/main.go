// Command openctl-cloudflare is an external provider for openctl's v2 plugin
// protocol (pkg/pluginproto). It manages Cloudflare infrastructure —
// cloudflare.openctl.io/v1 DNSRecord and Tunnel — via the Cloudflare REST API
// v4, persisting server-assigned IDs through openctl's state store.
//
// Usage in ~/.openctl/config.yaml:
//
//	providers:
//	  cloudflare:
//	    command: openctl-cloudflare
//	    args: [plugin-serve]
//	    contexts:
//	      prod: { credentials: cf }
//	    credentials:
//	      cf: { tokenSecretFile: ~/.openctl/cloudflare.token }
//	    defaults:
//	      zoneId: <zone-id>       # default zone for DNSRecord + list
//	      accountId: <account-id> # default account for Tunnel + list
//
// The controller spawns `openctl-cloudflare plugin-serve`, performs the v2
// handshake, and speaks the protocol over stdio. stdout is the protocol
// channel; all diagnostics must go to stderr.
//
// See docs/plugin-protocol.md for the full protocol reference.
package main

import (
	"fmt"
	"os"

	"github.com/openctl/openctl/pkg/pluginproto"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "plugin-serve" {
		fmt.Fprintln(os.Stderr, "usage: openctl-cloudflare plugin-serve")
		fmt.Fprintln(os.Stderr, "(this binary is an openctl v2 plugin; run it via the controller, not directly)")
		os.Exit(2)
	}
	if err := pluginproto.Serve(newProvider()); err != nil {
		fmt.Fprintf(os.Stderr, "openctl-cloudflare: serve error: %v\n", err)
		os.Exit(1)
	}
}
