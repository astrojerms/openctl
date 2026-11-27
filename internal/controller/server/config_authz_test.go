package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openctl/openctl/internal/controller/auth"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestConfigurationMutationsDoNotLetNonAdminsRewriteCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	h := newConfigHandler()
	mutations := []struct {
		name string
		call func(context.Context) error
	}{
		{"provider credentials", func(ctx context.Context) error {
			_, err := h.UpsertProvider(ctx, &apiv1.UpsertProviderRequest{Name: "proxmox", Endpoint: "https://untrusted.example.test", TokenId: "fixture", TokenSecret: "fixture"})
			return err
		}},
		{"provider deletion", func(ctx context.Context) error {
			_, err := h.DeleteProvider(ctx, &apiv1.DeleteProviderRequest{Name: "proxmox"})
			return err
		}},
		{"controller policy", func(ctx context.Context) error {
			_, err := h.UpdateControllerConfig(ctx, &apiv1.UpdateControllerConfigRequest{Config: &apiv1.ControllerConfig{ReconcilerEnabled: true, ReconcilerInterval: "5m", OpRetainPerResource: 50}})
			return err
		}},
	}
	for _, role := range []auth.Role{auth.RoleViewer, auth.RoleEditor} {
		for _, mutation := range mutations {
			t.Run(string(role)+"/"+mutation.name, func(t *testing.T) {
				err := mutation.call(ctxWithRole(role))
				if status.Code(err) != codes.PermissionDenied {
					t.Fatalf("non-admin configuration write returned %v", err)
				}
			})
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".openctl", "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("unauthorized calls touched controller configuration: %v", err)
	}
}
