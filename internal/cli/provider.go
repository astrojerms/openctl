package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/openctl/openctl/internal/log"
	"github.com/openctl/openctl/internal/plugin"
	"github.com/openctl/openctl/pkg/protocol"
)

func newProviderCommand(p *plugin.Plugin) *cobra.Command {
	cmd := &cobra.Command{
		Use:   p.Name,
		Short: fmt.Sprintf("Manage %s resources", p.Name),
		Long:  fmt.Sprintf("Commands for managing resources in the %s provider", p.Name),
	}

	executor := plugin.NewExecutor(p, 0)
	caps, err := executor.GetCapabilities(context.Background())
	if err != nil {
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("failed to get plugin capabilities: %w", err)
		}
		return cmd
	}

	cmd.AddCommand(newGetCommand(p, caps))
	cmd.AddCommand(newCreateCommand(p, caps))
	cmd.AddCommand(newDeleteCommand(p, caps))
	cmd.AddCommand(newApplyProviderCommand(p, caps))
	for _, subcmd := range caps.Subcommands {
		cmd.AddCommand(newPluginSubcommand(p, subcmd))
	}

	return cmd
}

func newGetCommand(p *plugin.Plugin, caps *protocol.Capabilities) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <resource-type> [name]",
		Short: "Get one or more resources",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resourceType := args[0]
			var resourceName string
			if len(args) > 1 {
				resourceName = args[1]
			}

			kind := resolveResourceKind(resourceType, caps)
			if kind == "" {
				return fmt.Errorf("unknown resource type: %s", resourceType)
			}

			log.Verbose("Provider: %s", p.Name)
			log.Verbose("Resource type: %s (kind: %s)", resourceType, kind)

			providerConfig, err := globalConfig.GetProviderConfig(p.Name, contextName)
			if err != nil {
				return err
			}

			log.Verbose("Endpoint: %s", providerConfig.Endpoint)
			log.Verbose("Node: %s", providerConfig.Node)
			log.Verbose("Token ID: %s", providerConfig.TokenID)
			if providerConfig.TokenSecret != "" {
				log.Debug("Token secret: [%d chars]", len(providerConfig.TokenSecret))
			} else {
				log.Verbose("Token secret: (not set)")
			}

			action := protocol.ActionList
			if resourceName != "" {
				action = protocol.ActionGet
			}

			req := &protocol.Request{
				Version:      protocol.ProtocolVersion,
				Action:       action,
				ResourceType: kind,
				ResourceName: resourceName,
				Config:       *providerConfig,
			}

			executor := plugin.NewExecutor(p, globalTimeout)
			resp, err := executor.Execute(getContext(), req)
			if err != nil {
				return err
			}

			if resp.Status == protocol.StatusError && resp.Error != nil {
				return fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
			}

			formatter := getFormatter()
			if resp.Resource != nil {
				return formatter.FormatResource(resp.Resource)
			}
			return formatter.FormatResources(resp.Resources)
		},
	}

	return cmd
}

func newCreateCommand(p *plugin.Plugin, caps *protocol.Capabilities) *cobra.Command {
	var manifestFile string

	cmd := &cobra.Command{
		Use:   "create <resource-type> [flags]",
		Short: "Create a resource",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resourceType := args[0]

			kind := resolveResourceKind(resourceType, caps)
			if kind == "" {
				return fmt.Errorf("unknown resource type: %s", resourceType)
			}

			if manifestFile == "" {
				return fmt.Errorf("manifest file required (-f flag)")
			}

			manifest, err := loadManifest(manifestFile)
			if err != nil {
				return err
			}

			if manifest.Kind != kind {
				return fmt.Errorf("manifest kind %q does not match resource type %q", manifest.Kind, kind)
			}

			providerConfig, err := globalConfig.GetProviderConfig(p.Name, contextName)
			if err != nil {
				return err
			}

			req := &protocol.Request{
				Version:      protocol.ProtocolVersion,
				Action:       protocol.ActionCreate,
				ResourceType: kind,
				Manifest:     manifest,
				Config:       *providerConfig,
			}

			// Use dispatcher for plugins that support dispatch
			if caps.SupportsDispatch {
				dispatcher, dispatcherErr := plugin.NewDispatcher(globalConfig, globalTimeout)
				if dispatcherErr != nil {
					return dispatcherErr
				}
				resp, execErr := dispatcher.ExecuteWithDispatch(getContext(), p.Name, req)
				if execErr != nil {
					return execErr
				}
				if resp.Status == protocol.StatusError && resp.Error != nil {
					return fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
				}
				fmt.Println(resp.Message)
				return nil
			}

			executor := plugin.NewExecutor(p, globalTimeout)
			resp, execErr := executor.Execute(getContext(), req)
			if execErr != nil {
				return execErr
			}

			if resp.Status == protocol.StatusError && resp.Error != nil {
				return fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
			}

			fmt.Println(resp.Message)
			return nil
		},
	}

	cmd.Flags().StringVarP(&manifestFile, "file", "f", "", "manifest file")

	return cmd
}

