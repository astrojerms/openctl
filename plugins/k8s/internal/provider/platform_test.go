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
