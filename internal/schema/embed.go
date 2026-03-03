package schema

import (
	"embed"
	"io/fs"
	"path/filepath"
	"strings"

	"cuelang.org/go/cue/load"
)

//go:embed all:schemas
var schemasFS embed.FS

// GetOverlay returns a CUE overlay that maps embedded schema files
// to the CUE module path for use with cue/load. The dir parameter
// specifies the working directory where the overlay will be rooted.
func GetOverlay(dir string) map[string]load.Source {
	overlay := make(map[string]load.Source)

	// Create a module.cue file in the working directory to enable imports
	moduleCue := `module: "openctl.local"
language: version: "v0.9.0"
`
	overlay[filepath.Join(dir, "cue.mod", "module.cue")] = load.FromBytes([]byte(moduleCue))

	// Map embedded schemas to the CUE module package path
	_ = fs.WalkDir(schemasFS, "schemas", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, readErr := schemasFS.ReadFile(path)
		if readErr != nil {
			return nil
		}
		// Strip "schemas/" prefix and map to CUE module pkg path
		relPath := strings.TrimPrefix(path, "schemas/")
		// Skip the cue.mod directory from the embedded schemas - we create our own
		if strings.HasPrefix(relPath, "cue.mod") {
			return nil
		}
		modPath := filepath.Join(dir, "cue.mod", "pkg", "openctl.io", "schemas", relPath)
		overlay[modPath] = load.FromBytes(data)
		return nil
	})
	return overlay
}
