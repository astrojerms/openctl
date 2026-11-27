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

func TestCtlPreviewReferenceFailuresRemainActionable(t *testing.T) {
	for _, field := range []string{"status.ip", ""} {
		for _, format := range []string{"table", "json"} {
			t.Run(format+"/"+field, func(t *testing.T) {
				code := "reference_field_unavailable"
				if field == "" {
					code = "reference_read_failed"
				}
				out, err := runPreviewTest(t, t.Context(), &apiv1.DryRunApplyResponse{
					FieldErrors: []*apiv1.FieldError{{
						Path: "spec.target.host", Code: code,
						Message:        "Referenced output is unavailable. Inspect the declared dependency.",
						Reference:      &apiv1.ResourceRef{ApiVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine", Name: "declared-vm"},
						ReferenceField: field,
					}},
				}, nil, format)
				if err == nil {
					t.Fatal("field-only invalid preview succeeded")
				}
				if format == "table" {
					requested := field
					if requested == "" {
						requested = "(whole resource)"
					}
					for _, want := range []string{
						"Validation error at spec.target.host:",
						"Referenced resource: proxmox.openctl.io/v1 VirtualMachine/declared-vm",
						"requested output: " + requested,
					} {
						if !strings.Contains(out, want) {
							t.Fatalf("actionable reference diagnostic lost %q: %s", want, out)
						}
					}
				} else {
					var response struct {
						FieldErrors []struct {
							Path      string `json:"path"`
							Code      string `json:"code"`
							Reference struct {
								APIVersion string `json:"apiVersion"`
								Kind       string `json:"kind"`
								Name       string `json:"name"`
							} `json:"reference"`
							ReferenceField string `json:"referenceField"`
						} `json:"fieldErrors"`
					}
					if err := json.Unmarshal([]byte(out), &response); err != nil {
						t.Fatalf("invalid preview lost usable JSON: %v", err)
					}
					if len(response.FieldErrors) != 1 {
						t.Fatalf("field diagnostic lost: %s", out)
					}
					fe := response.FieldErrors[0]
					if fe.Path != "spec.target.host" || fe.Code != code || fe.Reference.APIVersion != "proxmox.openctl.io/v1" || fe.Reference.Kind != "VirtualMachine" || fe.Reference.Name != "declared-vm" || fe.ReferenceField != field {
						t.Fatalf("structured reference target lost: %s", out)
					}
				}
				for _, forbidden := range []string{"PRIVATE-TOKEN", "PRIVATE-BODY", "private.invalid", "ctl apply", "ctl delete"} {
					if strings.Contains(out, forbidden) {
						t.Fatalf("preview emitted unsafe content %q", forbidden)
					}
				}
			})
		}
	}
}

func TestCtlPreviewDoesNotInventReferenceTargets(t *testing.T) {
	for _, reference := range []*apiv1.ResourceRef{nil, {Kind: "VirtualMachine", Name: "partial"}} {
		var out bytes.Buffer
		if err := printCtlPreview(&out, &apiv1.DryRunApplyResponse{FieldErrors: []*apiv1.FieldError{{
			Path: "spec.target.host", Code: "reference_invalid",
			Message:   "Reference proxmox.openctl.io/v1 VirtualMachine/declared-vm status.ip is invalid.",
			Reference: reference, ReferenceField: "status.ip",
		}}}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "Validation error at spec.target.host:") || strings.Contains(out.String(), "Referenced resource:") || strings.Contains(out.String(), "requested output:") {
			t.Fatalf("missing metadata was inferred from message or partial target: %s", out.String())
		}
	}
}
