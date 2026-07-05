package schema

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"cuelang.org/go/cue/load"
)

//go:embed all:schemas
var schemasFS embed.FS

// Info describes one embedded CUE schema for UI introspection
// (SchemaService.ListSchemas / GetSchema). Returned by Registry.
type Info struct {
	APIVersion string
	Kind       string
	Provider   string
	FileName   string // relative path under schemas/ (e.g. "proxmox/vm.cue")
}

// Registry returns the list of (apiVersion, kind) pairs that have a schema —
// the embedded built-ins plus any external schemas registered at runtime by
// provider plugins (RegisterExternal). The embedded mapping mirrors
// schemaSelector — adding a new built-in kind requires updating both. Keeping
// the mapping explicit avoids over-trusting filesystem layout.
func Registry() []Info {
	infos := []Info{
		{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "VirtualMachine",
			Provider:   "proxmox",
			FileName:   "proxmox/vm.cue",
		},
		{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "ProxmoxNode",
			Provider:   "proxmox",
			FileName:   "proxmox/node.cue",
		},
		{
			APIVersion: "k3s.openctl.io/v1",
			Kind:       "Cluster",
			Provider:   "k3s",
			FileName:   "k3s/cluster.cue",
		},
		{
			APIVersion: "k3s.openctl.io/v1",
			Kind:       "K3sNode",
			Provider:   "k3s",
			FileName:   "k3s/node.cue",
		},
		{
			APIVersion: "k3s.openctl.io/v1",
			Kind:       "AgentInstall",
			Provider:   "k3s",
			FileName:   "k3s/agentinstall.cue",
		},
	}
	return append(infos, externalInfos()...)
}

// SourceFor returns the raw CUE text for the given schema. Used by
// SchemaService.GetSchema to feed the editor. External schemas (empty
// FileName) come from the runtime plugin registry; embedded schemas are read
// from the embedded FS.
func SourceFor(info Info) ([]byte, error) {
	if info.FileName == "" {
		s, ok := lookupExternal(info.APIVersion, info.Kind)
		if !ok {
			return nil, fmt.Errorf("no external schema registered for %s/%s", info.APIVersion, info.Kind)
		}
		return []byte(s.source), nil
	}
	return schemasFS.ReadFile("schemas/" + info.FileName)
}

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
