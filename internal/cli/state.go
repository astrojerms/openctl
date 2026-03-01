package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/openctl/openctl/internal/state"
)

func newStateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "state",
		Short: "Manage resource state",
		Long:  "Commands for viewing and managing the state of resources tracked by openctl.",
	}

	cmd.AddCommand(newStateListCommand())
	cmd.AddCommand(newStateGetCommand())
	cmd.AddCommand(newStateDeleteCommand())

	return cmd
}

func newStateListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list [provider]",
		Short: "List tracked resources",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := state.NewStore()
			if err != nil {
				return err
			}

			var states []*state.State
			if len(args) > 0 {
				states, err = store.List(args[0])
			} else {
				states, err = store.ListAll()
			}
			if err != nil {
				return err
			}

			if len(states) == 0 {
				fmt.Println("No resources tracked")
				return nil
			}

			// Table format output
			fmt.Printf("%-15s %-20s %-12s %s\n", "PROVIDER", "NAME", "PHASE", "CHILDREN")
			for _, s := range states {
				childSummary := summarizeChildren(s.Children)
				fmt.Printf("%-15s %-20s %-12s %s\n",
					s.Metadata.Provider,
					s.Metadata.Name,
					s.Status.Phase,
					childSummary,
				)
			}

			return nil
		},
	}
}

func newStateGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get <provider> <name>",
		Short: "Get state details for a resource",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := args[0]
			name := args[1]

			store, err := state.NewStore()
			if err != nil {
				return err
			}

			st, err := store.Get(provider, name)
			if err != nil {
				return err
			}

			// Output as YAML
			data, err := yaml.Marshal(st)
			if err != nil {
				return err
			}

			fmt.Print(string(data))
			return nil
		},
	}
}

func newStateDeleteCommand() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <provider> <name>",
		Short: "Delete state for a resource",
		Long: `Delete state tracking for a resource.

This only removes the state tracking file - it does NOT delete the actual resource.
Use this to clean up stale state or after manually deleting resources.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := args[0]
			name := args[1]

			store, err := state.NewStore()
			if err != nil {
				return err
			}

			// Check if state exists
			st, err := store.Get(provider, name)
			if err != nil {
				return fmt.Errorf("state not found: %s/%s", provider, name)
			}

			// Warn about children unless forced
			if len(st.Children) > 0 && !force {
				fmt.Fprintf(os.Stderr, "Warning: This state has %d children:\n", len(st.Children))
				for _, child := range st.Children {
					fmt.Fprintf(os.Stderr, "  - %s/%s/%s\n", child.Provider, child.Kind, child.Name)
				}
				fmt.Fprintf(os.Stderr, "\nThese resources may become orphaned. Use --force to delete anyway.\n")
				return fmt.Errorf("deletion aborted")
			}

			if err := store.Delete(provider, name); err != nil {
				return err
			}

			fmt.Printf("State deleted: %s/%s\n", provider, name)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force deletion even if state has children")

	return cmd
}

func summarizeChildren(children []state.ChildReference) string {
	if len(children) == 0 {
		return "-"
	}

	// Count children by provider/kind
	counts := make(map[string]int)
	for _, child := range children {
		key := fmt.Sprintf("%s/%s", child.Provider, child.Kind)
		counts[key]++
	}

	var parts []string
	for key, count := range counts {
		parts = append(parts, fmt.Sprintf("%d %s", count, key))
	}

	return strings.Join(parts, ", ")
}
