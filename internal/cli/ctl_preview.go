package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/metadata"
)

func newCtlPreviewCommand() *cobra.Command {
	var file string
	var values []string
	cmd := &cobra.Command{
		Use:   "preview -f <manifest>",
		Short: "Preview one resource through the controller without applying it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if file == "" {
				return fmt.Errorf("--file is required")
			}
			timeout := globalTimeout
			if timeout < 0 {
				return fmt.Errorf("timeout must not be negative")
			}
			if timeout == 0 {
				timeout = 5 * time.Minute
			}
			r, err := loadManifestWithValues(file, values)
			if err != nil {
				return err
			}
			resource, err := resourceToProto(r)
			if err != nil {
				return err
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
			defer conn.Close()
			ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
			response, err := apiv1.NewResourceServiceClient(conn).DryRunApply(ctx, &apiv1.DryRunApplyRequest{Resource: resource})
			if err != nil {
				return fmt.Errorf("preview: %w", err)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			invalid := len(response.GetValidationErrors()) > 0 || len(response.GetFieldErrors()) > 0
			handled, err := printControllerStructured(cmd.OutOrStdout(), response)
			if err != nil {
				return err
			}
			if !handled {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Resource: %s %s/%s\n", resource.GetApiVersion(), resource.GetKind(), resource.GetMetadata().GetName()); err != nil {
					return err
				}
				if err := printCtlPreview(cmd.OutOrStdout(), response); err != nil {
					return err
				}
			}
			if invalid {
				return fmt.Errorf("preview failed controller validation")
			}
			switch response.GetOperation() {
			case "create", "update", "no-op":
				return nil
			default:
				return fmt.Errorf("controller returned missing or unknown preview operation %q; upgrade the controller", response.GetOperation())
			}
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "manifest file (.yaml or .cue)")
	cmd.Flags().StringArrayVar(&values, "values", nil, "CUE values file(s) to unify into a .cue manifest (repeatable)")
	return cmd
}

func printCtlPreview(out io.Writer, response *apiv1.DryRunApplyResponse) error {
	operation := response.GetOperation()
	if operation != "create" && operation != "update" && operation != "no-op" {
		operation = "unknown"
	}
	if _, err := fmt.Fprintf(out, "Operation: %s\nSummary: %s\n", operation, response.GetSummary()); err != nil {
		return err
	}
	for _, section := range []struct {
		name    string
		live    bool
		entries []*apiv1.DriftEntry
	}{
		{"Desired spec changes", false, response.GetDiff()},
		{"Metadata changes", false, response.GetMetadataDiff()},
		{"Live drift (independent of stored intent)", true, response.GetLiveDrift()},
	} {
		if _, err := fmt.Fprintf(out, "%s:\n", section.name); err != nil {
			return err
		}
		if len(section.entries) == 0 {
			if _, err := fmt.Fprintln(out, "  (none)"); err != nil {
				return err
			}
		}
		for _, entry := range section.entries {
			before, after := entry.GetDesired(), entry.GetObserved()
			label := "previous"
			if section.live {
				before, after = entry.GetObserved(), entry.GetDesired()
				label = "observed"
			}
			if _, err := fmt.Fprintf(out, "  %s: %s %s -> desired %s\n", entry.GetPath(), label, before, after); err != nil {
				return err
			}
		}
	}
	for _, child := range response.GetChildren() {
		if _, err := fmt.Fprintf(out, "Child action: %s %s/%s %s\n", child.GetVerb(), child.GetKind(), child.GetName(), child.GetDetail()); err != nil {
			return err
		}
	}
	for _, gate := range response.GetRequiredGates() {
		if _, err := fmt.Fprintf(out, "Required gate (not approved): %s\n", gate); err != nil {
			return err
		}
	}
	for _, diagnostic := range response.GetValidationErrors() {
		if _, err := fmt.Fprintf(out, "Validation error: %s\n", diagnostic); err != nil {
			return err
		}
	}
	for _, diagnostic := range response.GetFieldErrors() {
		if _, err := fmt.Fprintf(out, "Validation error at %s: %s\n", diagnostic.GetPath(), diagnostic.GetMessage()); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(out, "Preview only: no changes applied; existence is not reserved and Apply is not authorized.")
	return err
}
