package server

import (
	"testing"

	"github.com/openctl/openctl/pkg/protocol"
)

// TestResourceToProtoEncodesTypedContainers pins the regression where a
// ProxmoxNode's status carried a []string (node storages/bridges) that
// structpb.NewStruct rejected with "proto: invalid type: []string", 500ing the
// resource GET. normalize must convert typed slices/maps to the generic
// []any / map[string]any structpb accepts.
func TestResourceToProtoEncodesTypedContainers(t *testing.T) {
	r := &protocol.Resource{
		APIVersion: "proxmox.openctl.io/v1",
		Kind:       "ProxmoxNode",
		Metadata:   protocol.ResourceMetadata{Name: "pve1"},
		Status: map[string]any{
			"storages": []string{"local", "local-lvm"}, // the exact 500 trigger
			"bridges":  []string{"vmbr0"},
			"counts":   []int{1, 2, 3},                       // other typed slice
			"labels":   map[string]string{"role": "compute"}, // typed map
			"nested":   []any{[]string{"a", "b"}},            // typed slice inside []any
			"blob":     []byte("hi"),                         // []byte stays base64 string
		},
	}

	out, err := resourceToProto(r)
	if err != nil {
		t.Fatalf("resourceToProto: %v", err)
	}

	fields := out.GetStatus().GetFields()
	storages := fields["storages"].GetListValue().GetValues()
	if len(storages) != 2 || storages[0].GetStringValue() != "local" || storages[1].GetStringValue() != "local-lvm" {
		t.Errorf("storages = %v, want [local local-lvm]", storages)
	}
	if b := fields["bridges"].GetListValue().GetValues(); len(b) != 1 || b[0].GetStringValue() != "vmbr0" {
		t.Errorf("bridges = %v, want [vmbr0]", b)
	}
	counts := fields["counts"].GetListValue().GetValues()
	if len(counts) != 3 || counts[0].GetNumberValue() != 1 {
		t.Errorf("counts = %v, want numeric [1 2 3]", counts)
	}
	if role := fields["labels"].GetStructValue().GetFields()["role"].GetStringValue(); role != "compute" {
		t.Errorf("labels.role = %q, want compute", role)
	}
	inner := fields["nested"].GetListValue().GetValues()
	if len(inner) != 1 || inner[0].GetListValue().GetValues()[0].GetStringValue() != "a" {
		t.Errorf("nested = %v, want [[a b]]", inner)
	}
	// []byte encodes as a base64 string ("hi" -> "aGk=").
	if blob := fields["blob"].GetStringValue(); blob != "aGk=" {
		t.Errorf("blob = %q, want base64 aGk=", blob)
	}
}
