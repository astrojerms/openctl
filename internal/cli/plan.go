package cli

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/openctl/openctl/internal/manifest"
	"github.com/openctl/openctl/internal/plan"
	"github.com/openctl/openctl/pkg/protocol"
)

func newPlanCommand() *cobra.Command {
	var files []string

	cmd := &cobra.Command{
		Use:   "plan -f <file|dir> [-f ...]",
		Short: "Preview the apply order and $ref dependency graph for a set of manifests",
		Long: "Load a set of manifests (files or directories, .cue/.yaml/.yml) and print the\n" +
			"cross-resource dependency graph derived from their $ref markers: the\n" +
			"topological apply order as waves (resources in the same wave have no\n" +
			"inter-dependency and can apply concurrently), what each resource waits on, and\n" +
			"any $ref targets that are NOT in the set (they must already exist).\n\n" +
			"This is offline — it needs no controller connection. (Per-resource dry-run\n" +
			"diffs need a running controller and are not included here.)",
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := append([]string{}, files...)
			paths = append(paths, args...) // also accept bare path args
			if len(paths) == 0 {
				return fmt.Errorf("no manifests: pass -f <file|dir> (or bare path args)")
			}
			resources, err := loadManifestBatch(paths)
			if err != nil {
				return err
			}
			if len(resources) == 0 {
				return fmt.Errorf("no manifests found in %s", strings.Join(paths, ", "))
			}
			p, err := plan.Build(resources)
			if err != nil {
				return fmt.Errorf("plan: %w", err)
			}
			printPlan(cmd.OutOrStdout(), p)
			return nil
		},
	}
	cmd.Flags().StringArrayVarP(&files, "file", "f", nil, "Manifest file or directory (repeatable)")
	return cmd
}

// loadManifestBatch loads every manifest under the given paths (files or
// directories), dispatching by extension. Directories are walked recursively
// for .cue/.yaml/.yml files, in sorted order for deterministic output.
func loadManifestBatch(paths []string) ([]*protocol.Resource, error) {
	var files []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		if !info.IsDir() {
			files = append(files, p)
			continue
		}
		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && isManifestFile(path) {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", p, err)
		}
	}
	sort.Strings(files)

	var out []*protocol.Resource
	for _, f := range files {
		rs, err := loadManifestFile(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		out = append(out, rs...)
	}
	return out, nil
}

func isManifestFile(path string) bool {
	switch filepath.Ext(path) {
	case ".cue", ".yaml", ".yml":
		return true
	}
	return false
}

func loadManifestFile(path string) ([]*protocol.Resource, error) {
	switch filepath.Ext(path) {
	case ".cue":
		return manifest.LoadCUE(path)
	case ".yaml", ".yml":
		return manifest.LoadMultiple(path)
	default:
		return nil, fmt.Errorf("unsupported manifest extension (want .cue/.yaml/.yml)")
	}
}

func printPlan(w io.Writer, p *plan.Plan) {
	fmt.Fprintf(w, "Plan: %d resource(s), %d wave(s)\n\n", p.Count(), len(p.Waves))
	fmt.Fprintln(w, "Apply order:")

	external := map[string][]string{} // node display -> external refs
	for i, wave := range p.Waves {
		fmt.Fprintf(w, "  wave %d:\n", i+1)
		for _, n := range wave {
			line := "    " + n.Display()
			if len(n.Deps) > 0 {
				line += "  ← " + strings.Join(n.Deps, ", ")
			}
			fmt.Fprintln(w, line)
			if len(n.External) > 0 {
				external[n.Display()] = n.External
			}
		}
	}

	if len(external) > 0 {
		fmt.Fprintln(w, "\nExternal references (must already exist — not in this set):")
		names := make([]string, 0, len(external))
		for k := range external {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			fmt.Fprintf(w, "    %s → %s\n", k, strings.Join(external[k], ", "))
		}
	}
}
