// CLI commands that route through the controller. Lives behind `openctl
// ctl ...` for now so it doesn't conflict with the legacy exec-plugin
// commands; in Phase 6 the exec-plugin tree gets removed and the `ctl`
// subcommands graduate to top-level (`openctl apply`, `openctl get`, etc.).
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/openctl/openctl/internal/schema"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
)

func newCtlCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ctl",
		Short: "Resource operations via the controller",
		Long: `Run resource operations through the controller. Equivalent to the legacy
exec-plugin commands (e.g. ` + "`openctl proxmox apply`" + `) but routed through
the persistent controller. In Phase 6 these graduate to top-level commands
and the exec-plugin paths are removed.`,
	}
	cmd.AddCommand(newCtlApplyCommand())
	cmd.AddCommand(newCtlGetCommand())
	cmd.AddCommand(newCtlDeleteCommand())
	cmd.AddCommand(newCtlOpCommand())
	return cmd
}

func newCtlApplyCommand() *cobra.Command {
	var (
		file        string
		noWait      bool
		waitTimeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "apply -f <manifest>",
		Short: "Submit a manifest to the controller",
		Long: `Submits the manifest to the controller as an async operation. By default
waits for the operation to reach a terminal status (succeeded/failed/
interrupted) before returning. Use --no-wait to fire-and-forget.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if file == "" {
				return fmt.Errorf("--file is required")
			}
			r, err := loadManifest(file)
			if err != nil {
				return err
			}
			if err := schema.Validate(r); err != nil {
				return fmt.Errorf("validation: %w", err)
			}
			return ctlApply(cmd.Context(), r, !noWait, waitTimeout)
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "manifest file (.yaml or .cue)")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return immediately after submission instead of polling for completion")
	cmd.Flags().DurationVar(&waitTimeout, "wait-timeout", 5*time.Minute, "max time to wait for completion when not --no-wait")
	return cmd
}

func newCtlGetCommand() *cobra.Command {
	var apiVersion string
	cmd := &cobra.Command{
		Use:   "get <kind> [name]",
		Short: "Get one or all resources of a kind from the controller",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := args[0]
			if apiVersion == "" {
				return fmt.Errorf("--api-version is required (e.g. proxmox.openctl.io/v1)")
			}
			if len(args) == 2 {
				return ctlGet(cmd.Context(), apiVersion, kind, args[1])
			}
			return ctlList(cmd.Context(), apiVersion, kind)
		},
	}
	cmd.Flags().StringVar(&apiVersion, "api-version", "", "resource apiVersion (e.g. proxmox.openctl.io/v1)")
	return cmd
}

func newCtlDeleteCommand() *cobra.Command {
	var (
		apiVersion  string
		noWait      bool
		waitTimeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "delete <kind> <name>",
		Short: "Delete a resource via the controller (idempotent)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if apiVersion == "" {
				return fmt.Errorf("--api-version is required (e.g. proxmox.openctl.io/v1)")
			}
			return ctlDelete(cmd.Context(), apiVersion, args[0], args[1], !noWait, waitTimeout)
		},
	}
	cmd.Flags().StringVar(&apiVersion, "api-version", "", "resource apiVersion (e.g. proxmox.openctl.io/v1)")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return immediately after submission instead of polling for completion")
	cmd.Flags().DurationVar(&waitTimeout, "wait-timeout", 5*time.Minute, "max time to wait for completion when not --no-wait")
	return cmd
}

func newCtlOpCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "op",
		Short: "Inspect operations",
	}
	cmd.AddCommand(newCtlOpGetCommand())
	cmd.AddCommand(newCtlOpListCommand())
	return cmd
}

func newCtlOpGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get <op-id>",
		Short: "Get an operation by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, token, err := dialController()
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()
			ctx := metadata.AppendToOutgoingContext(cmd.Context(), "authorization", "Bearer "+token)
			op, err := apiv1.NewOperationServiceClient(conn).GetOperation(ctx, &apiv1.GetOperationRequest{Id: args[0]})
			if err != nil {
				return err
			}
			return printOp(op)
		},
	}
}

func newCtlOpListCommand() *cobra.Command {
	var (
		statusFlag string
		apiVersion string
		kind       string
		name       string
		limit      int32
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List operations (newest first)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			conn, token, err := dialController()
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()
			ctx := metadata.AppendToOutgoingContext(cmd.Context(), "authorization", "Bearer "+token)
			resp, err := apiv1.NewOperationServiceClient(conn).ListOperations(ctx, &apiv1.ListOperationsRequest{
				Status:       statusFlag,
				ApiVersion:   apiVersion,
				Kind:         kind,
				ResourceName: name,
				Limit:        limit,
			})
			if err != nil {
				return err
			}
			for _, op := range resp.GetOperations() {
				if err := printOp(op); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&statusFlag, "status", "", "filter by status (pending|running|succeeded|failed|interrupted)")
	cmd.Flags().StringVar(&apiVersion, "api-version", "", "filter by apiVersion")
	cmd.Flags().StringVar(&kind, "kind", "", "filter by kind")
	cmd.Flags().StringVar(&name, "name", "", "filter by resource name")
	cmd.Flags().Int32Var(&limit, "limit", 0, "max results returned (0 = no cap)")
	return cmd
}

func printOp(op *apiv1.Operation) error {
	js, err := json.MarshalIndent(op, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(js))
	return nil
}

func ctlApply(ctx context.Context, r *protocol.Resource, wait bool, waitTimeout time.Duration) error {
	conn, token, err := dialController()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	pr, err := resourceToProto(r)
	if err != nil {
		return err
	}
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	resp, err := apiv1.NewResourceServiceClient(conn).Apply(ctx, &apiv1.ApplyRequest{Resource: pr})
	if err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	fmt.Fprintln(os.Stderr, resp.GetMessage())
	if !wait {
		fmt.Println(resp.GetOperationId())
		return nil
	}
	return waitForOp(ctx, conn, resp.GetOperationId(), waitTimeout)
}

func ctlGet(ctx context.Context, apiVersion, kind, name string) error {
	conn, token, err := dialController()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	resp, err := apiv1.NewResourceServiceClient(conn).Get(ctx, &apiv1.GetRequest{
		ApiVersion: apiVersion,
		Kind:       kind,
		Name:       name,
	})
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}
	return printResource(resp.GetResource())
}

func ctlList(ctx context.Context, apiVersion, kind string) error {
	conn, token, err := dialController()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	resp, err := apiv1.NewResourceServiceClient(conn).List(ctx, &apiv1.ListRequest{
		ApiVersion: apiVersion,
		Kind:       kind,
	})
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}
	for _, r := range resp.GetResources() {
		if err := printResource(r); err != nil {
			return err
		}
	}
	return nil
}

func ctlDelete(ctx context.Context, apiVersion, kind, name string, wait bool, waitTimeout time.Duration) error {
	conn, token, err := dialController()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	resp, err := apiv1.NewResourceServiceClient(conn).Delete(ctx, &apiv1.DeleteRequest{
		ApiVersion: apiVersion,
		Kind:       kind,
		Name:       name,
	})
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	fmt.Fprintln(os.Stderr, resp.GetMessage())
	if !wait {
		fmt.Println(resp.GetOperationId())
		return nil
	}
	return waitForOp(ctx, conn, resp.GetOperationId(), waitTimeout)
}

// waitForOp polls the controller for op completion. Prints the final op
// state on stdout. Errors out if the timeout fires or the op terminates
// with a non-success status.
func waitForOp(ctx context.Context, conn *grpc.ClientConn, opID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := apiv1.NewOperationServiceClient(conn)
	for {
		op, err := client.GetOperation(ctx, &apiv1.GetOperationRequest{Id: opID})
		if err != nil {
			return fmt.Errorf("get op %s: %w", opID, err)
		}
		switch op.GetStatus() {
		case "succeeded":
			fmt.Fprintf(os.Stderr, "operation %s succeeded\n", opID)
			if op.GetResult() != nil {
				return printResource(op.GetResult())
			}
			return nil
		case "failed":
			return fmt.Errorf("operation %s failed: %s", opID, op.GetError())
		case "interrupted":
			return fmt.Errorf("operation %s interrupted: %s", opID, op.GetError())
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("operation %s did not finish within %s (still %s — poll with `openctl ctl op get %s`)",
				opID, timeout, op.GetStatus(), opID)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// resourceToProto mirrors the controller-side conversion so the wire shape
// stays consistent. Kept private here (not in the api package) because the
// controller's version handles unsupported number types from YAML decoders;
// the CLI's input has the same problem and applies the same fix.
func resourceToProto(r *protocol.Resource) (*apiv1.Resource, error) {
	out := &apiv1.Resource{
		ApiVersion: r.APIVersion,
		Kind:       r.Kind,
		Metadata: &apiv1.Metadata{
			Name:        r.Metadata.Name,
			Labels:      r.Metadata.Labels,
			Annotations: r.Metadata.Annotations,
		},
	}
	if r.Spec != nil {
		s, err := structpb.NewStruct(normalizeNumeric(r.Spec))
		if err != nil {
			return nil, fmt.Errorf("spec: %w", err)
		}
		out.Spec = s
	}
	if r.Status != nil {
		s, err := structpb.NewStruct(normalizeNumeric(r.Status))
		if err != nil {
			return nil, fmt.Errorf("status: %w", err)
		}
		out.Status = s
	}
	return out, nil
}

func normalizeNumeric(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = normalizeNumericValue(v)
	}
	return out
}

func normalizeNumericValue(v any) any {
	switch val := v.(type) {
	case int:
		return float64(val)
	case int32:
		return float64(val)
	case int64:
		return float64(val)
	case uint:
		return float64(val)
	case uint32:
		return float64(val)
	case uint64:
		return float64(val)
	case map[string]any:
		return normalizeNumeric(val)
	case []any:
		out := make([]any, len(val))
		for i, x := range val {
			out[i] = normalizeNumericValue(x)
		}
		return out
	default:
		return v
	}
}

// printResource emits the response in the user's chosen output format. For
// Phase 2 we just dump JSON since the table formatter is provider-shaped
// and the controller path is generic.
func printResource(r *apiv1.Resource) error {
	if r == nil {
		return nil
	}
	js, err := json.MarshalIndent(struct {
		APIVersion string          `json:"apiVersion"`
		Kind       string          `json:"kind"`
		Metadata   *apiv1.Metadata `json:"metadata"`
		Spec       any             `json:"spec,omitempty"`
		Status     any             `json:"status,omitempty"`
	}{
		APIVersion: r.GetApiVersion(),
		Kind:       r.GetKind(),
		Metadata:   r.GetMetadata(),
		Spec:       r.GetSpec().AsMap(),
		Status:     r.GetStatus().AsMap(),
	}, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(js))
	return nil
}
