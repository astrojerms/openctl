package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/openctl/openctl/internal/config"
	"github.com/openctl/openctl/internal/manifest"
	"github.com/openctl/openctl/internal/plugin"
	"github.com/openctl/openctl/pkg/protocol"
)

func newApplyCommand() *cobra.Command {
	var manifestFile string

	cmd := &cobra.Command{
		Use:   "apply -f <manifest>",
		Short: "Apply a manifest (auto-detects provider)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if manifestFile == "" {
				return fmt.Errorf("manifest file required (-f flag)")
			}

			m, err := loadManifest(manifestFile)
			if err != nil {
				return err
			}

			providerName := manifest.ExtractProvider(m.APIVersion)
			if providerName == "" {
				return fmt.Errorf("cannot determine provider from apiVersion: %s", m.APIVersion)
			}

			p, err := plugin.FindPlugin(providerName)
			if err != nil {
				return err
			}
			if p == nil {
				return fmt.Errorf("plugin %q not found for apiVersion %s", providerName, m.APIVersion)
			}

			providerConfig, err := globalConfig.GetProviderConfig(providerName, contextName)
			if err != nil {
				return err
			}

			req := &protocol.Request{
				Version:      protocol.ProtocolVersion,
				Action:       protocol.ActionApply,
				ResourceType: m.Kind,
				Manifest:     m,
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

func newPluginCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "Manage plugins",
	}

	cmd.AddCommand(newPluginListCommand())

	return cmd
}

func newPluginListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List installed plugins",
		RunE: func(cmd *cobra.Command, args []string) error {
			plugins, err := plugin.Discover()
			if err != nil {
				return err
			}

			if len(plugins) == 0 {
				fmt.Println("No plugins installed")
				return nil
			}

			fmt.Printf("%-20s %s\n", "NAME", "PATH")
			for _, p := range plugins {
				executor := plugin.NewExecutor(p, 5*globalTimeout)
				caps, err := executor.GetCapabilities(context.Background())
				if err != nil {
					fmt.Printf("%-20s %s (error: %v)\n", p.Name, p.Path, err)
					continue
				}

				resourceTypes := make([]string, len(caps.Resources))
				for i, r := range caps.Resources {
					resourceTypes[i] = r.Plural
				}

				fmt.Printf("%-20s %s\n", p.Name, p.Path)
				if len(resourceTypes) > 0 {
					fmt.Printf("  Resources: %v\n", resourceTypes)
				}
			}

			return nil
		},
	}
}

func newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage configuration",
	}

	cmd.AddCommand(newConfigViewCommand())

	return cmd
}

func newConfigViewCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "view",
		Short: "View current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := config.GetPaths()
			if err != nil {
				return err
			}

			data, err := os.ReadFile(paths.ConfigFile)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Println("No configuration file found")
					fmt.Printf("Create one at: %s\n", paths.ConfigFile)
					return nil
				}
				return err
			}

			fmt.Print(string(data))
			return nil
		},
	}
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("openctl version 0.1.0")
		},
	}
}

func loadManifest(path string) (*protocol.Resource, error) {
	return manifest.Load(path)
}