func newDeleteCommand(p *plugin.Plugin, caps *protocol.Capabilities) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <resource-type> <name>",
		Short: "Delete a resource",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resourceType := args[0]
			resourceName := args[1]

			kind := resolveResourceKind(resourceType, caps)
			if kind == "" {
				return fmt.Errorf("unknown resource type: %s", resourceType)
			}

			providerConfig, err := globalConfig.GetProviderConfig(p.Name, contextName)
			if err != nil {
				return err
			}

			req := &protocol.Request{
				Version:      protocol.ProtocolVersion,
				Action:       protocol.ActionDelete,
				ResourceType: kind,
				ResourceName: resourceName,
				Config:       *providerConfig,
			}

			// Use dispatcher for plugins that support dispatch
			if caps.SupportsDispatch {
				dispatcher, dispatcherErr := plugin.NewDispatcher(globalConfig, globalTimeout)
				if dispatcherErr != nil {
					return dispatcherErr
				}
				resp, execErr := dispatcher.ExecuteWithDispatch(getContext(), p.Name, req)
				if execErr != nil {
					return execErr
				}
				if resp.Status == protocol.StatusError && resp.Error != nil {
					return fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
				}
				fmt.Println(resp.Message)
				return nil
			}

			executor := plugin.NewExecutor(p, globalTimeout)
			resp, execErr := executor.Execute(getContext(), req)
			if execErr != nil {
				return execErr
			}

			if resp.Status == protocol.StatusError && resp.Error != nil {
				return fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
			}

			fmt.Println(resp.Message)
			return nil
		},
	}

	return cmd
}

func newApplyProviderCommand(p *plugin.Plugin, caps *protocol.Capabilities) *cobra.Command {
	var manifestFile string

	cmd := &cobra.Command{
		Use:   "apply [flags]",
		Short: "Apply a manifest (create or update)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if manifestFile == "" {
				return fmt.Errorf("manifest file required (-f flag)")
			}

			manifest, err := loadManifest(manifestFile)
			if err != nil {
				return err
			}

			kind := manifest.Kind
			if resolveResourceKind(kind, caps) == "" {
				return fmt.Errorf("unknown resource kind: %s", kind)
			}

			providerConfig, err := globalConfig.GetProviderConfig(p.Name, contextName)
			if err != nil {
				return err
			}

			req := &protocol.Request{
				Version:      protocol.ProtocolVersion,
				Action:       protocol.ActionApply,
				ResourceType: kind,
				Manifest:     manifest,
				Config:       *providerConfig,
			}

			executor := plugin.NewExecutor(p, globalTimeout)
			resp, err := executor.Execute(getContext(), req)
			if err != nil {
				return err
			}

			if resp.Status == protocol.StatusError && resp.Error != nil {
				return fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
			}

			fmt.Println(resp.Message)
			return nil
		},
	}

	cmd.Flags().StringVarP(&manifestFile, "file", "f", "", "manifest file")

	return cmd
}

func resolveResourceKind(resourceType string, caps *protocol.Capabilities) string {
	for _, r := range caps.Resources {
		if r.Kind == resourceType || r.Plural == resourceType {
			return r.Kind
		}
	}
	return ""
}

