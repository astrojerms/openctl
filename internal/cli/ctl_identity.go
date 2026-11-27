package cli

import (
	"fmt"
	"strings"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/metadata"
)

func newCtlConfirmIdentityCommand() *cobra.Command {
	var apiVersion, providerContext, identity string
	cmd := &cobra.Command{
		Use:   "confirm-identity <kind> <name> --identity <verified-fingerprint>",
		Short: "Bind an already-tracked VM to its verified physical identity without changing it",
		Long:  "One-time upgrade from name-only VM tracking. Independently verify the context, node, VMID and fingerprint shown by ctl discover before confirming. Requires an admin, an idle controller, and GitOps disabled. This does not adopt, Apply, reboot or delete the VM.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(identity) == "" {
				return fmt.Errorf("--identity must be the independently verified discovery fingerprint")
			}
			conn, token, err := dialController()
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()
			ctx := metadata.AppendToOutgoingContext(cmd.Context(), "authorization", "Bearer "+token)
			result, err := apiv1.NewResourceServiceClient(conn).ConfirmResourceIdentity(ctx, &apiv1.ConfirmResourceIdentityRequest{
				ApiVersion: apiVersion, Kind: args[0], Name: args[1], Identity: identity, ProviderContext: providerContext,
			})
			if err != nil {
				return fmt.Errorf("controller identity confirmation: %w", err)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), result.GetMessage())
			return err
		},
	}
	cmd.Flags().StringVar(&apiVersion, "api-version", "proxmox.openctl.io/v1", "tracked resource apiVersion")
	cmd.Flags().StringVar(&providerContext, "provider-context", "", "must match the saved desired VM placement")
	cmd.Flags().StringVar(&identity, "identity", "", "verified physical fingerprint from controller discovery")
	return cmd
}
