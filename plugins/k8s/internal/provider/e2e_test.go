package provider

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/openctl/openctl/pkg/pluginproto"
	"github.com/openctl/openctl/pkg/protocol"
)

// TestE2EHelmRelease drives the full provider (Apply/Get/Delete) against a REAL
// Kubernetes cluster, installing the published podinfo chart from both an HTTP
// repo and an OCI registry. Gated on KUBECONFIG_E2E (a kubeconfig path) so CI
// stays hermetic; run locally against k3d/kind/homelab:
//
//	k3d cluster create openctl-e2e
//	KUBECONFIG_E2E="$(k3d kubeconfig write openctl-e2e)" \
//	  go test ./internal/provider/ -run TestE2EHelmRelease -v
func TestE2EHelmRelease(t *testing.T) {
	kcPath := os.Getenv("KUBECONFIG_E2E")
	if kcPath == "" {
		t.Skip("set KUBECONFIG_E2E to a kubeconfig path to run the real-cluster e2e")
	}
	kc, err := os.ReadFile(kcPath)
	if err != nil {
		t.Fatalf("read kubeconfig: %v", err)
	}

	cases := []struct {
		name   string
		chart  map[string]any
		byPath bool // supply the kubeconfig as a path (the $ref/Cluster route)
	}{
		{"http", map[string]any{
			"repo": "https://stefanprodan.github.io/podinfo", "name": "podinfo", "version": "6.7.0",
		}, false},
		{"oci", map[string]any{
			"repo": "oci://ghcr.io/stefanprodan/charts", "name": "podinfo", "version": "6.7.0",
		}, false},
		{"kubeconfig-path", map[string]any{
			"repo": "https://stefanprodan.github.io/podinfo", "name": "podinfo", "version": "6.7.0",
		}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			p := New()
			relName := "podinfo-" + tc.name
			m := &protocol.Resource{APIVersion: apiVersion, Kind: kindHelmRelease}
			m.Metadata.Name = relName
			m.Spec = map[string]any{
				"namespace":       "openctl-e2e-" + tc.name,
				"createNamespace": true,
				"chart":           tc.chart,
				"wait":            true,
				"timeout":         "3m",
				"values":          map[string]any{"replicaCount": 1.0},
			}
			// The path route mirrors what a Cluster $ref resolves to; the content
			// route mirrors an inline external kubeconfig ($secret).
			if tc.byPath {
				m.Spec["kubeconfigPath"] = kcPath
			} else {
				m.Spec["kubeconfig"] = string(kc)
			}

			ar, err := p.Apply(ctx, pluginproto.ApplyParams{Manifest: m})
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if ar.Resource.Status["phase"] != "Ready" {
				t.Fatalf("phase = %v, want Ready (status %v)", ar.Resource.Status["phase"], ar.Resource.Status["status"])
			}
			// State carries what Get/Delete need; the kubeconfig must not leak into
			// the observed spec.
			if _, leaked := ar.Resource.Spec["kubeconfig"]; leaked {
				t.Error("observed spec leaked kubeconfig")
			}
			var st releaseState
			if err := json.Unmarshal(ar.State, &st); err != nil || st.ReleaseName != relName {
				t.Fatalf("state = %s (err %v)", ar.State, err)
			}

			// Get from persisted state.
			gr, err := p.Get(ctx, pluginproto.GetParams{Kind: kindHelmRelease, Name: relName, State: ar.State})
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if gr.Resource.Status["status"] != "deployed" {
				t.Errorf("Get status = %v, want deployed", gr.Resource.Status["status"])
			}

			// Idempotent re-apply bumps the revision (upgrade path).
			ar2, err := p.Apply(ctx, pluginproto.ApplyParams{Manifest: m, State: ar.State})
			if err != nil {
				t.Fatalf("re-Apply: %v", err)
			}
			if rev, _ := ar2.Resource.Status["revision"].(int); rev < 2 {
				t.Errorf("re-apply revision = %v, want >= 2", ar2.Resource.Status["revision"])
			}

			// Delete, then Get is NotFound.
			if err := p.Delete(ctx, pluginproto.DeleteParams{Kind: kindHelmRelease, State: ar.State}); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			_, err = p.Get(ctx, pluginproto.GetParams{Kind: kindHelmRelease, Name: relName, State: ar.State})
			var perr *pluginproto.Error
			if !errors.As(err, &perr) || perr.Code != pluginproto.CodeNotFound {
				t.Fatalf("Get after Delete = %v, want NotFound", err)
			}
		})
	}
}

