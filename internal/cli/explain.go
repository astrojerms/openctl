package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/openctl/openctl/internal/schema"
)

// newExplainCommand surfaces the status outputs a kind declares — the values
// another resource can pull with a $ref. Answers "what can I reference off a
// Cluster?" without applying anything and reading the result back.
//
// Works offline for built-in kinds (proxmox, k3s), whose schemas are embedded
// in the CLI. External plugin kinds declare outputs the same way, but their
// schemas live in the controller — surfaced there and in the UI.
func newExplainCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "explain <apiVersion> <kind>",
		Short: "Show the status outputs a kind exposes for $ref",
		Long: "Show the fields a kind publishes in its status — the values another " +
			"resource can reference with a $ref (e.g. a HelmRelease's kubeconfigPath " +
			"from a Cluster's status.outputs.kubeconfigPath).",
		Example: "  openctl explain k3s.openctl.io/v1 Cluster",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			apiVersion, kind := args[0], args[1]
			outs, ok := schema.OutputsFor(apiVersion, kind)
			if !ok {
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s declares no status outputs.\n", apiVersion, kind)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s — referenceable status fields ($ref field):\n\n", apiVersion, kind)
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			for _, o := range outs {
				fmt.Fprintf(tw, "  %s\t%s\t%s\n", o.Path, o.Type, o.Doc)
			}
			return tw.Flush()
		},
	}
	return cmd
}
