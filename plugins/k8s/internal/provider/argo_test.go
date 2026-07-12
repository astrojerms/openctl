package provider

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestSummarizeArgoApp(t *testing.T) {
	u := unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "web"},
		"status": map[string]any{
			"health": map[string]any{"status": "Healthy"},
			"sync":   map[string]any{"status": "Synced"},
		},
	}}
	got := summarizeArgoApp(u)
	if got.Name != "web" || got.Health != "Healthy" || got.Sync != "Synced" {
		t.Errorf("summary = %+v", got)
	}

	// Missing status fields degrade gracefully to empty strings.
	bare := unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "x"}}}
	if g := summarizeArgoApp(bare); g.Name != "x" || g.Health != "" || g.Sync != "" {
		t.Errorf("bare summary = %+v", g)
	}
}

func TestArgoObserved(t *testing.T) {
	r := argoObserved("apps", []argoApp{
		{Name: "a", Health: "Healthy", Sync: "Synced"},
		{Name: "b", Health: "Degraded", Sync: "OutOfSync"},
	})
	if r.Status["count"] != 2 || r.Status["healthy"] != 1 {
		t.Errorf("count/healthy = %v/%v", r.Status["count"], r.Status["healthy"])
	}
	if r.Status["phase"] != "Degraded" {
		t.Errorf("phase = %v, want Degraded (not all healthy)", r.Status["phase"])
	}

	// All healthy → Ready. Empty → Ready (nothing unhealthy).
	if r := argoObserved("x", []argoApp{{Name: "a", Health: "Healthy"}}); r.Status["phase"] != "Ready" {
		t.Errorf("all-healthy phase = %v, want Ready", r.Status["phase"])
	}
	if r := argoObserved("x", nil); r.Status["phase"] != "Ready" || r.Status["count"] != 0 {
		t.Errorf("empty = %v", r.Status)
	}
}

func TestArgocdIsAPlatformComponent(t *testing.T) {
	comps := enabledComponents(map[string]any{"argocd": map[string]any{"enabled": true}})
	if len(comps) != 1 || comps[0].comp.name != "argocd" {
		t.Fatalf("argocd should be an enableable platform component, got %+v", comps)
	}
	if comps[0].chart.Name != "argo-cd" {
		t.Errorf("argocd chart = %q, want argo-cd", comps[0].chart.Name)
	}
}
