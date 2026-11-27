package cli

import (
	"context"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/metadata"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

func ctlSaved(cmd *cobra.Command, apiVersion, kind, name string) error {
	timeout := globalTimeout
	if timeout < 0 {
		return fmt.Errorf("timeout must not be negative")
	}
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	conn, token, err := dialController()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	client := apiv1.NewInventoryServiceClient(conn)
	if name != "" {
		response, err := client.GetSavedResource(ctx, &apiv1.GetRequest{ApiVersion: apiVersion, Kind: kind, Name: name})
		if err != nil {
			return fmt.Errorf("read saved desired intent: %w", err)
		}
		resource := response.GetResource()
		if resource == nil || resource.GetApiVersion() != apiVersion || resource.GetKind() != kind || resource.GetMetadata().GetName() != name {
			return fmt.Errorf("controller did not return the requested saved declaration")
		}
		if resource.GetStatus() != nil || len(resource.GetDrift()) != 0 || len(resource.GetChildren()) != 0 || len(resource.GetMetadata().GetOwnerRefs()) != 0 {
			return fmt.Errorf("controller mixed observed state into saved intent; refusing to export it")
		}
		format := outputFormat
		if format != "yaml" {
			format = "json"
		}
		return printControllerMessage(cmd.OutOrStdout(), resource, format)
	}
	response, err := client.ListSavedResources(ctx, &apiv1.ListRequest{ApiVersion: apiVersion, Kind: kind})
	if err != nil {
		return fmt.Errorf("list saved desired inventory: %w", err)
	}
	if handled, err := printControllerStructured(cmd.OutOrStdout(), response); handled {
		return err
	}
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Saved desired inventory only; not live health, ownership or replacement authorization."); err != nil {
		return err
	}
	out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(out, "NAME\tSOURCE\tLAST APPLIED"); err != nil {
		return err
	}
	for _, resource := range response.GetResources() {
		if _, err := fmt.Fprintf(out, "%s\t%s\t%s\n", resource.GetMetadata().GetName(), resource.GetSource(), resource.GetAppliedAt()); err != nil {
			return err
		}
	}
	return out.Flush()
}