func newPluginSubcommand(p *plugin.Plugin, def protocol.SubcommandDefinition) *cobra.Command {
	use := def.Name
	if len(def.PositionalArgs) > 0 {
		parts := []string{def.Name}
		for _, arg := range def.PositionalArgs {
			if arg.Required {
				parts = append(parts, "<"+arg.Name+">")
			} else {
				parts = append(parts, "["+arg.Name+"]")
			}
		}
		use = strings.Join(parts, " ")
	}

	cmd := &cobra.Command{
		Use:   use,
		Short: firstNonEmpty(def.Short, "Provider-defined command"),
		Long:  def.Long,
		Args:  positionalArgValidator(def.PositionalArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			if def.Action == "" {
				return fmt.Errorf("plugin subcommand %q has no action", def.Name)
			}
			providerConfig, err := globalConfig.GetProviderConfig(p.Name, contextName)
			if err != nil {
				return err
			}
			reqArgs, err := collectSubcommandArgs(cmd, def, args)
			if err != nil {
				return err
			}
			req := &protocol.Request{
				Version: protocol.ProtocolVersion,
				Action:  def.Action,
				Args:    reqArgs,
				Config:  *providerConfig,
			}
			executor := plugin.NewExecutor(p, globalTimeout)
			resp, err := executor.Execute(getContext(), req)
			if err != nil {
				return err
			}
			if resp.Status == protocol.StatusError && resp.Error != nil {
				return fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
			}
			return formatPluginSubcommandResponse(resp)
		},
	}

	for _, flag := range def.Flags {
		addSubcommandFlag(cmd, flag)
	}
	return cmd
}

func addSubcommandFlag(cmd *cobra.Command, flag protocol.FlagSpec) {
	if flag.Name == "" {
		return
	}
	help := flag.Help
	switch flag.Type {
	case "string", "":
		cmd.Flags().StringP(flag.Name, flag.Short, flag.Default, help)
	case "int":
		def := 0
		if flag.Default != "" {
			if n, err := strconv.Atoi(flag.Default); err == nil {
				def = n
			}
		}
		cmd.Flags().IntP(flag.Name, flag.Short, def, help)
	case "bool":
		def := false
		if flag.Default != "" {
			if b, err := strconv.ParseBool(flag.Default); err == nil {
				def = b
			}
		}
		cmd.Flags().BoolP(flag.Name, flag.Short, def, help)
	default:
		cmd.Flags().StringP(flag.Name, flag.Short, flag.Default, help)
	}
	if flag.Required {
		_ = cmd.MarkFlagRequired(flag.Name)
	}
}

func positionalArgValidator(args []protocol.ArgSpec) cobra.PositionalArgs {
	required := 0
	for _, arg := range args {
		if arg.Required {
			required++
		}
	}
	return cobra.RangeArgs(required, len(args))
}

func collectSubcommandArgs(cmd *cobra.Command, def protocol.SubcommandDefinition, positionals []string) (map[string]any, error) {
	out := map[string]any{}
	for i, spec := range def.PositionalArgs {
		if spec.Name == "" || i >= len(positionals) {
			continue
		}
		out[spec.Name] = positionals[i]
	}
	for _, spec := range def.Flags {
		if spec.Name == "" {
			continue
		}
		flag := cmd.Flags().Lookup(spec.Name)
		if flag == nil {
			continue
		}
		switch spec.Type {
		case "int":
			v, err := cmd.Flags().GetInt(spec.Name)
			if err != nil {
				return nil, err
			}
			out[spec.Name] = v
		case "bool":
			v, err := cmd.Flags().GetBool(spec.Name)
			if err != nil {
				return nil, err
			}
			out[spec.Name] = v
		default:
			v, err := cmd.Flags().GetString(spec.Name)
			if err != nil {
				return nil, err
			}
			out[spec.Name] = v
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func formatPluginSubcommandResponse(resp *protocol.Response) error {
	formatter := getFormatter()
	if resp.Resource != nil {
		return formatter.FormatResource(resp.Resource)
	}
	if len(resp.Resources) > 0 {
		return formatter.FormatResources(resp.Resources)
	}
	if resp.Message != "" {
		fmt.Println(resp.Message)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
