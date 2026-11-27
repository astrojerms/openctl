package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

func newCtlDiscoverCommand() *cobra.Command {
	var apiVersion, providerContext string
	var contexts, test bool
	cmd := &cobra.Command{
		Use:   "discover",
		Short: "Observe configured infrastructure without adopting or modifying it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if contexts && test {
				return fmt.Errorf("--contexts and --test are mutually exclusive")
			}
			conn, token, err := dialController()
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()
			ctx := metadata.AppendToOutgoingContext(cmd.Context(), "authorization", "Bearer "+token)
			client := apiv1.NewResourceServiceClient(conn)
			var result proto.Message
			switch {
			case contexts:
				result, err = client.ListDiscoveryContexts(ctx, &apiv1.ListDiscoveryContextsRequest{ApiVersion: apiVersion})
			case test:
				result, err = client.TestDiscoveryConnection(ctx, &apiv1.TestDiscoveryConnectionRequest{ApiVersion: apiVersion, Context: providerContext})
			default:
				result, err = client.DiscoverResources(ctx, &apiv1.DiscoverResourcesRequest{ApiVersion: apiVersion, Context: providerContext})
			}
			if err != nil {
				return fmt.Errorf("controller discovery: %w", err)
			}
			if handled, err := printControllerStructured(cmd.OutOrStdout(), result); handled {
				return err
			}
			out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			switch response := result.(type) {
			case *apiv1.ListDiscoveryContextsResponse:
				fmt.Fprintln(out, "CONTEXT\tENDPOINT\tDEFAULT")
				for _, c := range response.GetContexts() {
					fmt.Fprintf(out, "%s\t%s\t%t\n", c.GetName(), c.GetEndpoint(), c.GetDefaultContext())
				}
			case *apiv1.TestDiscoveryConnectionResponse:
				fmt.Fprintf(out, "CONNECTED\tMESSAGE\n%t\t%s\n", response.GetConnected(), response.GetMessage())
			case *apiv1.DiscoverResourcesResponse:
				fmt.Fprintln(out, "CONTEXT\tNODE\tVMID\tNAME\tMANAGED\tADOPTABLE\tCONFIRM_IDENTITY\tIDENTITY\tREASON")
				for _, r := range response.GetResources() {
					fmt.Fprintf(out, "%s\t%s\t%d\t%s\t%t\t%t\t%t\t%s\t%s\n", r.GetContext(), r.GetNode(), r.GetVmid(), r.GetResource().GetMetadata().GetName(), r.GetManaged(), r.GetAdoptable(), r.GetIdentityConfirmationRequired(), r.GetIdentity(), r.GetReason())
				}
			}
			return out.Flush()
		},
	}
	cmd.Flags().StringVar(&apiVersion, "api-version", "proxmox.openctl.io/v1", "provider apiVersion")
	cmd.Flags().StringVar(&providerContext, "provider-context", "", "controller-side context (default: provider default)")
	cmd.Flags().BoolVar(&contexts, "contexts", false, "list configured controller-side contexts")
	cmd.Flags().BoolVar(&test, "test", false, "test provider connectivity without changing resources")
	return cmd
}
