package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

func newCtlActionCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "action", Short: "Inspect and invoke controller runtime actions",
		Long: "Inspect and invoke controller runtime actions, not durable Apply operations. Actions are never automatically retried. Results and links may contain sensitive information; URLs are not opened. Downloads, including raw stdout downloads, can contain credentials.",
	}
	cmd.AddCommand(newCtlActionListCommand(), newCtlActionRunCommand())
	return cmd
}

func ctlActionContext(cmd *cobra.Command) (context.Context, context.CancelFunc, error) {
	timeout := globalTimeout
	if timeout < 0 {
		return nil, nil, fmt.Errorf("timeout must not be negative")
	}
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	return ctx, cancel, nil
}

func newCtlActionListCommand() *cobra.Command {
	var apiVersion string
	cmd := &cobra.Command{
		Use: "list <kind> --api-version <version>", Short: "List supported actions and parameter schemas", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(apiVersion) == "" {
				return fmt.Errorf("--api-version is required")
			}
			ctx, cancel, err := ctlActionContext(cmd)
			if err != nil {
				return err
			}
			defer cancel()
			conn, token, err := dialController()
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()
			ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
			response, err := apiv1.NewResourceServiceClient(conn).ListActions(ctx, &apiv1.ListActionsRequest{ApiVersion: apiVersion, Kind: args[0]})
			if err != nil {
				return fmt.Errorf("controller action list failed (%s)", status.Code(err))
			}
			if handled, err := printControllerStructured(cmd.OutOrStdout(), response); handled {
				return err
			}
			out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(out, "ACTION\tDESCRIPTION\tPARAMETER\tTYPE\tREQUIRED\tDEFAULT\tPARAMETER DESCRIPTION")
			seen := make(map[string]bool, len(response.GetActionSpecs()))
			for _, spec := range response.GetActionSpecs() {
				seen[spec.GetName()] = true
				if len(spec.GetParameters()) == 0 {
					fmt.Fprintf(out, "%s\t%s\t\t\t\t\t\n", spec.GetName(), spec.GetDescription())
				}
				for _, parameter := range spec.GetParameters() {
					fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%t\t%s\t%s\n", spec.GetName(), spec.GetDescription(), parameter.GetName(), parameter.GetType(), parameter.GetRequired(), parameter.GetDefaultValue(), parameter.GetDescription())
				}
			}
			for _, name := range response.GetActions() {
				if !seen[name] {
					fmt.Fprintf(out, "%s\t\t\t\t\t\t\n", name)
				}
			}
			return out.Flush()
		},
	}
	cmd.Flags().StringVar(&apiVersion, "api-version", "", "required resource apiVersion (no provider default)")
	return cmd
}

