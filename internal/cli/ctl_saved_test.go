package cli

import (
	"bytes"
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

type savedExportRPC struct {
	apiv1.UnimplementedInventoryServiceServer
	resource *apiv1.Resource
}

func (s *savedExportRPC) GetSavedResource(context.Context, *apiv1.GetRequest) (*apiv1.GetSavedResourceResponse, error) {
	return &apiv1.GetSavedResourceResponse{Resource: s.resource}, nil
}

func TestCtlSavedRefusesIncompleteOrObservedDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resource *apiv1.Resource
	}{
		{name: "no declaration"},
		{name: "wrong identity", resource: &apiv1.Resource{ApiVersion: "example.io/v1", Kind: "Saved", Metadata: &apiv1.Metadata{Name: "someone-else"}}},
		{name: "observed status", resource: &apiv1.Resource{ApiVersion: "example.io/v1", Kind: "Saved", Metadata: &apiv1.Metadata{Name: "sample"}, Status: &structpb.Struct{}}},
		{name: "observed ownership", resource: &apiv1.Resource{ApiVersion: "example.io/v1", Kind: "Saved", Metadata: &apiv1.Metadata{Name: "sample", OwnerRefs: []*apiv1.ResourceRef{{Kind: "Parent", Name: "parent"}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configureControllerRPC(t, func(server *grpc.Server) {
				apiv1.RegisterInventoryServiceServer(server, &savedExportRPC{resource: tc.resource})
			})
			previousFormat, previousTimeout := outputFormat, globalTimeout
			t.Cleanup(func() { outputFormat, globalTimeout = previousFormat, previousTimeout })
			outputFormat, globalTimeout = "yaml", 0
			var out bytes.Buffer
			cmd := newCtlGetCommand()
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			cmd.SetArgs([]string{"Saved", "sample", "--api-version", "example.io/v1", "--saved"})
			if err := cmd.ExecuteContext(t.Context()); err == nil || out.Len() != 0 {
				t.Fatalf("unusable desired export was reported as successful: error=%v output=%s", err, out.String())
			}
		})
	}
}
