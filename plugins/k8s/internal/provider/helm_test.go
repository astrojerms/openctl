package provider

import (
	"errors"
	"io"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	kubefake "helm.sh/helm/v3/pkg/kube/fake"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
)

// fakeRelease is a minimal deployed release for pure mapping tests.
func fakeRelease() *release.Release {
	return &release.Release{
		Name:      "x",
		Namespace: "demo",
		Version:   1,
		Info:      &release.Info{Status: release.StatusDeployed},
		Chart:     &chart.Chart{Metadata: &chart.Metadata{Name: "testchart", Version: "0.1.0", AppVersion: "1.0"}},
	}
}

// fakeCfg is an in-process Helm action.Configuration: releases live in a memory
// driver and the kube client is a no-op printer. This exercises the real Helm
// install/upgrade/get/list/uninstall engine — the same code path a live cluster
// drives — without any API server. State persists within one cfg (as the
// in-cluster release secrets would), so a lifecycle test reuses one cfg.
func fakeCfg() *action.Configuration {
	return &action.Configuration{
		Releases:     storage.Init(driver.NewMemory()),
		KubeClient:   &kubefake.PrintingKubeClient{Out: io.Discard},
		Capabilities: chartutil.DefaultCapabilities,
		Log:          func(string, ...any) {},
	}
}

func TestReleaseLifecycle(t *testing.T) {
	cfg := fakeCfg()
	ch, err := loader.LoadDir("testdata/testchart")
	if err != nil {
		t.Fatalf("load chart: %v", err)
	}

	o := releaseOpts{releaseName: "demo", namespace: "default", values: map[string]any{"greeting": "hi"}}

	// Install (release absent -> install path).
	rel, err := installOrUpgrade(cfg, o, ch)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if rel.Version != 1 {
		t.Errorf("install revision = %d, want 1", rel.Version)
	}
	if rel.Info.Status != release.StatusDeployed {
		t.Errorf("install status = %v, want deployed", rel.Info.Status)
	}
	if phaseFor(rel) != "Ready" {
		t.Errorf("phase = %q, want Ready", phaseFor(rel))
	}

	// Get reads it back.
	got, err := getRelease(cfg, "demo")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Version != 1 {
		t.Errorf("get revision = %d, want 1", got.Version)
	}

	// Apply again (release exists -> upgrade path, revision bumps).
	o.values = map[string]any{"greeting": "bye"}
	rel2, err := installOrUpgrade(cfg, o, ch)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if rel2.Version != 2 {
		t.Errorf("upgrade revision = %d, want 2", rel2.Version)
	}

	// List finds it.
	list, err := listReleases(cfg)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %d releases (%v), err %v", len(list), list, err)
	}

	// Uninstall, then Get is NotFound.
	if err := uninstall(cfg, "demo"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := getRelease(cfg, "demo"); !errors.Is(err, driver.ErrReleaseNotFound) {
		t.Errorf("get after uninstall err = %v, want ErrReleaseNotFound", err)
	}
	// Uninstall is idempotent.
	if err := uninstall(cfg, "demo"); err != nil {
		t.Errorf("second uninstall should be nil, got %v", err)
	}
}
