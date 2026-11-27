package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type previewRPC struct {
	apiv1.UnimplementedResourceServiceServer
	response *apiv1.DryRunApplyResponse
	err      error
}

func (s *previewRPC) DryRunApply(ctx context.Context, _ *apiv1.DryRunApplyRequest) (*apiv1.DryRunApplyResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer fixture-token" {
		return nil, status.Error(codes.Unauthenticated, "missing bearer token")
	}
	return s.response, s.err
}

func runPreviewTest(t *testing.T, ctx context.Context, response *apiv1.DryRunApplyResponse, rpcErr error, format string) (string, error) {
	t.Helper()
	configureControllerRPC(t, func(server *grpc.Server) {
		apiv1.RegisterResourceServiceServer(server, &previewRPC{response: response, err: rpcErr})
	})
	previousFormat, previousTimeout := outputFormat, globalTimeout
	t.Cleanup(func() { outputFormat, globalTimeout = previousFormat, previousTimeout })
	outputFormat, globalTimeout = format, 0
	path := filepath.Join(t.TempDir(), "resource.yaml")
	// An unknown local kind must reach the live controller for validation.
	if err := os.WriteFile(path, []byte("apiVersion: example.test/v1\nkind: ControllerOnlyKind\nmetadata:\n  name: preview\nspec:\n  value: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := newCtlPreviewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"-f", path})
	err := cmd.ExecuteContext(ctx)
	return out.String(), err
}

func TestCtlPreviewCreateWithoutDiff(t *testing.T) {
	out, err := runPreviewTest(t, t.Context(), &apiv1.DryRunApplyResponse{Operation: "create", Summary: "would create resource"}, nil, "table")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Operation: create") || strings.Contains(out, "Operation: no-op") {
		t.Fatalf("empty create diff lost operation: %s", out)
	}
}

func TestCtlPreviewMetadataOnlyUpdate(t *testing.T) {
	out, err := runPreviewTest(t, t.Context(), &apiv1.DryRunApplyResponse{
		Operation:    "update",
		MetadataDiff: []*apiv1.DriftEntry{{Path: "labels.team", Desired: "old", Observed: "new"}},
	}, nil, "table")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Operation: update") || !strings.Contains(out, "labels.team: previous old -> desired new") {
		t.Fatalf("metadata update not distinguished from spec: %s", out)
	}
}

func TestCtlPreviewInvalidJSONRemainsUsable(t *testing.T) {
	out, err := runPreviewTest(t, t.Context(), &apiv1.DryRunApplyResponse{ValidationErrors: []string{"spec.value: invalid"}}, nil, "json")
	if err == nil {
		t.Fatal("invalid preview succeeded")
	}
	var response map[string]any
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("diagnostics were not usable JSON: %v: %s", err, out)
	}
	diagnostics, ok := response["validationErrors"].([]any)
	if !ok || len(diagnostics) != 1 || diagnostics[0] != "spec.value: invalid" {
		t.Fatalf("controller diagnostics lost: %s", out)
	}
}

func TestCtlPreviewErrorsCannotReportReadiness(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel bool
		rpcErr error
	}{
		{name: "canceled", cancel: true},
		{name: "rpc error", rpcErr: status.Error(codes.Unavailable, "offline")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			out, err := runPreviewTest(t, ctx, &apiv1.DryRunApplyResponse{Operation: "create", Summary: "ready"}, tc.rpcErr, "table")
			if err == nil || out != "" {
				t.Fatalf("failed preview produced success output: err=%v output=%q", err, out)
			}
			if tc.cancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
}

func TestCtlPreviewRejectsAmbiguousOperation(t *testing.T) {
	for _, operation := range []string{"", "legacy"} {
		t.Run(operation, func(t *testing.T) {
			out, err := runPreviewTest(t, t.Context(), &apiv1.DryRunApplyResponse{Operation: operation}, nil, "table")
			if err == nil || !strings.Contains(out, "Operation: unknown") || strings.Contains(out, "Operation: no-op") {
				t.Fatalf("ambiguous operation accepted: err=%v output=%s", err, out)
			}
		})
	}
}

func TestCtlPreviewDistinguishesStoredEditsFromLiveObservation(t *testing.T) {
	var out bytes.Buffer
	if err := printCtlPreview(&out, &apiv1.DryRunApplyResponse{
		Operation: "update",
		Diff:      []*apiv1.DriftEntry{{Path: "cpu.cores", Desired: "2", Observed: "4"}},
		LiveDrift: []*apiv1.DriftEntry{{Path: "cpu.cores", Desired: "4", Observed: "3"}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"previous 2 -> desired 4", "observed 3 -> desired 4"} {
		if !strings.Contains(out.String(), change) {
			t.Fatalf("preview reversed desired intent or live observation: %s", out.String())
		}
	}
}
