package cli

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type canceledOperationServer struct {
	apiv1.UnimplementedOperationServiceServer
	calls atomic.Int32
	block bool
}

func (s *canceledOperationServer) GetOperation(ctx context.Context, req *apiv1.GetOperationRequest) (*apiv1.Operation, error) {
	s.calls.Add(1)
	if s.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &apiv1.Operation{Id: req.GetId(), Status: "canceled", Error: "operator canceled the deployment"}, nil
}

func TestWaitForOpReturnsCancellationWithoutPollingUntilTimeout(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	handler := &canceledOperationServer{}
	apiv1.RegisterOperationServiceServer(server, handler)
	t.Cleanup(server.Stop)
	go func() { _ = server.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///operations", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	err = waitForOp(t.Context(), conn, "cancel-me", 100*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "cancel-me canceled") || !strings.Contains(err.Error(), "operator canceled") {
		t.Fatalf("expected terminal cancellation diagnostic, got %v", err)
	}
	if got := handler.calls.Load(); got != 1 {
		t.Fatalf("canceled terminal operation was polled %d times", got)
	}
}

func TestWaitForOpDeadlineBoundsAnInFlightStatusRequest(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	apiv1.RegisterOperationServiceServer(server, &canceledOperationServer{block: true})
	t.Cleanup(server.Stop)
	go func() { _ = server.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///operations", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err = waitForOp(ctx, conn, "blocked", 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not finish within 20ms") {
		t.Fatalf("wait deadline did not bound the status RPC: %v", err)
	}
}
