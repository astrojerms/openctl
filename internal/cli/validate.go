package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/openctl/openctl/internal/manifest"
)

func newValidateCommand() *cobra.Command {
	var file string

	cmd := &cobra.Command{
		Use:   "validate -f <file.cue>",
		Short: "Validate a CUE manifest",
		RunE: func(cmd *cobra.Command, args []string) error {
			resources, err := manifest.LoadCUE(file)
			if err != nil {
				return fmt.Errorf("validation failed: %w", err)
			}

			fmt.Printf("Valid: %d resource(s)\n", len(resources))
			for _, r := range resources {
				fmt.Printf("  %s %s\n", r.Kind, r.Metadata.Name)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "", "CUE file to validate")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}
