package provider

import (
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

func platformResource(spec map[string]any) *protocol.Resource {
	spec["kubeconfig"] = "KCONF"
	m := &protocol.Resource{Kind: kindPlatform, Spec: spec}
	m.Metadata.Name = "edge"
	return m
}

func TestEnabledComponents(t *testing.T) {
	// Only traefik enabled; cloudflared present-but-disabled and an untouched one.
	comps := enabledComponents(map[string]any{
		"traefik": map[string]any{
			"enabled":   true,
			"namespace": "ingress",
			"chart":     map[string]any{"version": "36.0.0"},
			"values":    map[string]any{"service": map[string]any{"type": "ClusterIP"}},
		},
		"cloudflared": map[string]any{"enabled": false},
	})
	if len(comps) != 1 {
		t.Fatalf("enabled = %d, want 1 (traefik only)", len(comps))
	}
	tr := comps[0]
	if tr.comp.name != "traefik" || tr.opts.namespace != "ingress" {
		t.Errorf("traefik = %s/%s", tr.comp.name, tr.opts.namespace)
	}
	if tr.chart.Repo != "https://traefik.github.io/charts" || tr.chart.Name != "traefik" || tr.chart.Version != "36.0.0" {
		t.Errorf("chart = %+v (defaults with version override expected)", tr.chart)
	}
	if !tr.opts.createNamespace {
		t.Error("component should create its namespace")
	}

	// Nothing enabled → empty (opt-in, never default).
	if got := enabledComponents(map[string]any{}); len(got) != 0 {
		t.Errorf("no components should be enabled by default, got %d", len(got))
	}
}

func TestEnabledComponents_NfsProvisioner(t *testing.T) {
	comps := enabledComponents(map[string]any{
		"nfsProvisioner": map[string]any{
			"enabled": true,
			"values":  map[string]any{"nfs": map[string]any{"server": "10.0.0.5", "path": "/volume1/k8s"}},
		},
	})
	if len(comps) != 1 {
		t.Fatalf("enabled = %d, want 1 (nfsProvisioner)", len(comps))
	}
	c := comps[0]
	if c.comp.name != "nfsProvisioner" {
		t.Errorf("component = %q", c.comp.name)
	}
	if c.chart.Repo != "https://kubernetes-sigs.github.io/nfs-subdir-external-provisioner" || c.chart.Name != "nfs-subdir-external-provisioner" {
		t.Errorf("chart defaults wrong: %+v", c.chart)
	}
	if c.opts.namespace != "nfs-provisioner" {
		t.Errorf("default namespace = %q", c.opts.namespace)
	}
	// The NAS export values pass through to the chart.
	nfs, _ := c.values["nfs"].(map[string]any)
	if nfs["server"] != "10.0.0.5" || nfs["path"] != "/volume1/k8s" {
		t.Errorf("nfs values not threaded: %+v", c.values)
	}
}

func TestEnabledComponents_Longhorn(t *testing.T) {
	comps := enabledComponents(map[string]any{
		"longhorn": map[string]any{
			"enabled": true,
			"values":  map[string]any{"defaultSettings": map[string]any{"defaultReplicaCount": 2}},
		},
	})
	if len(comps) != 1 {
		t.Fatalf("enabled = %d, want 1 (longhorn)", len(comps))
	}
	c := comps[0]
	if c.comp.name != "longhorn" {
		t.Errorf("component = %q", c.comp.name)
	}
	if c.chart.Repo != "https://charts.longhorn.io" || c.chart.Name != "longhorn" {
		t.Errorf("chart defaults wrong: %+v", c.chart)
	}
	if c.opts.namespace != "longhorn-system" {
		t.Errorf("default namespace = %q, want longhorn-system", c.opts.namespace)
	}
	// User values (e.g. replica count) thread through to the chart.
	ds, _ := c.values["defaultSettings"].(map[string]any)
	if ds["defaultReplicaCount"] != 2 {
		t.Errorf("longhorn values not threaded: %+v", c.values)
	}
}

func TestEnabledComponents_NvidiaDevicePlugin(t *testing.T) {
	comps := enabledComponents(map[string]any{
		"nvidiaDevicePlugin": map[string]any{"enabled": true},
	})
	if len(comps) != 1 {
		t.Fatalf("enabled = %d, want 1 (nvidiaDevicePlugin)", len(comps))
	}
	c := comps[0]
	if c.comp.name != "nvidiaDevicePlugin" {
		t.Errorf("component = %q", c.comp.name)
	}
	if c.chart.Repo != "https://nvidia.github.io/k8s-device-plugin" || c.chart.Name != "nvidia-device-plugin" {
		t.Errorf("chart defaults wrong: %+v", c.chart)
	}
	if c.opts.namespace != "nvidia-device-plugin" {
		t.Errorf("default namespace = %q", c.opts.namespace)
	}
}

// TestEnabledComponents_Generic covers the K4(a) generic path: an arbitrary
// chart declared under `components` installs with no preset, defaults enabled
// to true, defaults its namespace to its name, and threads chart+values.
func TestEnabledComponents_Generic(t *testing.T) {
	comps := enabledComponents(map[string]any{
		"components": map[string]any{
			"prometheus": map[string]any{
				"chart":  map[string]any{"repo": "https://prometheus-community.github.io/helm-charts", "name": "kube-prometheus-stack", "version": "65.0.0"},
				"values": map[string]any{"grafana": map[string]any{"enabled": false}},
			},
		},
	})
	if len(comps) != 1 {
		t.Fatalf("enabled = %d, want 1 (generic prometheus)", len(comps))
	}
	c := comps[0]
	if c.comp.name != "prometheus" || c.opts.releaseName != "prometheus" {
		t.Errorf("name/release = %s/%s", c.comp.name, c.opts.releaseName)
	}
	if c.chart.Repo != "https://prometheus-community.github.io/helm-charts" || c.chart.Name != "kube-prometheus-stack" || c.chart.Version != "65.0.0" {
		t.Errorf("chart wrong: %+v", c.chart)
	}
	if c.opts.namespace != "prometheus" { // defaults to the release name
		t.Errorf("namespace = %q, want prometheus (defaulted to name)", c.opts.namespace)
	}
	g, _ := c.values["grafana"].(map[string]any)
	if g["enabled"] != false {
		t.Errorf("values not threaded: %+v", c.values)
	}
}

// TestEnabledComponents_GenericDisabledAndInvalid: an explicit enabled:false
// generic component is skipped, and one without chart coordinates is skipped
// (defensive — the schema requires chart.repo+name).
func TestEnabledComponents_GenericDisabledAndInvalid(t *testing.T) {
	comps := enabledComponents(map[string]any{
		"components": map[string]any{
			"off":       map[string]any{"enabled": false, "chart": map[string]any{"repo": "r", "name": "n"}},
			"nochart":   map[string]any{"namespace": "x"},
			"malformed": map[string]any{"chart": map[string]any{"repo": "r"}}, // no name
		},
	})
	if len(comps) != 0 {
		t.Fatalf("enabled = %d, want 0 (all skipped)", len(comps))
	}
}

// TestEnabledComponents_GenericOverridesPreset: a generic component sharing a
// preset's name overrides that preset's chart, keeping a single release.
func TestEnabledComponents_GenericOverridesPreset(t *testing.T) {
	comps := enabledComponents(map[string]any{
		"traefik": map[string]any{"enabled": true},
		"components": map[string]any{
			"traefik": map[string]any{"chart": map[string]any{"repo": "https://example.test/charts", "name": "my-traefik"}},
		},
	})
	if len(comps) != 1 {
		t.Fatalf("enabled = %d, want 1 (deduped)", len(comps))
	}
	if comps[0].chart.Name != "my-traefik" || comps[0].chart.Repo != "https://example.test/charts" {
		t.Errorf("generic should override preset chart, got %+v", comps[0].chart)
	}
}

// TestEnabledComponents_PresetAndGenericOrder: presets come first in
// declaration order, then generic components sorted by name.
func TestEnabledComponents_PresetAndGenericOrder(t *testing.T) {
	comps := enabledComponents(map[string]any{
		"traefik": map[string]any{"enabled": true},
		"components": map[string]any{
			"zzz": map[string]any{"chart": map[string]any{"repo": "r", "name": "z"}},
			"aaa": map[string]any{"chart": map[string]any{"repo": "r", "name": "a"}},
		},
	})
	got := []string{}
	for _, c := range comps {
		got = append(got, c.comp.name)
	}
	want := []string{"traefik", "aaa", "zzz"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestPlanPlatform(t *testing.T) {
	p := New()
	m := platformResource(map[string]any{
		"traefik":     map[string]any{"enabled": true},
		"cloudflared": map[string]any{"enabled": true, "namespace": "cf"},
	})
	res := p.planPlatform(m)
	if len(res.Children) != 2 {
		t.Fatalf("children = %d, want 2", len(res.Children))
	}
	names := map[string]bool{}
	for _, c := range res.Children {
		if c.Kind != kindHelmRelease {
			t.Errorf("child kind = %s, want HelmRelease", c.Kind)
		}
		if c.Metadata.Labels["openctl.io/owner-kind"] != kindPlatform || c.Metadata.Labels["openctl.io/owner-name"] != "edge" {
			t.Errorf("child %s owner labels = %v", c.Metadata.Name, c.Metadata.Labels)
		}
		names[c.Metadata.Name] = true
	}
	if !names["edge-traefik"] || !names["edge-cloudflared"] {
		t.Errorf("child names = %v, want edge-traefik + edge-cloudflared", names)
	}
}

func TestPrunedReleases(t *testing.T) {
	rc := func(comp string) releaseCoord { return releaseCoord{Component: comp, Name: comp, Namespace: comp} }
	prior := []releaseCoord{rc("traefik"), rc("cloudflared")}
	current := []releaseCoord{rc("traefik")}
	pruned := prunedReleases(prior, current)
	if len(pruned) != 1 || pruned[0].Component != "cloudflared" {
		t.Fatalf("pruned = %+v, want just cloudflared", pruned)
	}
}
