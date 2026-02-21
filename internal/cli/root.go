package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/openctl/openctl/internal/config"
	"github.com/openctl/openctl/internal/log"
	"github.com/openctl/openctl/internal/output"
	"github.com/openctl/openctl/internal/plugin"
)

var (
	cfgFile       string
	outputFormat  string
	contextName   string
	timeout       int
	verbose       bool
	debug         bool
	globalConfig  *config.Config
	globalTimeout time.Duration
)

// rootCmd represents the base command
var rootCmd = &cobra.Command{
	Use:   "openctl",
	Short: "A CLI tool for managing infrastructure resources",
	Long: `OpenCtl is a CLI tool that provides a unified interface for managing
infrastructure resources across different providers using a kubectl-like experience
with a Terraform-like plugin system.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Configure logging first
		if debug {
			log.SetDebug(true)
		} else if verbose {
			log.SetVerbose(true)
		}

		var err error
		var configPath string
		if cfgFile != "" {
			configPath = cfgFile
			globalConfig, err = config.LoadFromFile(cfgFile)
		} else {
			paths, _ := config.GetPaths()
			if paths != nil {
				configPath = paths.ConfigFile
			}
			globalConfig, err = config.Load()
		}
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		log.Debug("Config file: %s", configPath)
		log.Debug("Context: %s", contextName)

		if timeout == 0 {
			timeout = globalConfig.Defaults.Timeout
		}
		if timeout == 0 {
			timeout = 300
		}
		globalTimeout = time.Duration(timeout) * time.Second

		if outputFormat == "" {
			outputFormat = globalConfig.Defaults.Output
		}
		if outputFormat == "" {
			outputFormat = "table"
		}

		log.Debug("Output format: %s", outputFormat)
		log.Debug("Timeout: %ds", timeout)

		return nil
	},
}

// Execute runs the root command
func Execute() error {
	if err := rootCmd.Execute(); err != nil {
		output.PrintError(os.Stderr, err)
		return err
	}
	return nil
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is ~/.openctl/config.yaml)")
	rootCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "", "output format (table, yaml, json, wide)")
	rootCmd.PersistentFlags().StringVar(&contextName, "context", "", "context to use")
	rootCmd.PersistentFlags().IntVar(&timeout, "timeout", 0, "timeout in seconds (default 300)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable verbose output")
	rootCmd.PersistentFlags().BoolVar(&debug, "debug", false, "enable debug output (more verbose)")

	rootCmd.AddCommand(newPluginCommand())
	rootCmd.AddCommand(newConfigCommand())
	rootCmd.AddCommand(newApplyCommand())
	rootCmd.AddCommand(newVersionCommand())

	addProviderCommands()
}

func addProviderCommands() {
	plugins, err := plugin.Discover()
	if err != nil {
		return
	}

	for _, p := range plugins {
		cmd := newProviderCommand(p)
		rootCmd.AddCommand(cmd)
	}
}

func getFormatter() *output.Formatter {
	return output.NewFormatter(output.Format(outputFormat), os.Stdout)
}

func getContext() context.Context {
	return context.Background()
}
