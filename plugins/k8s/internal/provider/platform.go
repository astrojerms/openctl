package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/openctl/openctl/pkg/pluginproto"
	"github.com/openctl/openctl/pkg/protocol"
)

// Platform is an opt-in composite: a curated, infra-coupled platform layer that
// fans out into one Helm release per enabled component. Nothing is enabled by
// default. Unlike a native k3s composite, an external plugin's Plan children are
// not auto-applied by the controller, so Platform.Apply installs the component
// releases directly (reusing the Helm engine); Plan provides the UI graph.
//
// The cloudflared token flows through as a `$secret` in that component's values:
// openctl resolves it before Apply (so Helm gets the real token) but persists
// only the raw marker (no leak). The token can be auto-wired from the Cloudflare
// Tunnel via the `action` secret provider — e.g. token:
// {$secret: {provider: action, key: "cloudflare.openctl.io/v1/Tunnel/<n>#get-token"}}
// — which runs the Tunnel's get-token action at resolve time; or supplied by
// hand (run get-token once, store the token) for a file/env secret.

const kindPlatform = "Platform"

// component is a known platform piece with default chart coordinates. Users
// enable it and may override chart/namespace/values.
type component struct {
	name         string
	defaultRepo  string
	defaultChart string
	defaultNS    string
}

// platformComponents is the curated, opinionated set. Traefik is the ingress
// (ingress-nginx is in maintenance; no Cilium-CNI dependency); cloudflared wires
// a Cloudflare Tunnel to in-cluster services.
var platformComponents = []component{
	{"traefik", "https://traefik.github.io/charts", "traefik", "traefik"},
	{"cloudflared", "https://cloudflare.github.io/helm-charts", "cloudflare-tunnel-remote", "cloudflared"},
	{"argocd", "https://argoproj.github.io/argo-helm", "argo-cd", "argocd"},
	// nvidiaDevicePlugin advertises nvidia.com/gpu on nodes that have the
	// NVIDIA runtime + drivers, so GPU workloads (e.g. a local model / Ollama)
	// can request a GPU. Pairs with a k3s worker pool that has GPU passthrough.
	{"nvidiaDevicePlugin", "https://nvidia.github.io/k8s-device-plugin", "nvidia-device-plugin", "nvidia-device-plugin"},
	// nfsProvisioner gives workloads a dynamic, NFS-backed StorageClass (e.g. a
	// Synology share) so stateful services (media, Home Assistant) get
	// persistent volumes on request. Set nfsProvisioner.values.nfs.{server,path}
	// to the NAS export.
	{"nfsProvisioner", "https://kubernetes-sigs.github.io/nfs-subdir-external-provisioner", "nfs-subdir-external-provisioner", "nfs-provisioner"},
}

