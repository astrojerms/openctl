package cli

import (
	"bytes"
	"context"
	"testing"

	"google.golang.org/grpc"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

type missingOverviewTimeRPC struct {
	apiv1.UnimplementedOverviewServiceServer
}

func (*missingOverviewTimeRPC) GetOverview(context.Context, *apiv1.GetOverviewRequest) (*apiv1.GetOverviewResponse, error) {
	return &apiv1.GetOverviewResponse{}, nil
}

func TestCtlOverviewDoesNotTurnIncompleteResponseIntoEmptyInventory(t *testing.T) {
	configureControllerRPC(t, func(server *grpc.Server) {
		apiv1.RegisterOverviewServiceServer(server, &missingOverviewTimeRPC{})
	})
	previousFormat, previousTimeout := outputFormat, globalTimeout
	t.Cleanup(func() { outputFormat, globalTimeout = previousFormat, previousTimeout })
	outputFormat, globalTimeout = "json", 0
	var out bytes.Buffer
	cmd := newCtlOverviewCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.ExecuteContext(t.Context()); err == nil || out.Len() != 0 {
		t.Fatalf("incomplete snapshot became usable empty inventory: error=%v output=%s", err, out.String())
	}
}
