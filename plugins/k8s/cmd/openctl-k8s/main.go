// Command openctl-k8s is a native openctl provider (v2 plugin protocol) for
// deploying Kubernetes workloads. Phase 1 manages k8s.openctl.io/v1 HelmRelease
// via the Helm Go SDK against a supplied kubeconfig — the engine of openctl's
// unified deployment model (see docs/deployment-model.md).
//
// Usage in ~/.openctl/config.yaml:
//
//	providers:
//	  k8s:
//	    command: openctl-k8s
//	    args: [plugin-serve]
//
// The controller spawns `openctl-k8s plugin-serve` and speaks the protocol over
// stdio; stdout is the protocol channel, diagnostics go to stderr.
package main

import (
	"fmt"
	"os"

	"github.com/openctl/openctl-k8s/internal/provider"
	"github.com/openctl/openctl/pkg/pluginproto"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "plugin-serve" {
		fmt.Fprintln(os.Stderr, "usage: openctl-k8s plugin-serve")
		fmt.Fprintln(os.Stderr, "(this binary is an openctl v2 plugin; run it via the controller, not directly)")
		os.Exit(2)
	}
	if err := pluginproto.Serve(provider.New()); err != nil {
		fmt.Fprintf(os.Stderr, "openctl-k8s: serve error: %v\n", err)
		os.Exit(1)
	}
}
