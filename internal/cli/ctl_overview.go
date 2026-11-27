package cli

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/metadata"
)

func newCtlOverviewCommand() *cobra.Command {
	var limit uint32
	cmd := &cobra.Command{
		Use:   "overview",
		Short: "Inspect saved inventory and root requests without querying providers",
		Long:  "Read persisted control-plane metadata, including owned desired resources, active root requests and the latest unsuccessful retained request per resource. This does not query providers or prove infrastructure health. Use a resource Get or runtime inspection for live readiness.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit > 100 {
				return fmt.Errorf("--limit must be between 0 and 100 (0 uses 10)")
			}
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
			response, err := apiv1.NewOverviewServiceClient(conn).GetOverview(ctx, &apiv1.GetOverviewRequest{OperationLimit: limit})
			if err != nil {
				return fmt.Errorf("controller overview: %w", err)
			}
			if _, err := time.Parse(time.RFC3339Nano, response.GetAsOf()); err != nil {
				return fmt.Errorf("controller did not return a valid overview snapshot time; inspect its version and state")
			}
			if handled, err := printControllerStructured(cmd.OutOrStdout(), response); handled {
				return err
			}
			return printCtlOverview(cmd.OutOrStdout(), response)
		},
	}
	cmd.Flags().Uint32Var(&limit, "limit", 10, "maximum rows in each operation list (0 uses 10; max 100; totals remain exact)")
	return cmd
}

func printCtlOverview(out io.Writer, response *apiv1.GetOverviewResponse) error {
	if _, err := fmt.Fprintf(out, "Control-plane snapshot: %s\nSaved desired resources: %d (including owned children)\nRoot requests: %d pending, %d running\nLatest unsuccessful retained requests needing review: %d\nNot live health: no providers or secrets were read.\n\n", response.GetAsOf(), response.GetManagedResources(), response.GetPendingOperations(), response.GetRunningOperations(), response.GetReviewResources()); err != nil {
		return err
	}
	table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "API VERSION\tKIND\tSAVED DESIRED RESOURCES"); err != nil {
		return err
	}
	for _, kind := range response.GetKinds() {
		if _, err := fmt.Fprintf(table, "%s\t%s\t%d\n", kind.GetApiVersion(), kind.GetKind(), kind.GetResources()); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	for _, section := range [...]struct {
		name  string
		total uint64
		rows  []*apiv1.Operation
	}{
		{"Active root requests", uint64(response.GetPendingOperations()) + uint64(response.GetRunningOperations()), response.GetActiveOperations()},
		{"Latest requests needing review", uint64(response.GetReviewResources()), response.GetReviewOperations()},
	} {
		if _, err := fmt.Fprintf(out, "\n%s: showing %d of %d\n", section.name, len(section.rows), section.total); err != nil {
			return err
		}
		table = tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(table, "ID\tTYPE\tAPI VERSION\tKIND\tNAME\tSTATUS\tSUBMITTED"); err != nil {
			return err
		}
		for _, op := range section.rows {
			if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", op.GetId(), op.GetType(), op.GetApiVersion(), op.GetKind(), op.GetResourceName(), op.GetStatus(), op.GetSubmittedAt()); err != nil {
				return err
			}
		}
		if err := table.Flush(); err != nil {
			return err
		}
	}
	return nil
}
