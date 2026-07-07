package schema

import (
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

func vmWithPassword(pw any) *protocol.Resource {
	return &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "VirtualMachine",
		Metadata:   protocol.ResourceMetadata{Name: "db-01"},
		Spec: map[string]any{
			"node":     "pve1",
			"template": map[string]any{"name": "ubuntu-22.04-cloudinit"},
			"cpu":      map[string]any{"cores": 2},
			"memory":   map[string]any{"size": 2048},
			"cloudInit": map[string]any{
				"user":     "ubuntu",
				"password": pw,
			},
		},
	}
}

// A $secret marker with the file sugar validates against the VM schema.
func TestValidate_SecretFileSugar(t *testing.T) {
	r := vmWithPassword(map[string]any{"$secret": map[string]any{"file": "db-01.pw"}})
	if err := Validate(r); err != nil {
		t.Errorf("want nil, got: %v", err)
	}
}

// The env sugar validates too.
func TestValidate_SecretEnvSugar(t *testing.T) {
	r := vmWithPassword(map[string]any{"$secret": map[string]any{"env": "DB01_PASSWORD"}})
	if err := Validate(r); err != nil {
		t.Errorf("want nil, got: %v", err)
	}
}

// The canonical {provider, key} form validates.
func TestValidate_SecretCanonical(t *testing.T) {
	r := vmWithPassword(map[string]any{"$secret": map[string]any{"provider": "vault", "key": "secret/data/db#pw"}})
	if err := Validate(r); err != nil {
		t.Errorf("want nil, got: %v", err)
	}
}

// Back-compat: a bare plaintext password still validates.
func TestValidate_PlaintextPasswordStillValid(t *testing.T) {
	r := vmWithPassword("hunter2")
	if err := Validate(r); err != nil {
		t.Errorf("want nil, got: %v", err)
	}
}
