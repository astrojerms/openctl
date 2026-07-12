package provider

import (
	"context"
	"encoding/json"

	"github.com/openctl/openctl/pkg/pluginproto"
)

// ChildrenOf hangs a resource's in-cluster objects under it in the openctl
// children graph. Reaching the cluster needs the kubeconfig, which lives only
// in the resource's state blob — the external adapter threads it in via
// RefParams.State (see the CapabilityChildren wiring). Best-effort: a resource
// with no prior state, or an unreachable cluster, yields no children rather
// than an error, so the graph degrades gracefully.
//
// Platform is intentionally absent: its component HelmReleases already surface
// through Plan (CapabilityPlan), which the graph prefers over ChildrenOf.
func (p *provider) ChildrenOf(_ context.Context, req pluginproto.RefParams) ([]pluginproto.ResourceRef, error) {
	switch req.Kind {
	case kindHelmRelease:
		return p.childrenOfHelmRelease(req.State)
	case kindManifest:
		return p.childrenOfManifest(req.State)
	default:
		return nil, nil
	}
}

// childrenOfHelmRelease enumerates the objects a Helm release declares by
// parsing the rendered manifest Helm stores in-cluster (helm get manifest).
// These are the release's own top-level objects (Deployments, Services,
// ConfigMaps, …) — not the Pods those objects spawn, which aren't in the
// manifest.
func (p *provider) childrenOfHelmRelease(state json.RawMessage) ([]pluginproto.ResourceRef, error) {
	var st releaseState
	if len(state) > 0 {
		_ = json.Unmarshal(state, &st)
	}
	if !st.hasCluster() {
		return nil, nil // never applied / no state to reach the cluster with
	}
	kc, err := st.loadKubeconfig()
	if err != nil {
		return nil, nil
	}
	cfg, _, cleanup, err := newActionConfig(kc, st.Namespace)
	if err != nil {
		return nil, nil
	}
	defer cleanup()

	rel, err := getRelease(cfg, st.ReleaseName)
	if err != nil || rel == nil {
		return nil, nil
	}
	return refsFromReleaseManifest(rel.Manifest), nil
}

// refsFromReleaseManifest maps the objects declared in a rendered Helm manifest
// (multi-doc YAML) to graph refs, skipping documents that can't be addressed by
// name. Split out so it's testable without a cluster.
func refsFromReleaseManifest(manifest string) []pluginproto.ResourceRef {
	objs, err := parseObjects(manifest)
	if err != nil {
		return nil
	}
	out := make([]pluginproto.ResourceRef, 0, len(objs))
	for _, obj := range objs {
		name := obj.GetName()
		if name == "" {
			continue // generateName-only objects can't be addressed by name
		}
		out = append(out, pluginproto.ResourceRef{
			APIVersion: obj.GetAPIVersion(),
			Kind:       obj.GetKind(),
			Name:       name,
		})
	}
	return out
}

// childrenOfManifest reports the objects a Manifest applied, straight from its
// persisted state — no cluster round-trip needed since the refs are recorded
// at apply time.
func (p *provider) childrenOfManifest(state json.RawMessage) ([]pluginproto.ResourceRef, error) {
	var st manifestState
	if len(state) > 0 {
		_ = json.Unmarshal(state, &st)
	}
	out := make([]pluginproto.ResourceRef, 0, len(st.Objects))
	for _, ref := range st.Objects {
		if ref.Name == "" {
			continue
		}
		out = append(out, pluginproto.ResourceRef{
			APIVersion: apiVersionFor(ref.Group, ref.Version),
			Kind:       ref.Kind,
			Name:       ref.Name,
		})
	}
	return out, nil
}

// apiVersionFor rebuilds an object's apiVersion from its group + version: core
// objects (empty group) are just the version ("v1"); grouped objects join with
// a slash ("apps/v1").
func apiVersionFor(group, version string) string {
	if group == "" {
		return version
	}
	return group + "/" + version
}
