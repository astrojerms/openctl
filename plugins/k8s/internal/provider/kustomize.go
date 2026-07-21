package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"

	"github.com/openctl/openctl/pkg/pluginproto"
	"github.com/openctl/openctl/pkg/protocol"
)

// Kustomize is a deployment method as a provider (K4b): a first-class kind that
// renders a kustomization and applies the resulting objects, exactly like the
// Manifest kind but with the object YAML produced by `kustomize build` instead
// of a literal spec.manifest. State/Get/Delete are shared with Manifest — both
// are "a set of applied cluster objects" — so a Kustomize is a Manifest whose
// content is computed.
const kindKustomize = "Kustomize"

// renderKustomization builds the kustomization rooted at buildDir out of an
// in-memory file tree (path -> content), returning the rendered multi-document
// YAML. Pure and offline (kustomize's in-memory filesystem — no temp dirs, no
// network), so it is unit-testable independent of a cluster.
func renderKustomization(files map[string]string, buildDir string) (string, error) {
	fSys := filesys.MakeFsInMemory()
	// Deterministic write order (in-memory fs is a map, but be explicit).
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := fSys.WriteFile(n, []byte(files[n])); err != nil {
			return "", fmt.Errorf("stage %s: %w", n, err)
		}
	}
	k := krusty.MakeKustomizer(krusty.MakeDefaultOptions())
	resMap, err := k.Run(fSys, buildDir)
	if err != nil {
		return "", err
	}
	out, err := resMap.AsYaml()
	if err != nil {
		return "", fmt.Errorf("encode rendered objects: %w", err)
	}
	return string(out), nil
}

// kustomizeFiles extracts the spec.files map (path -> content). Every value must
// be a string; a non-string is a schema-level error surfaced clearly here.
func kustomizeFiles(spec map[string]any) (map[string]string, error) {
	raw, ok := spec["files"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("spec.files is required (map of path -> file content, including a kustomization.yaml)")
	}
	out := make(map[string]string, len(raw))
	for name, v := range raw {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("spec.files[%q] must be a string, got %T", name, v)
		}
		out[name] = s
	}
	return out, nil
}

func (p *provider) applyKustomize(ctx context.Context, m *protocol.Resource, prior json.RawMessage) (*pluginproto.ApplyResult, error) {
	content, path, err := kubeconfigFromSpec(m.Spec, m.Metadata.Name)
	if err != nil {
		return nil, err
	}
	files, err := kustomizeFiles(m.Spec)
	if err != nil {
		return nil, fmt.Errorf("kustomize %q: %w", m.Metadata.Name, err)
	}
	buildDir := specString(m.Spec, "path")
	if buildDir == "" {
		buildDir = "."
	}
	rendered, err := renderKustomization(files, buildDir)
	if err != nil {
		return nil, fmt.Errorf("kustomize build %q: %w", m.Metadata.Name, err)
	}
	// From here it is identical to a Manifest apply — parse, apply, prune, save.
	return p.applyObjectSet(ctx, m, content, path, rendered, kindKustomize, prior)
}

func (p *provider) getKustomize(ctx context.Context, req pluginproto.GetParams) (*pluginproto.GetResult, error) {
	return p.getObjectSet(ctx, req, kindKustomize)
}
