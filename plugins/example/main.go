// Command openctl-example is a reference external provider for openctl's v2
// plugin protocol (pkg/pluginproto). It manages a single toy resource kind,
// example.openctl.io/v1 Note, backed by files on disk — a complete, runnable
// template for authoring your own out-of-process provider.
//
// Usage in ~/.openctl/config.yaml:
//
//	providers:
//	  example:
//	    command: openctl-example
//	    args: [plugin-serve]
//	    defaults:
//	      dir: /var/lib/openctl-example   # where Notes are stored
//
// The controller spawns `openctl-example plugin-serve`, performs the v2
// handshake, and speaks the protocol over stdio. Anything the provider writes
// to stderr shows up in the controller log; stdout is the protocol channel and
// must not be written to directly.
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
		fmt.Fprintln(os.Stderr, "usage: openctl-example plugin-serve")
		fmt.Fprintln(os.Stderr, "(this binary is an openctl v2 plugin; run it via the controller, not directly)")
		os.Exit(2)
	}
	if err := pluginproto.Serve(newProvider()); err != nil {
		fmt.Fprintf(os.Stderr, "openctl-example: serve error: %v\n", err)
		os.Exit(1)
	}
}
