package server

import (
	"context"
	"testing"

	"github.com/openctl/openctl/internal/config"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

// sandboxHome points config.Load/Save at a temp HOME so tests never touch the
// developer's real ~/.openctl/config.yaml.
func sandboxHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestGetControllerConfigReturnsDefaultsWhenUnset(t *testing.T) {
	sandboxHome(t)
	h := newConfigHandler()

	resp, err := h.GetControllerConfig(context.Background(), &apiv1.GetControllerConfigRequest{})
	if err != nil {
		t.Fatalf("GetControllerConfig: %v", err)
	}
	if !resp.GetRestartRequired() {
		t.Error("restart_required should be true")
	}
	c := resp.GetConfig()
	if !c.GetReconcilerEnabled() {
		t.Error("reconciler should default to enabled")
	}
	if c.GetReconcilerInterval() != "5m" {
		t.Errorf("reconciler interval default = %q, want 5m", c.GetReconcilerInterval())
	}
	if c.GetOpRetainPerResource() != int32(config.DefaultRetainPerResource) {
		t.Errorf("retain default = %d, want %d", c.GetOpRetainPerResource(), config.DefaultRetainPerResource)
	}
}

func TestUpdateControllerConfigPersistsAndRoundTrips(t *testing.T) {
	sandboxHome(t)
	h := newConfigHandler()

	disabled := false
	_, err := h.UpdateControllerConfig(context.Background(), &apiv1.UpdateControllerConfigRequest{
		Config: &apiv1.ControllerConfig{
			ReconcilerEnabled:   disabled,
			ReconcilerInterval:  "90s",
			OpRetainPerResource: 200,
		},
	})
	if err != nil {
		t.Fatalf("UpdateControllerConfig: %v", err)
	}

	// Re-read through a fresh Get to prove it hit disk.
	resp, err := h.GetControllerConfig(context.Background(), &apiv1.GetControllerConfigRequest{})
	if err != nil {
		t.Fatalf("GetControllerConfig: %v", err)
	}
	c := resp.GetConfig()
	if c.GetReconcilerEnabled() {
		t.Error("reconciler should be disabled after update")
	}
	if c.GetReconcilerInterval() != "90s" {
		t.Errorf("interval = %q, want 90s", c.GetReconcilerInterval())
	}
	if c.GetOpRetainPerResource() != 200 {
		t.Errorf("retain = %d, want 200", c.GetOpRetainPerResource())
	}

	// The on-disk config must round-trip through config.Load with the same
	// values, and the persisted Reconciler.Enabled pointer must be non-nil.
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.Reconciler == nil || cfg.Reconciler.Enabled == nil || *cfg.Reconciler.Enabled {
		t.Errorf("persisted reconciler.enabled = %+v, want explicit false", cfg.Reconciler)
	}
	if cfg.Operations == nil || cfg.Operations.RetainPerResource != 200 {
		t.Errorf("persisted operations = %+v, want retainPerResource 200", cfg.Operations)
	}
}

func TestUpdateControllerConfigZeroRetentionDropsBlock(t *testing.T) {
	sandboxHome(t)
	h := newConfigHandler()

	// Seed a non-default retention, then clear it back to 0 (= use default).
	if _, err := h.UpdateControllerConfig(context.Background(), &apiv1.UpdateControllerConfigRequest{
		Config: &apiv1.ControllerConfig{ReconcilerEnabled: true, OpRetainPerResource: 300},
	}); err != nil {
		t.Fatalf("seed update: %v", err)
	}
	if _, err := h.UpdateControllerConfig(context.Background(), &apiv1.UpdateControllerConfigRequest{
		Config: &apiv1.ControllerConfig{ReconcilerEnabled: true, OpRetainPerResource: 0},
	}); err != nil {
		t.Fatalf("clear update: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.Operations != nil {
		t.Errorf("operations block should be dropped for retain=0, got %+v", cfg.Operations)
	}
	// Get should report the default retention again.
	resp, _ := h.GetControllerConfig(context.Background(), &apiv1.GetControllerConfigRequest{})
	if resp.GetConfig().GetOpRetainPerResource() != int32(config.DefaultRetainPerResource) {
		t.Errorf("retain = %d, want default %d", resp.GetConfig().GetOpRetainPerResource(), config.DefaultRetainPerResource)
	}
}

func TestUpdateControllerConfigRejectsBadInput(t *testing.T) {
	sandboxHome(t)
	h := newConfigHandler()

	if _, err := h.UpdateControllerConfig(context.Background(), &apiv1.UpdateControllerConfigRequest{
		Config: &apiv1.ControllerConfig{ReconcilerInterval: "not-a-duration"},
	}); err == nil {
		t.Error("expected error for invalid interval")
	}
	if _, err := h.UpdateControllerConfig(context.Background(), &apiv1.UpdateControllerConfigRequest{
		Config: &apiv1.ControllerConfig{OpRetainPerResource: -1},
	}); err == nil {
		t.Error("expected error for negative retention")
	}
	if _, err := h.UpdateControllerConfig(context.Background(), &apiv1.UpdateControllerConfigRequest{}); err == nil {
		t.Error("expected error for nil config")
	}
}

// UpdateControllerConfig must not clobber unrelated config (providers etc).
func TestUpdateControllerConfigPreservesOtherBlocks(t *testing.T) {
	sandboxHome(t)
	h := newConfigHandler()

	// Write a provider first, then update controller config.
	if _, err := h.UpsertProvider(context.Background(), &apiv1.UpsertProviderRequest{
		Name: "proxmox", Endpoint: "https://pve.local:8006", TokenId: "root@pam!ui", TokenSecret: "s3cret",
	}); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	if _, err := h.UpdateControllerConfig(context.Background(), &apiv1.UpdateControllerConfigRequest{
		Config: &apiv1.ControllerConfig{ReconcilerEnabled: true, ReconcilerInterval: "10m"},
	}); err != nil {
		t.Fatalf("UpdateControllerConfig: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if _, ok := cfg.Providers["proxmox"]; !ok {
		t.Error("provider block was clobbered by controller-config update")
	}
}
