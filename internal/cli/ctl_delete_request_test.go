package cli

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

type deleteRecoveryRPC struct {
	apiv1.UnimplementedPingServiceServer
	apiv1.UnimplementedResourceServiceServer
	apiv1.UnimplementedOperationServiceServer
	supported     bool
	loseAck       bool
	ack           string
	omitOperation bool
	calls         atomic.Int32
	polls         atomic.Int32
	accepted      atomic.Pointer[apiv1.Operation]
}

func (s *deleteRecoveryRPC) Ping(context.Context, *apiv1.PingRequest) (*apiv1.PingResponse, error) {
	return &apiv1.PingResponse{SupportsIdempotentDelete: s.supported}, nil
}

func (s *deleteRecoveryRPC) Delete(_ context.Context, req *apiv1.DeleteRequest) (*apiv1.DeleteResponse, error) {
	s.calls.Add(1)
	op := &apiv1.Operation{Id: "op-delete-accepted", RequestId: req.GetRequestId(), Type: "delete", Status: "pending"}
	s.accepted.Store(op)
	if s.loseAck {
		return nil, status.Error(codes.Unavailable, "lost acknowledgement after acceptance")
	}
	ack := req.GetRequestId()
	if s.ack != "" {
		ack = s.ack
		if ack == "missing" {
			ack = ""
		}
	}
	id := op.Id
	if s.omitOperation {
		id = ""
	}
	return &apiv1.DeleteResponse{RequestId: ack, OperationId: id}, nil
}

func (s *deleteRecoveryRPC) GetOperationByRequest(_ context.Context, req *apiv1.GetOperationByRequestRequest) (*apiv1.Operation, error) {
	op := s.accepted.Load()
	if op == nil || op.RequestId != req.GetRequestId() {
		return nil, status.Error(codes.NotFound, "not accepted")
	}
	return op, nil
}

func (s *deleteRecoveryRPC) GetOperation(context.Context, *apiv1.GetOperationRequest) (*apiv1.Operation, error) {
	s.polls.Add(1)
	return nil, status.Error(codes.Unavailable, "poll unavailable")
}

func configureDeleteRecoveryRPC(t *testing.T, handler *deleteRecoveryRPC) {
	t.Helper()
	configureControllerRPC(t, func(server *grpc.Server) {
		apiv1.RegisterPingServiceServer(server, handler)
		apiv1.RegisterResourceServiceServer(server, handler)
		apiv1.RegisterOperationServiceServer(server, handler)
	})
}

func TestCtlDeleteLostAcknowledgementRecoveryDoesNotResubmit(t *testing.T) {
	handler := &deleteRecoveryRPC{supported: true, loseAck: true}
	configureDeleteRecoveryRPC(t, handler)
	_, diagnostic, err := captureRequestRecoveryOutput(t, func() error {
		return ctlDelete(t.Context(), &apiv1.DeleteRequest{ApiVersion: "test/v1", Kind: "VM", Name: "old"}, false, 0)
	})
	op := handler.accepted.Load()
	if op == nil {
		t.Fatal("delete not accepted")
	}
	if _, parseErr := uuid.Parse(op.RequestId); parseErr != nil {
		t.Fatalf("generated request identity is not a UUID: %v", parseErr)
	}
	if err == nil || !strings.Contains(diagnostic, op.RequestId) || !strings.Contains(err.Error(), op.RequestId) || !strings.Contains(err.Error(), "--request-id") {
		t.Fatalf("lost acknowledgement is not recoverable: %v; %s", err, diagnostic)
	}
	out, _, err := captureRequestRecoveryOutput(t, func() error {
		cmd := newCtlOpGetCommand()
		cmd.SetArgs([]string{"--request-id", op.RequestId})
		cmd.SetContext(t.Context())
		return cmd.Execute()
	})
	var recovered apiv1.Operation
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.Id != op.Id || recovered.RequestId != op.RequestId || handler.calls.Load() != 1 {
		t.Fatalf("lookup did not recover original deletion without resubmission: %s", out)
	}
}

func TestCtlDeleteRefusesUnsupportedAndInvalidRequests(t *testing.T) {
	for _, tc := range []struct {
		name, id  string
		supported bool
	}{
		{"unsupported", "explicit-delete", false},
		{"control-character", "bad\nkey", true},
		{"overlong", strings.Repeat("a", 129), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := &deleteRecoveryRPC{supported: tc.supported}
			configureDeleteRecoveryRPC(t, handler)
			_, _, err := captureRequestRecoveryOutput(t, func() error {
				return ctlDelete(t.Context(), &apiv1.DeleteRequest{RequestId: tc.id}, false, 0)
			})
			if err == nil || handler.calls.Load() != 0 {
				t.Fatalf("unsafe submission: calls=%d err=%v", handler.calls.Load(), err)
			}
		})
	}
}

func TestCtlDeleteRejectsUnrecoverableAcknowledgements(t *testing.T) {
	for _, tc := range []struct {
		name, ack     string
		omitOperation bool
	}{
		{"missing-request", "missing", false},
		{"different-request", "different", false},
		{"missing-operation", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := &deleteRecoveryRPC{supported: true, ack: tc.ack, omitOperation: tc.omitOperation}
			configureDeleteRecoveryRPC(t, handler)
			out, _, err := captureRequestRecoveryOutput(t, func() error {
				return ctlDelete(t.Context(), &apiv1.DeleteRequest{RequestId: "original-delete"}, true, time.Second)
			})
			if err == nil || !strings.Contains(err.Error(), "original-delete") || out != "" || handler.calls.Load() != 1 || handler.polls.Load() != 0 {
				t.Fatalf("bad acknowledgement treated as accepted: out=%q err=%v calls=%d polls=%d", out, err, handler.calls.Load(), handler.polls.Load())
			}
		})
	}
}

func TestCtlDeleteCommandNoWaitPreservesExplicitIdentity(t *testing.T) {
	handler := &deleteRecoveryRPC{supported: true}
	configureDeleteRecoveryRPC(t, handler)
	out, diagnostic, err := captureRequestRecoveryOutput(t, func() error {
		cmd := newCtlDeleteCommand()
		cmd.SetArgs([]string{"VM", "old", "--api-version", "test/v1", "--request-id", "explicit-delete", "--no-wait", "--wait-timeout", "1s"})
		cmd.SetContext(t.Context())
		return cmd.Execute()
	})
	if err != nil || out != "op-delete-accepted\n" || !strings.Contains(diagnostic, "Delete request: explicit-delete") || handler.accepted.Load().RequestId != "explicit-delete" || handler.polls.Load() != 0 {
		t.Fatalf("no-wait result not recoverable: out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
}

func TestCtlDeleteWaitFailurePreservesRecoveryIdentity(t *testing.T) {
	handler := &deleteRecoveryRPC{supported: true}
	configureDeleteRecoveryRPC(t, handler)
	_, _, err := captureRequestRecoveryOutput(t, func() error {
		return ctlDelete(t.Context(), &apiv1.DeleteRequest{RequestId: "waiting-delete"}, true, time.Second)
	})
	if err == nil || !strings.Contains(err.Error(), "poll unavailable") || !strings.Contains(err.Error(), "waiting-delete") || handler.calls.Load() != 1 || handler.polls.Load() != 1 {
		t.Fatalf("wait failure lost recovery instruction: %v", err)
	}
}