// releaseCoord locates an installed component release for Get/Delete/prune.
type releaseCoord struct {
	Component string `json:"component"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type platformState struct {
	kubeconfigState
	Releases []releaseCoord `json:"releases,omitempty"`
}

// componentRelease is a resolved, enabled component ready to install.
type componentRelease struct {
	comp   component
	chart  chartSpec
	opts   releaseOpts
	values map[string]any
}

// enabledComponents parses the Platform spec into the set of enabled component
// releases, applying per-component overrides over the curated defaults.
func enabledComponents(spec map[string]any) []componentRelease {
	var out []componentRelease
	for _, c := range platformComponents {
		raw, _ := spec[c.name].(map[string]any)
		if raw == nil || !specBool(raw, "enabled") {
			continue
		}
		ns := specString(raw, "namespace")
		if ns == "" {
			ns = c.defaultNS
		}
		chart := chartSpec{Repo: c.defaultRepo, Name: c.defaultChart}
		if cr, ok := raw["chart"].(map[string]any); ok {
			if v := specString(cr, "repo"); v != "" {
				chart.Repo = v
			}
			if v := specString(cr, "name"); v != "" {
				chart.Name = v
			}
			chart.Version = specString(cr, "version")
		}
		values, _ := raw["values"].(map[string]any)
		out = append(out, componentRelease{
			comp:  c,
			chart: chart,
			opts: releaseOpts{
				releaseName:     c.name,
				namespace:       ns,
				createNamespace: true,
				values:          values,
				wait:            specBool(raw, "wait"),
				timeout:         5 * time.Minute,
			},
			values: values,
		})
	}
	return out
}

func (p *provider) applyPlatform(_ context.Context, m *protocol.Resource, prior json.RawMessage) (*pluginproto.ApplyResult, error) {
	content, path, err := kubeconfigFromSpec(m.Spec, m.Metadata.Name)
	if err != nil {
		return nil, err
	}
	comps := enabledComponents(m.Spec)

	installed := make([]releaseCoord, 0, len(comps))
	names := make([]string, 0, len(comps))
	for i := range comps {
		cr := &comps[i]
		cfg, settings, cleanup, err := newActionConfig(content, cr.opts.namespace)
		if err != nil {
			return nil, err
		}
		ch, err := loadChart(cfg, settings, cr.chart)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("platform %q: load %s chart: %w", m.Metadata.Name, cr.comp.name, err)
		}
		if _, err := installOrUpgrade(cfg, cr.opts, ch); err != nil {
			cleanup()
			return nil, fmt.Errorf("platform %q: install %s: %w", m.Metadata.Name, cr.comp.name, err)
		}
		cleanup()
		installed = append(installed, releaseCoord{Component: cr.comp.name, Name: cr.opts.releaseName, Namespace: cr.opts.namespace})
		names = append(names, cr.comp.name)
	}

	// Prune components that were enabled before and now aren't (uninstall).
	var priorState platformState
	if len(prior) > 0 {
		_ = json.Unmarshal(prior, &priorState)
	}
	for _, rc := range prunedReleases(priorState.Releases, installed) {
		cfg, _, cleanup, err := newActionConfig(content, rc.Namespace)
		if err != nil {
			return nil, err
		}
		err = uninstall(cfg, rc.Name)
		cleanup()
		if err != nil {
			return nil, fmt.Errorf("platform %q: prune %s: %w", m.Metadata.Name, rc.Component, err)
		}
	}

	st, _ := json.Marshal(platformState{kubeconfigState: kubeconfigStateOf(content, path), Releases: installed})
	sort.Strings(names)
	status := map[string]any{"components": names, "enabled": len(names), "phase": "Ready"}
	r := &protocol.Resource{APIVersion: apiVersion, Kind: kindPlatform, Spec: platformObservedSpec(m.Spec), Status: status}
	r.Metadata.Name = m.Metadata.Name
	return &pluginproto.ApplyResult{Resource: r, State: st}, nil
}

func (p *provider) getPlatform(_ context.Context, req pluginproto.GetParams) (*pluginproto.GetResult, error) {
	var st platformState
	if len(req.State) > 0 {
		_ = json.Unmarshal(req.State, &st)
	}
	if len(st.Releases) == 0 || !st.present() {
		return nil, pluginproto.NotFound(fmt.Sprintf("Platform %q has no prior state", req.Name))
	}
	kc, err := st.loadKubeconfig()
	if err != nil {
		return nil, err
	}
	ready := 0
	names := make([]string, 0, len(st.Releases))
	for _, rc := range st.Releases {
		cfg, _, cleanup, err := newActionConfig(kc, rc.Namespace)
		if err != nil {
			return nil, err
		}
		rel, err := getRelease(cfg, rc.Name)
		cleanup()
		if err == nil {
			names = append(names, rc.Component)
			if phaseFor(rel) == "Ready" {
				ready++
			}
		}
	}
	if len(names) == 0 {
		return nil, pluginproto.NotFound(fmt.Sprintf("Platform %q components not found", req.Name))
	}
	phase := "Ready"
	if ready < len(st.Releases) {
		phase = "Degraded"
	}
	r := &protocol.Resource{APIVersion: apiVersion, Kind: kindPlatform, Status: map[string]any{
		"components": names, "ready": ready, "enabled": len(st.Releases), "phase": phase,
	}}
	r.Metadata.Name = req.Name
	return &pluginproto.GetResult{Resource: r, State: req.State}, nil
}

func (p *provider) deletePlatform(_ context.Context, req pluginproto.DeleteParams) error {
	var st platformState
	if len(req.State) > 0 {
		_ = json.Unmarshal(req.State, &st)
	}
	if len(st.Releases) == 0 || !st.present() {
		return nil
	}
	kc, err := st.loadKubeconfig()
	if err != nil {
		return err
	}
	for _, rc := range st.Releases {
		cfg, _, cleanup, err := newActionConfig(kc, rc.Namespace)
		if err != nil {
			return err
		}
		err = uninstall(cfg, rc.Name)
		cleanup()
		if err != nil {
			return fmt.Errorf("delete %s: %w", rc.Component, err)
		}
	}
	return nil
}

// planPlatform emits a HelmRelease child descriptor per enabled component, for
// the UI composition graph. Children carry owner labels attributing them to the
// Platform (the controller doesn't apply them — Apply installs the releases).
func (p *provider) planPlatform(m *protocol.Resource) *pluginproto.PlanResult {
	comps := enabledComponents(m.Spec)
	children := make([]*protocol.Resource, 0, len(comps))
	for i := range comps {
		cr := &comps[i]
		child := &protocol.Resource{
			APIVersion: apiVersion,
			Kind:       kindHelmRelease,
			Spec: map[string]any{
				"namespace": cr.opts.namespace,
				"chart":     map[string]any{"repo": cr.chart.Repo, "name": cr.chart.Name, "version": cr.chart.Version},
			},
		}
		child.Metadata.Name = m.Metadata.Name + "-" + cr.comp.name
		child.Metadata.Labels = map[string]string{
			"openctl.io/owner-kind": kindPlatform,
			"openctl.io/owner-name": m.Metadata.Name,
		}
		children = append(children, child)
	}
	return &pluginproto.PlanResult{Children: children}
}

// platformObservedSpec echoes the desired spec minus credentials.
func platformObservedSpec(spec map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range spec {
		if k == "kubeconfig" || k == "kubeconfigPath" {
			continue
		}
		out[k] = v
	}
	return out
}

// prunedReleases returns component releases present in prior but absent now.
func prunedReleases(prior, current []releaseCoord) []releaseCoord {
	keep := make(map[string]bool, len(current))
	for _, r := range current {
		keep[r.Component] = true
	}
	var out []releaseCoord
	for _, r := range prior {
		if !keep[r.Component] {
			out = append(out, r)
		}
	}
	return out
}
