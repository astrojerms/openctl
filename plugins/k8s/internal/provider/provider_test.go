package provider

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/openctl/openctl/pkg/pluginproto"
	"github.com/openctl/openctl/pkg/protocol"
)

func TestHandshake(t *testing.T) {
	hs, err := New().Handshake(context.Background())
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if hs.ProviderName != "k8s" || hs.ProtocolVersion != pluginproto.ProtocolVersion {
		t.Errorf("identity = %q/%q", hs.ProviderName, hs.ProtocolVersion)
	}
	if len(hs.Kinds) != 1 || hs.Kinds[0].Kind != kindHelmRelease || hs.Kinds[0].Schema == "" {
		t.Fatalf("kinds = %+v", hs.Kinds)
	}
	caps := map[string]bool{}
	for _, c := range hs.Capabilities {
		caps[c] = true
	}
	if !caps[pluginproto.CapabilityState] || !caps[pluginproto.CapabilitySchema] {
		t.Errorf("capabilities = %v, want state + schema", hs.Capabilities)
	}
}

func TestParseHelmSpec(t *testing.T) {
	m := &protocol.Resource{Kind: kindHelmRelease}
	m.Metadata.Name = "podinfo"
	m.Spec = map[string]any{
		"kubeconfig": "KCONF",
		"namespace":  "demo",
		"chart":      map[string]any{"repo": "https://example.test/charts", "name": "podinfo", "version": "6.7.0"},
		"values":     map[string]any{"replicaCount": 2.0},
		"wait":       true,
		"timeout":    "3m",
	}
	hs, err := parseHelmSpec(m)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if string(hs.kubeconfig) != "KCONF" || hs.namespace != "demo" {
		t.Errorf("kubeconfig/namespace = %q/%q", hs.kubeconfig, hs.namespace)
	}
	if hs.chart.Name != "podinfo" || hs.chart.Version != "6.7.0" {
		t.Errorf("chart = %+v", hs.chart)
	}
	if hs.opts.releaseName != "podinfo" { // defaults to metadata.name
		t.Errorf("releaseName = %q", hs.opts.releaseName)
	}
	if !hs.opts.wait || hs.opts.timeout != 3*time.Minute {
		t.Errorf("wait/timeout = %v/%v", hs.opts.wait, hs.opts.timeout)
	}
}

func TestParseHelmSpecKubeconfigPath(t *testing.T) {
	// A kubeconfigPath (as $ref would resolve to) is read from disk; the path —
	// not the content — is what gets recorded for Get/Delete re-reads.
	dir := t.TempDir()
	path := dir + "/kubeconfig"
	if err := os.WriteFile(path, []byte("KUBE-CONTENT"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &protocol.Resource{Kind: kindHelmRelease}
	m.Metadata.Name = "app"
	m.Spec = map[string]any{
		"kubeconfigPath": path,
		"chart":          map[string]any{"repo": "https://x/charts", "name": "app"},
	}
	hs, err := parseHelmSpec(m)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if string(hs.kubeconfig) != "KUBE-CONTENT" {
		t.Errorf("kubeconfig content = %q, want file content", hs.kubeconfig)
	}
	if hs.kubeconfigPath != path {
		t.Errorf("kubeconfigPath = %q, want %q", hs.kubeconfigPath, path)
	}

	// releaseState with a path re-loads from the file (not stored bytes).
	st := releaseState{KubeconfigPath: path, ReleaseName: "app"}
	if !st.hasCluster() {
		t.Error("hasCluster() = false, want true for path-based state")
	}
	kc, err := st.loadKubeconfig()
	if err != nil || string(kc) != "KUBE-CONTENT" {
		t.Errorf("loadKubeconfig = %q (err %v)", kc, err)
	}
	// A missing path surfaces an error rather than silently proceeding.
	if _, err := (releaseState{KubeconfigPath: dir + "/gone", ReleaseName: "app"}).loadKubeconfig(); err == nil {
		t.Error("loadKubeconfig on missing path should error")
	}
}

func TestParseHelmSpecErrors(t *testing.T) {
	cases := map[string]map[string]any{
		"no kubeconfig": {"chart": map[string]any{"repo": "r"}},
		"no chart":      {"kubeconfig": "k"},
		"no repo":       {"kubeconfig": "k", "chart": map[string]any{"name": "x"}},
	}
	for name, spec := range cases {
		m := &protocol.Resource{Kind: kindHelmRelease, Spec: spec}
		m.Metadata.Name = "x"
		if _, err := parseHelmSpec(m); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestChartRef(t *testing.T) {
	cases := []struct {
		cs      chartSpec
		ref     string
		httpURL string
	}{
		{chartSpec{Repo: "https://charts.example/x", Name: "podinfo"}, "podinfo", "https://charts.example/x"},
		{chartSpec{Repo: "oci://ghcr.io/org/charts", Name: "podinfo"}, "oci://ghcr.io/org/charts/podinfo", ""},
		{chartSpec{Repo: "oci://ghcr.io/org/charts/podinfo"}, "oci://ghcr.io/org/charts/podinfo", ""},
	}
	for _, c := range cases {
		if got := c.cs.ref(); got != c.ref {
			t.Errorf("ref(%+v) = %q, want %q", c.cs, got, c.ref)
		}
		if got := c.cs.httpRepoURL(); got != c.httpURL {
			t.Errorf("httpRepoURL(%+v) = %q, want %q", c.cs, got, c.httpURL)
		}
	}
}

func TestObservedRedactsKubeconfig(t *testing.T) {
	m := &protocol.Resource{Kind: kindHelmRelease, Spec: map[string]any{"kubeconfig": "SECRET", "namespace": "demo"}}
	m.Metadata.Name = "x"
	// A minimal fake release via the memory-driver install would be heavier; here
	// just check the spec-echo path drops the credential.
	r := observed(m, fakeRelease())
	if _, leaked := r.Spec["kubeconfig"]; leaked {
		t.Error("observed spec leaked kubeconfig")
	}
	if r.Spec["namespace"] != "demo" {
		t.Errorf("observed spec.namespace = %v", r.Spec["namespace"])
	}
	if r.Status["phase"] != "Ready" {
		t.Errorf("status.phase = %v, want Ready", r.Status["phase"])
	}
}
