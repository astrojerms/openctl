package cli

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openctl/openctl/internal/config"
	controllertls "github.com/openctl/openctl/internal/controller/tls"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

type requestRecoveryRPC struct {
	apiv1.UnimplementedPingServiceServer
	apiv1.UnimplementedResourceServiceServer
	apiv1.UnimplementedOperationServiceServer
	supported       bool
	loseAck         bool
	omitAckIdentity bool
	omitOperationID bool
	applyCalls      atomic.Int32
	accepted        atomic.Pointer[apiv1.Operation]
}

func (s *requestRecoveryRPC) Ping(context.Context, *apiv1.PingRequest) (*apiv1.PingResponse, error) {
	return &apiv1.PingResponse{SupportsIdempotentApply: s.supported}, nil
}

func (s *requestRecoveryRPC) Apply(_ context.Context, req *apiv1.ApplyRequest) (*apiv1.ApplyResponse, error) {
	s.applyCalls.Add(1)
	op := &apiv1.Operation{Id: "op-accepted-before-disconnect", RequestId: req.GetRequestId(), Type: "apply", Status: "pending"}
	s.accepted.Store(op)
	if s.loseAck {
		return nil, status.Error(codes.Unavailable, "connection lost after acceptance")
	}
	response := &apiv1.ApplyResponse{OperationId: op.Id, RequestId: op.RequestId}
	if s.omitAckIdentity {
		response.RequestId = ""
	}
	if s.omitOperationID {
		response.OperationId = ""
	}
	return response, nil
}

func (s *requestRecoveryRPC) GetOperationByRequest(_ context.Context, req *apiv1.GetOperationByRequestRequest) (*apiv1.Operation, error) {
	op := s.accepted.Load()
	if op == nil || op.RequestId != req.GetRequestId() {
		return nil, status.Error(codes.NotFound, "request not accepted")
	}
	return op, nil
}

func configureRequestRecoveryRPC(t *testing.T, handler *requestRecoveryRPC) {
	t.Helper()
	configureControllerRPC(t, func(server *grpc.Server) {
		apiv1.RegisterPingServiceServer(server, handler)
		apiv1.RegisterResourceServiceServer(server, handler)
		apiv1.RegisterOperationServiceServer(server, handler)
	})
}

func configureControllerRPC(t *testing.T, register func(*grpc.Server)) {
	t.Helper()
	state := t.TempDir()
	material, err := controllertls.EnsureMaterial(filepath.Join(state, "tls"), "localhost", []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(state, "token")
	if err := os.WriteFile(token, []byte("fixture-token"), 0600); err != nil {
		t.Fatal(err)
	}
	cert, err := tls.LoadX509KeyPair(material.ServerCertPath, material.ServerKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}})))
	register(server)
	t.Cleanup(server.Stop)
	go func() { _ = server.Serve(listener) }()
	previous := globalConfig
	t.Cleanup(func() { globalConfig = previous })
	globalConfig = &config.Config{Controller: &config.Controller{URL: listener.Addr().String(), TokenFile: token, CAFile: material.CACertPath}}
}

func captureRequestRecoveryOutput(t *testing.T, run func() error) (string, string, error) {
	t.Helper()
	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	previousOut, previousErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdout, stderr
	defer func() { os.Stdout, os.Stderr = previousOut, previousErr }()
	runErr := run()
	out, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	diagnostic, err := os.ReadFile(stderr.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(out), string(diagnostic), runErr
}

func TestCtlApplyRefusesControllerWithoutDurableRecovery(t *testing.T) {
	handler := &requestRecoveryRPC{}
	configureRequestRecoveryRPC(t, handler)
	_, _, err := captureRequestRecoveryOutput(t, func() error {
		return ctlApply(t.Context(), &apiv1.ApplyRequest{Resource: &apiv1.Resource{}}, false, 0)
	})
	if err == nil || !strings.Contains(err.Error(), "upgrade") {
		t.Fatalf("old controller was not rejected: %v", err)
	}
	if handler.applyCalls.Load() != 0 {
		t.Fatal("sent an unsafe mutation before checking recovery support")
	}
}

func TestCtlLostAcknowledgementCanBeRecoveredWithoutAnotherApply(t *testing.T) {
	handler := &requestRecoveryRPC{supported: true, loseAck: true}
	configureRequestRecoveryRPC(t, handler)
	_, diagnostic, err := captureRequestRecoveryOutput(t, func() error {
		return ctlApply(t.Context(), &apiv1.ApplyRequest{Resource: &apiv1.Resource{}}, false, 0)
	})
	if err == nil {
		t.Fatal("lost acknowledgement was reported as success")
	}
	accepted := handler.accepted.Load()
	if accepted == nil || accepted.RequestId == "" || !strings.Contains(diagnostic, accepted.RequestId) || !strings.Contains(err.Error(), accepted.RequestId) {
		t.Fatalf("operator cannot recover the accepted request after disconnect: %v; %s", err, diagnostic)
	}
	out, _, err := captureRequestRecoveryOutput(t, func() error {
		cmd := newCtlOpGetCommand()
		cmd.SetArgs([]string{"--request-id", accepted.RequestId})
		cmd.SetContext(t.Context())
		return cmd.Execute()
	})
	if err != nil {
		t.Fatal(err)
	}
	var recovered apiv1.Operation
	if err := json.Unmarshal([]byte(out), &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.GetId() != accepted.GetId() || recovered.GetStatus() != "pending" || handler.applyCalls.Load() != 1 {
		t.Fatalf("request lookup did not recover the original work without resubmission: %s", out)
	}
}

func TestCtlApplyDoesNotClaimUnacknowledgedRequestSafety(t *testing.T) {
	handler := &requestRecoveryRPC{supported: true, omitAckIdentity: true}
	configureRequestRecoveryRPC(t, handler)
	_, _, err := captureRequestRecoveryOutput(t, func() error {
		return ctlApply(t.Context(), &apiv1.ApplyRequest{Resource: &apiv1.Resource{}, RequestId: "operator-reviewed-attempt"}, false, 0)
	})
	if err == nil || !strings.Contains(err.Error(), "op-accepted-before-disconnect") || !strings.Contains(err.Error(), "may already be running") {
		t.Fatalf("incompatible acknowledgement hid potentially running work: %v", err)
	}
}

func TestCtlApplyNeverReportsSuccessWithoutRecoverableOperationIdentity(t *testing.T) {
	handler := &requestRecoveryRPC{supported: true, omitOperationID: true}
	configureRequestRecoveryRPC(t, handler)
	out, _, err := captureRequestRecoveryOutput(t, func() error {
		return ctlApply(t.Context(), &apiv1.ApplyRequest{Resource: &apiv1.Resource{}, RequestId: "recover-without-operation-ack"}, false, 0)
	})
	if err == nil || out != "" || !strings.Contains(err.Error(), "recover-without-operation-ack") {
		t.Fatalf("lost operation identity was reported as successful submission: %q; %v", out, err)
	}
}