// TestE2EHelmReleaseChildren installs a real chart and proves ChildrenOf reads
// the release's declared objects back out of the in-cluster manifest — the
// upward-into-workloads view (Deployment/Service hang under the HelmRelease).
// Gated on KUBECONFIG_E2E.
func TestE2EHelmReleaseChildren(t *testing.T) {
	kcPath := os.Getenv("KUBECONFIG_E2E")
	if kcPath == "" {
		t.Skip("set KUBECONFIG_E2E to a kubeconfig path to run the real-cluster e2e")
	}
	kc, err := os.ReadFile(kcPath)
	if err != nil {
		t.Fatalf("read kubeconfig: %v", err)
	}
	ctx := context.Background()
	p := New()

	relName := "podinfo-children"
	m := &protocol.Resource{APIVersion: apiVersion, Kind: kindHelmRelease}
	m.Metadata.Name = relName
	m.Spec = map[string]any{
		"kubeconfig":      string(kc),
		"namespace":       "openctl-e2e-children",
		"createNamespace": true,
		"chart":           map[string]any{"repo": "https://stefanprodan.github.io/podinfo", "name": "podinfo", "version": "6.7.0"},
		"wait":            true,
		"timeout":         "3m",
		"values":          map[string]any{"replicaCount": 1.0},
	}
	ar, err := p.Apply(ctx, pluginproto.ApplyParams{Manifest: m})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() {
		_ = p.Delete(ctx, pluginproto.DeleteParams{Kind: kindHelmRelease, State: ar.State})
	})

	kids, err := p.ChildrenOf(ctx, pluginproto.RefParams{Kind: kindHelmRelease, Name: relName, State: ar.State})
	if err != nil {
		t.Fatalf("ChildrenOf: %v", err)
	}
	// podinfo declares at least a Deployment and a Service.
	var dep, svc *pluginproto.ResourceRef
	for i := range kids {
		switch kids[i].Kind {
		case "Deployment":
			dep = &kids[i]
		case "Service":
			svc = &kids[i]
		}
	}
	if dep == nil || dep.APIVersion != "apps/v1" {
		t.Errorf("expected an apps/v1 Deployment child, got %+v", kids)
	}
	if svc == nil || svc.APIVersion != "v1" {
		t.Errorf("expected a v1 Service child, got %+v", kids)
	}

	// A resource with no state can't reach the cluster → no children, no error.
	none, err := p.ChildrenOf(ctx, pluginproto.RefParams{Kind: kindHelmRelease, Name: "ghost"})
	if err != nil || none != nil {
		t.Errorf("no-state ChildrenOf = (%v, %v), want (nil, nil)", none, err)
	}
}

