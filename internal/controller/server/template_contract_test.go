package server

import (
	"context"
	"reflect"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/openctl/openctl/internal/templates"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

func TestComposeStarterTransportPreservesReferenceAndSSHPaths(t *testing.T) {
	params, err := structpb.NewStruct(map[string]any{
		"name": "welcome", "vmName": "docker-host",
		"privateKeyFile": "/controller/ssh/private key", "knownHostsFile": "/controller/ssh/verified_hosts",
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := newTemplateHandler(templates.Default()).RenderTemplate(context.Background(), &apiv1.RenderTemplateRequest{
		Name: "compose-application", Params: params,
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := rendered.Resource.Spec.AsMap()
	target := spec["target"].(map[string]any)
	want := map[string]any{
		"host": map[string]any{"$ref": map[string]any{
			"apiVersion": "proxmox.openctl.io/v1", "kind": "VirtualMachine", "name": "docker-host", "field": "status.ip",
		}},
		"user": "ubuntu", "sudo": true,
		"privateKeyFile": "/controller/ssh/private key", "knownHostsFile": "/controller/ssh/verified_hosts",
	}
	if !reflect.DeepEqual(target, want) {
		t.Fatalf("target markers or SSH paths changed: %#v", target)
	}
	validated, err := newSchemaHandler(nil).Validate(context.Background(), &apiv1.ValidateRequest{Resource: rendered.Resource})
	if err != nil {
		t.Fatal(err)
	}
	if len(validated.Errors) != 0 {
		t.Fatalf("unresolved VM reference and absent dotenv must be schema-valid: %v", validated.Errors)
	}
}

func TestDockerStarterTransportRejectsFractionalSizing(t *testing.T) {
	params, err := structpb.NewStruct(map[string]any{
		"name": "docker-host", "context": "lab", "node": "pve1",
		"templateVMID": 9000.0, "sshKey": "ssh-ed25519 AAAA test", "cpuCores": 1.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = newTemplateHandler(templates.Default()).RenderTemplate(context.Background(), &apiv1.RenderTemplateRequest{
		Name: "docker-host-vm", Params: params,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("fractional wire input must fail before preview/apply, got %v", err)
	}
}