func newCtlActionRunCommand() *cobra.Command {
	var apiVersion, paramsFile, download string
	var params []string
	cmd := &cobra.Command{
		Use: "run <kind> <name> <action> --api-version <version>", Short: "Invoke a runtime action once",
		Long: "Invoke a runtime action once, without automatic retries or a durable Apply operation. If the outcome is uncertain, inspect the resource before repeating. Use --params-file for secrets; --param values may be visible in process arguments. Results and URLs may be sensitive. --download - writes raw content (possibly credentials) to stdout, without other output. Download filenames from the server are never used as paths.",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(apiVersion) == "" {
				return fmt.Errorf("--api-version is required")
			}
			if cmd.Flags().Changed("params-file") && paramsFile == "" {
				return fmt.Errorf("--params-file requires a path or -")
			}
			if cmd.Flags().Changed("download") && download == "" {
				return fmt.Errorf("--download requires a path or -")
			}
			ctx, cancel, err := ctlActionContext(cmd)
			if err != nil {
				return err
			}
			defer cancel()
			parameters, err := readCtlActionParameters(cmd.InOrStdin(), paramsFile, params)
			if err != nil {
				return err
			}
			completed := false
			var response *apiv1.InvokeActionResponse
			invoke := func() error {
				conn, token, err := dialController()
				if err != nil {
					return err
				}
				defer func() { _ = conn.Close() }()
				authCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
				response, err = apiv1.NewResourceServiceClient(conn).InvokeAction(authCtx, &apiv1.InvokeActionRequest{ApiVersion: apiVersion, Kind: args[0], ResourceName: args[1], Action: args[2], Parameters: parameters})
				if err != nil {
					// Server errors can contain parameter values. Report only the status.
					return fmt.Errorf("controller action failed (%s); outcome may be uncertain; inspect the resource before repeating; no automatic retry was attempted", status.Code(err))
				}
				completed = true
				return nil
			}
			writeDownload := func(out io.Writer) error {
				if err := invoke(); err != nil {
					return err
				}
				if response.GetDownloadContent() == "" {
					return fmt.Errorf("no download content was returned")
				}
				_, err := io.WriteString(out, response.GetDownloadContent())
				return err
			}
			render := func() error {
				// Never pass the payload to a structured printer, even after saving it.
				response.DownloadContent = ""
				if handled, err := printControllerStructured(cmd.OutOrStdout(), response); handled {
					return err
				}
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Action request returned successfully; inspect the resource for task completion and readiness."); err != nil {
					return err
				}
				if response.GetMessage() != "" {
					if _, err := fmt.Fprintln(cmd.OutOrStdout(), response.GetMessage()); err != nil {
						return err
					}
				}
				if response.GetUrl() != "" {
					if _, err := fmt.Fprintln(cmd.OutOrStdout(), response.GetUrl()); err != nil {
						return err
					}
				}
				return nil
			}
			switch download {
			case "-":
				err = writeDownload(cmd.OutOrStdout())
			case "":
				err = invoke()
				if err == nil {
					if response.GetDownloadContent() != "" {
						err = fmt.Errorf("returned content was not saved; select --download before invoking an action that returns content")
					} else {
						err = render()
					}
				}
			default:
				err = writePrivateExport(download, writeDownload, render)
			}
			if err != nil && completed {
				return fmt.Errorf("action request already completed, but output handling failed: %w; inspect the resource and task status before repeating (do not assume rerunning is safe)", err)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&apiVersion, "api-version", "", "required resource apiVersion (no provider default)")
	cmd.Flags().StringArrayVar(&params, "param", nil, "non-sensitive key=value parameter (repeatable; no duplicate keys)")
	cmd.Flags().StringVar(&paramsFile, "params-file", "", "strict JSON object of string parameters; - reads stdin (use for secrets)")
	cmd.Flags().StringVar(&download, "download", "", "new private file for returned content; - streams raw stdout (may contain credentials)")
	return cmd
}

func readCtlActionParameters(stdin io.Reader, path string, options []string) (map[string]string, error) {
	parameters := make(map[string]string)
	if path != "" {
		input := stdin
		if path != "-" {
			file, err := os.Open(path)
			if err != nil {
				return nil, fmt.Errorf("cannot open action parameters file")
			}
			defer func() { _ = file.Close() }()
			input = file
		}
		decoder := json.NewDecoder(input)
		start, err := decoder.Token()
		if err != nil || start != json.Delim('{') {
			return nil, fmt.Errorf("parameters file must contain one JSON object of string values")
		}
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return nil, fmt.Errorf("invalid parameters JSON object")
			}
			key, ok := token.(string)
			if !ok || key == "" {
				return nil, fmt.Errorf("parameter keys must be nonempty strings")
			}
			if _, exists := parameters[key]; exists {
				return nil, fmt.Errorf("duplicate parameter keys are not allowed")
			}
			token, err = decoder.Token()
			value, ok := token.(string)
			if err != nil || !ok {
				return nil, fmt.Errorf("parameters file values must be JSON strings")
			}
			parameters[key] = value
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
			return nil, fmt.Errorf("invalid parameters JSON object")
		}
		if _, err := decoder.Token(); err != io.EOF {
			return nil, fmt.Errorf("parameters file must contain exactly one JSON object")
		}
	}
	for _, option := range options {
		key, value, ok := strings.Cut(option, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("--param requires a nonempty key followed by =value")
		}
		if _, exists := parameters[key]; exists {
			return nil, fmt.Errorf("duplicate parameter keys across --param and --params-file are not allowed")
		}
		parameters[key] = value
	}
	return parameters, nil
}
