package provider

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

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
		name  string
		chart map[string]any
	}{
		{"http", map[string]any{
			"repo": "https://stefanprodan.github.io/podinfo", "name": "podinfo", "version": "6.7.0",
		}},
		{"oci", map[string]any{
			"repo": "oci://ghcr.io/stefanprodan/charts", "name": "podinfo", "version": "6.7.0",
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			p := New()
			relName := "podinfo-" + tc.name
			m := &protocol.Resource{APIVersion: apiVersion, Kind: kindHelmRelease}
			m.Metadata.Name = relName
			m.Spec = map[string]any{
				"kubeconfig":      string(kc),
				"namespace":       "openctl-e2e-" + tc.name,
				"createNamespace": true,
				"chart":           tc.chart,
				"wait":            true,
				"timeout":         "3m",
				"values":          map[string]any{"replicaCount": 1.0},
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
