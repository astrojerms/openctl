package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

func newCtlHealthCommand() *cobra.Command {
	var apiVersion string
	cmd := &cobra.Command{
		Use:   "health <kind> <name> --api-version <version>",
		Short: "Check live readiness of one resource; exit nonzero unless ready",
		Long:  "Read one named resource through the controller and report only its readiness, message and independent checks. This never applies, invokes actions, reads saved intent as a fallback, polls or retries. Only explicit boolean readiness with no failed or unknown check is successful; running/phase labels alone are not proof. JSON/YAML retain health diagnostics on non-ready outcomes and exclude specs, annotations and unrelated status fields. Provider health messages may still contain sensitive data. This is not playback, access-policy or future-readiness proof.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(apiVersion) == "" {
				return fmt.Errorf("--api-version is required")
			}
			if strings.TrimSpace(args[0]) == "" || strings.TrimSpace(args[1]) == "" {
				return fmt.Errorf("kind and name are required")
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
			defer conn.Close()
			ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
			response, err := apiv1.NewResourceServiceClient(conn).Get(ctx, &apiv1.GetRequest{ApiVersion: apiVersion, Kind: args[0], Name: args[1]})
			if err != nil {
				return fmt.Errorf("controller health read failed (%s); no readiness established", status.Code(err))
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			resource := response.GetResource()
			if resource == nil || resource.GetApiVersion() != apiVersion || resource.GetKind() != args[0] || resource.GetMetadata().GetName() != args[1] {
				return fmt.Errorf("controller returned an absent or different resource; no readiness established")
			}
			health, state := ctlHealthProjection(resource)
			if handled, err := printControllerStructured(cmd.OutOrStdout(), health); handled {
				if err != nil {
					return err
				}
			} else if err := printCtlHealth(cmd.OutOrStdout(), health); err != nil {
				return err
			}
			if state != "ready" {
				return fmt.Errorf("resource readiness is %s", state)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&apiVersion, "api-version", "", "required resource apiVersion (no provider default)")
	return cmd
}

// Project known health fields without copying specs or arbitrary provider status.
// A provider's aggregate cannot override failed, malformed or future evidence.
func ctlHealthProjection(resource *apiv1.Resource) (*apiv1.Resource, string) {
	fields := resource.GetStatus().GetFields()
	state := "unknown"
	if ready, ok := fields["ready"].GetKind().(*structpb.Value_BoolValue); ok {
		if ready.BoolValue {
			state = "ready"
		} else {
			state = "not_ready"
		}
	}
	out := map[string]*structpb.Value{}
	if _, ok := fields["message"].GetKind().(*structpb.Value_StringValue); ok {
		out["message"] = fields["message"]
	}
	if raw, exists := fields["checks"]; exists {
		if checks, ok := raw.GetKind().(*structpb.Value_StructValue); ok && checks.StructValue != nil {
			projected := make(map[string]*structpb.Value, len(checks.StructValue.Fields))
			for name, value := range checks.StructValue.Fields {
				check := value.GetStructValue().GetFields()
				evidence := check["state"].GetStringValue()
				switch evidence {
				case "not_ready":
					state = "not_ready"
				case "ready", "not_configured":
				default:
					evidence = "unknown"
					if state == "ready" {
						state = "unknown"
					}
				}
				item := map[string]*structpb.Value{"state": structpb.NewStringValue(evidence)}
				if _, ok := check["message"].GetKind().(*structpb.Value_StringValue); ok {
					item["message"] = check["message"]
				}
				projected[name] = structpb.NewStructValue(&structpb.Struct{Fields: item})
			}
			out["checks"] = structpb.NewStructValue(&structpb.Struct{Fields: projected})
		} else if state == "ready" {
			state = "unknown"
		}
	}
	out["state"] = structpb.NewStringValue(state)
	out["ready"] = structpb.NewBoolValue(state == "ready")
	return &apiv1.Resource{ApiVersion: resource.ApiVersion, Kind: resource.Kind, Metadata: &apiv1.Metadata{Name: resource.GetMetadata().GetName()}, Status: &structpb.Struct{Fields: out}}, state
}

func printCtlHealth(out io.Writer, health *apiv1.Resource) error {
	fields := health.GetStatus().GetFields()
	if _, err := fmt.Fprintf(out, "Resource: %s %s/%s\nReadiness: %s\n", health.GetApiVersion(), health.GetKind(), health.GetMetadata().GetName(), fields["state"].GetStringValue()); err != nil {
		return err
	}
	if message := fields["message"].GetStringValue(); message != "" {
		if _, err := fmt.Fprintln(out, message); err != nil {
			return err
		}
	}
	checks := fields["checks"].GetStructValue().GetFields()
	if len(checks) > 0 {
		names := make([]string, 0, len(checks))
		for name := range checks {
			names = append(names, name)
		}
		sort.Strings(names)
		table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(table, "CHECK\tSTATE\tDETAILS"); err != nil {
			return err
		}
		for _, name := range names {
			check := checks[name].GetStructValue().GetFields()
			if _, err := fmt.Fprintf(table, "%s\t%s\t%s\n", name, check["state"].GetStringValue(), check["message"].GetStringValue()); err != nil {
				return err
			}
		}
		if err := table.Flush(); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(out, "Live read-only observation; no changes submitted. Not playback or access-policy proof.")
	return err
}