// TestE2EManifest drives the Manifest kind against a real cluster: server-side
// apply of two ConfigMaps, Get, prune (re-apply with one → the other is
// deleted), and Delete. Gated on KUBECONFIG_E2E like TestE2EHelmRelease.
func TestE2EManifest(t *testing.T) {
	kcPath := os.Getenv("KUBECONFIG_E2E")
	if kcPath == "" {
		t.Skip("set KUBECONFIG_E2E to a kubeconfig path to run the real-cluster e2e")
	}
	kc, err := os.ReadFile(kcPath)
	if err != nil {
		t.Fatalf("read kubeconfig: %v", err)
	}
	ctx := context.Background()
	p := New()

	cm := func(name, val string) string {
		return "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + name +
			"\n  namespace: default\ndata:\n  k: \"" + val + "\"\n"
	}
	m := &protocol.Resource{APIVersion: apiVersion, Kind: kindManifest}
	m.Metadata.Name = "glue"
	m.Spec = map[string]any{
		"kubeconfig": string(kc),
		"manifest":   cm("openctl-a", "1") + "---\n" + cm("openctl-b", "2"),
	}

	ar, err := p.Apply(ctx, pluginproto.ApplyParams{Manifest: m})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if ar.Resource.Status["applied"] != 2 {
		t.Fatalf("applied = %v, want 2", ar.Resource.Status["applied"])
	}

	gr, err := p.Get(ctx, pluginproto.GetParams{Kind: kindManifest, Name: "glue", State: ar.State})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if gr.Resource.Status["applied"] != 2 || gr.Resource.Status["phase"] != "Ready" {
		t.Errorf("get status = %v", gr.Resource.Status)
	}

	// Re-apply with only openctl-a → openctl-b is pruned (deleted).
	m.Spec["manifest"] = cm("openctl-a", "1")
	ar2, err := p.Apply(ctx, pluginproto.ApplyParams{Manifest: m, State: ar.State})
	if err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if ar2.Resource.Status["applied"] != 1 {
		t.Errorf("applied after prune = %v, want 1", ar2.Resource.Status["applied"])
	}
	// Confirm openctl-b is actually gone from the cluster.
	client, _ := newKubeClient(kc)
	bRef := objectRef{Version: "v1", Resource: "configmaps", Kind: "ConfigMap", Namespace: "default", Name: "openctl-b"}
	if _, err := client.get(ctx, bRef); err == nil {
		t.Error("openctl-b should have been pruned")
	}

	// Delete removes the rest.
	if err := p.Delete(ctx, pluginproto.DeleteParams{Kind: kindManifest, State: ar2.State}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	aRef := objectRef{Version: "v1", Resource: "configmaps", Kind: "ConfigMap", Namespace: "default", Name: "openctl-a"}
	if _, err := client.get(ctx, aRef); err == nil {
		t.Error("openctl-a should have been deleted")
	}
	if _, err := p.Get(ctx, pluginproto.GetParams{Kind: kindManifest, Name: "glue", State: ar2.State}); err == nil {
		t.Error("expected NotFound after delete")
	}
}

// TestE2EPlatform drives the opt-in Platform composite against a real cluster:
// enable the Traefik component (installs a real Helm release), Get, then prune
// it (re-apply with it disabled → uninstalled), then Delete. Gated on
// KUBECONFIG_E2E. Uses Traefik only (a reliable public chart); cloudflared needs
// a real Cloudflare tunnel token and is covered by unit tests.
func TestE2EPlatform(t *testing.T) {
	kcPath := os.Getenv("KUBECONFIG_E2E")
	if kcPath == "" {
		t.Skip("set KUBECONFIG_E2E to a kubeconfig path to run the real-cluster e2e")
	}
	kc, err := os.ReadFile(kcPath)
	if err != nil {
		t.Fatalf("read kubeconfig: %v", err)
	}
	ctx := context.Background()
	p := New()

	m := &protocol.Resource{APIVersion: apiVersion, Kind: kindPlatform}
	m.Metadata.Name = "edge"
	m.Spec = map[string]any{
		"kubeconfig": string(kc),
		"traefik": map[string]any{
			"enabled":   true,
			"namespace": "traefik-e2e",
			// Keep it light on k3d: no LB, single replica.
			"values": map[string]any{
				"service":      map[string]any{"type": "ClusterIP"},
				"deployment":   map[string]any{"replicas": 1},
				"ingressClass": map[string]any{"enabled": false}, // k3d's built-in Traefik owns it
			},
		},
	}

	ar, err := p.Apply(ctx, pluginproto.ApplyParams{Manifest: m})
	if err != nil {
		t.Fatalf("apply platform: %v", err)
	}
	if ar.Resource.Status["enabled"] != 1 {
		t.Fatalf("enabled = %v, want 1", ar.Resource.Status["enabled"])
	}
	if _, leaked := ar.Resource.Spec["kubeconfig"]; leaked {
		t.Error("platform observed spec leaked kubeconfig")
	}

	gr, err := p.Get(ctx, pluginproto.GetParams{Kind: kindPlatform, Name: "edge", State: ar.State})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if gr.Resource.Status["phase"] != "Ready" {
		t.Errorf("get phase = %v", gr.Resource.Status["phase"])
	}

	// Disable traefik → it is pruned (uninstalled).
	m.Spec["traefik"] = map[string]any{"enabled": false}
	ar2, err := p.Apply(ctx, pluginproto.ApplyParams{Manifest: m, State: ar.State})
	if err != nil {
		t.Fatalf("re-apply (disable): %v", err)
	}
	if ar2.Resource.Status["enabled"] != 0 {
		t.Errorf("enabled after disable = %v, want 0", ar2.Resource.Status["enabled"])
	}
	// The traefik release must be gone.
	cfg, _, cleanup, _ := newActionConfig(kc, "traefik-e2e")
	if _, err := getRelease(cfg, "traefik"); err == nil {
		t.Error("traefik release should have been pruned")
	}
	cleanup()

	// Get is now NotFound (no components), and Delete is a no-op-safe teardown.
	if _, err := p.Get(ctx, pluginproto.GetParams{Kind: kindPlatform, Name: "edge", State: ar2.State}); err == nil {
		t.Error("expected NotFound after all components disabled")
	}
	if err := p.Delete(ctx, pluginproto.DeleteParams{Kind: kindPlatform, State: ar.State}); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

// TestE2EArgoApplications aggregates Argo CD Applications without a full argo-cd
// install: it registers the Application CRD + a sample Application CR (with
// health/sync in status), then reads them back through the provider. Gated on
// KUBECONFIG_E2E.
func TestE2EArgoApplications(t *testing.T) {
	kcPath := os.Getenv("KUBECONFIG_E2E")
	if kcPath == "" {
		t.Skip("set KUBECONFIG_E2E to a kubeconfig path to run the real-cluster e2e")
	}
	kc, err := os.ReadFile(kcPath)
	if err != nil {
		t.Fatalf("read kubeconfig: %v", err)
	}
	ctx := context.Background()

	// A minimal, no-status-subresource Application CRD so we can set status on
	// create; plus the argocd namespace.
	crd := `
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: applications.argoproj.io
spec:
  group: argoproj.io
  scope: Namespaced
  names: { plural: applications, singular: application, kind: Application }
  versions:
    - name: v1alpha1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          x-kubernetes-preserve-unknown-fields: true
---
apiVersion: v1
kind: Namespace
metadata: { name: argocd }
`
	client, err := newKubeClient(kc)
	if err != nil {
		t.Fatalf("kube client: %v", err)
	}
	objs, _ := parseObjects(crd)
	for _, o := range objs {
		if _, err := client.apply(ctx, o); err != nil {
			t.Fatalf("apply CRD/ns: %v", err)
		}
	}

	// The API server registers the CRD endpoint asynchronously; retry the read.
	p := New()
	m := &protocol.Resource{APIVersion: apiVersion, Kind: kindArgoApplications}
	m.Metadata.Name = "edge-apps"
	m.Spec = map[string]any{"kubeconfig": string(kc), "namespace": "argocd"}

	var ar *pluginproto.ApplyResult
	for range 20 {
		ar, err = p.Apply(ctx, pluginproto.ApplyParams{Manifest: m})
		if err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("apply ArgoApplications (CRD never became servable): %v", err)
	}

	// Register a sample Application with health/sync, then read it back.
	app := `
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata: { name: sample, namespace: argocd }
status:
  health: { status: Healthy }
  sync: { status: Synced }
`
	appObjs, _ := parseObjects(app)
	// Fresh client: the earlier one's RESTMapper predates the Application CRD.
	client2, err := newKubeClient(kc)
	if err != nil {
		t.Fatalf("kube client: %v", err)
	}
	if _, err := client2.apply(ctx, appObjs[0]); err != nil {
		t.Fatalf("apply Application: %v", err)
	}

	sampleRef := objectRef{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications", Kind: "Application", Namespace: "argocd", Name: "sample"}
	t.Cleanup(func() { _ = client2.delete(ctx, sampleRef) })

	gr, err := p.Get(ctx, pluginproto.GetParams{Kind: kindArgoApplications, Name: "edge-apps", State: ar.State})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// The sample must appear with its health/sync (other leftovers are tolerated).
	apps, _ := gr.Resource.Status["applications"].([]any)
	var found map[string]any
	for _, a := range apps {
		if m, ok := a.(map[string]any); ok && m["name"] == "sample" {
			found = m
		}
	}
	if found == nil {
		t.Fatalf("sample Application not aggregated: %v", gr.Resource.Status["applications"])
	}
	if found["health"] != "Healthy" || found["sync"] != "Synced" {
		t.Errorf("sample = %v, want Healthy/Synced", found)
	}

	// Delete is a no-op (read-only view resource).
	if err := p.Delete(ctx, pluginproto.DeleteParams{Kind: kindArgoApplications, State: ar.State}); err != nil {
		t.Errorf("delete should be a no-op, got %v", err)
	}
}
