package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"gopkg.in/yaml.v3"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
)

type healthRPC struct {
	apiv1.UnimplementedResourceServiceServer
	calls atomic.Int32
	get   func(context.Context, *apiv1.GetRequest) (*apiv1.GetResponse, error)
}

func (s *healthRPC) Get(ctx context.Context, r *apiv1.GetRequest) (*apiv1.GetResponse, error) {
	s.calls.Add(1)
	return s.get(ctx, r)
}
func setupHealthRPC(t *testing.T, handler *healthRPC) {
	t.Helper()
	configureControllerRPC(t, func(server *grpc.Server) { apiv1.RegisterResourceServiceServer(server, handler) })
	oldTimeout, oldOutput := globalTimeout, outputFormat
	t.Cleanup(func() { globalTimeout, outputFormat = oldTimeout, oldOutput })
	globalTimeout, outputFormat = 0, "json"
}
func healthReply(t *testing.T, fields map[string]any) *apiv1.GetResponse {
	t.Helper()
	state, err := structpb.NewStruct(fields)
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := structpb.NewStruct(map[string]any{"password": "PRIVATE-SPEC", "privateKeyFile": "/PRIVATE-KEY-PATH"})
	return &apiv1.GetResponse{Resource: &apiv1.Resource{ApiVersion: "apps.openctl.io/v1", Kind: "Jellyfin", Metadata: &apiv1.Metadata{Name: "media", Annotations: map[string]string{"note": "PRIVATE-ANNOTATION"}}, Spec: spec, Status: state}, Applied: &apiv1.Resource{Spec: spec}}
}
func executeHealth(ctx context.Context, out io.Writer, args ...string) (string, error) {
	cmd := newCtlHealthCommand()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	var diagnostic bytes.Buffer
	cmd.SetOut(out)
	cmd.SetErr(&diagnostic)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(ctx)
	return diagnostic.String(), err
}
func healthArgs() []string {
	return []string{"Jellyfin", "media", "--api-version", "apps.openctl.io/v1"}
}

func TestCtlHealthRequiresExplicitReadinessAndConsistentChecks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields map[string]any
		state  string
	}{
		{"healthy private app", map[string]any{"ready": true, "checks": map[string]any{"application": map[string]any{"state": "ready"}, "publicEndpoint": map[string]any{"state": "not_configured"}}}, "ready"},
		{"explicit aggregate failure", map[string]any{"ready": false, "checks": map[string]any{"gpu": map[string]any{"state": "unknown"}}}, "not_ready"},
		{"running is not ready", map[string]any{"state": "running", "phase": "Ready"}, "unknown"},
		{"string true is not ready", map[string]any{"ready": "true"}, "unknown"},
		{"successful checks do not invent aggregate", map[string]any{"checks": map[string]any{"application": map[string]any{"state": "ready"}}}, "unknown"},
		{"aggregate cannot hide failed check", map[string]any{"ready": true, "checks": map[string]any{"gpu": map[string]any{"state": "not_ready"}, "media": map[string]any{"state": "unknown"}}}, "not_ready"},
		{"aggregate cannot hide unknown check", map[string]any{"ready": true, "checks": map[string]any{"gpu": map[string]any{"state": "unknown"}}}, "unknown"},
		{"future evidence fails closed", map[string]any{"ready": true, "checks": map[string]any{"gpu": map[string]any{"state": "future"}}}, "unknown"},
		{"obsolete evidence fails closed", map[string]any{"ready": true, "checks": map[string]any{"gpu": map[string]any{"ready": true}}}, "unknown"},
		{"malformed evidence fails closed", map[string]any{"ready": true, "checks": map[string]any{"gpu": []any{}}}, "unknown"},
		{"malformed check collection", map[string]any{"ready": true, "checks": []any{}}, "unknown"},
		{"aggregate without individual checks", map[string]any{"ready": true}, "ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := &healthRPC{get: func(context.Context, *apiv1.GetRequest) (*apiv1.GetResponse, error) {
				return healthReply(t, tc.fields), nil
			}}
			setupHealthRPC(t, handler)
			var out bytes.Buffer
			_, err := executeHealth(context.Background(), &out, healthArgs()...)
			if (err == nil) != (tc.state == "ready") {
				t.Fatalf("readiness exit mismatch: state=%s err=%v", tc.state, err)
			}
			var result struct {
				Status struct {
					State string `json:"state"`
					Ready bool   `json:"ready"`
				}
			}
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Status.State != tc.state || result.Status.Ready != (tc.state == "ready") {
				t.Fatalf("unexpected readiness: %s", &out)
			}
			if handler.calls.Load() != 1 {
				t.Fatal("health check polled or retried")
			}
		})
	}
}

func TestCtlHealthProjectsOnlyHealthFieldsAndPreservesFailureDiagnostics(t *testing.T) {
	for _, format := range []string{"json", "yaml", "table"} {
		t.Run(format, func(t *testing.T) {
			handler := &healthRPC{get: func(context.Context, *apiv1.GetRequest) (*apiv1.GetResponse, error) {
				return healthReply(t, map[string]any{
					"ready": false, "message": "Guest reboot must be explicitly reviewed", "kubeconfig": "PRIVATE-STATUS",
					"checks": map[string]any{"gpu": map[string]any{"state": "not_ready", "message": "GPU driver unavailable", "token": "PRIVATE-CHECK"}, "publicEndpoint": map[string]any{"state": "not_configured"}},
				}), nil
			}}
			setupHealthRPC(t, handler)
			outputFormat = format
			var out bytes.Buffer
			_, err := executeHealth(context.Background(), &out, healthArgs()...)
			if err == nil {
				t.Fatal("failed health returned success")
			}
			for _, secret := range []string{"PRIVATE-SPEC", "PRIVATE-KEY-PATH", "PRIVATE-ANNOTATION", "PRIVATE-STATUS", "PRIVATE-CHECK"} {
				if strings.Contains(out.String(), secret) {
					t.Fatal("health output included non-health data")
				}
			}
			if !strings.Contains(out.String(), "GPU driver unavailable") || !strings.Contains(out.String(), "Guest reboot must be explicitly reviewed") || !strings.Contains(out.String(), "not_configured") {
				t.Fatalf("lost repair evidence: %s", &out)
			}
			if format != "table" {
				var projection map[string]any
				if format == "json" {
					err = json.Unmarshal(out.Bytes(), &projection)
				} else {
					err = yaml.Unmarshal(out.Bytes(), &projection)
				}
				if err != nil {
					t.Fatal(err)
				}
				if _, exists := projection["spec"]; exists {
					t.Fatal("health projection retained spec")
				}
				if projection["status"].(map[string]any)["state"] != "not_ready" {
					t.Fatal("structured health lost failure")
				}
			}
		})
	}
}

func TestCtlHealthDoesNotInventReadinessOnPrivateReadErrors(t *testing.T) {
	for _, code := range []codes.Code{codes.Unavailable, codes.NotFound, codes.PermissionDenied, codes.Internal} {
		t.Run(code.String(), func(t *testing.T) {
			handler := &healthRPC{get: func(context.Context, *apiv1.GetRequest) (*apiv1.GetResponse, error) {
				return nil, status.Error(code, "PRIVATE-UPSTREAM https://user:password@private.invalid/")
			}}
			setupHealthRPC(t, handler)
			var out bytes.Buffer
			diagnostic, err := executeHealth(context.Background(), &out, healthArgs()...)
			if err == nil || out.Len() != 0 || handler.calls.Load() != 1 {
				t.Fatal("failed read invented readiness, emitted data or retried")
			}
			if strings.Contains(diagnostic+err.Error(), "PRIVATE-") || strings.Contains(diagnostic+err.Error(), "private.invalid") {
				t.Fatal("health read forwarded raw provider diagnostics")
			}
		})
	}
}

func TestCtlHealthRejectsDifferentOrMissingResource(t *testing.T) {
	for _, different := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "different"}[different], func(t *testing.T) {
			handler := &healthRPC{get: func(context.Context, *apiv1.GetRequest) (*apiv1.GetResponse, error) {
				if !different {
					return &apiv1.GetResponse{}, nil
				}
				r := healthReply(t, map[string]any{"ready": true})
				r.Resource.Metadata.Name = "another-app"
				return r, nil
			}}
			setupHealthRPC(t, handler)
			var out bytes.Buffer
			_, err := executeHealth(context.Background(), &out, healthArgs()...)
			if err == nil || out.Len() != 0 {
				t.Fatal("reported another or absent resource as healthy")
			}
		})
	}
}

func TestCtlHealthCancellationNeverReportsReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler := &healthRPC{get: func(ctx context.Context, _ *apiv1.GetRequest) (*apiv1.GetResponse, error) {
		cancel()
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}}
	setupHealthRPC(t, handler)
	var out bytes.Buffer
	_, err := executeHealth(ctx, &out, healthArgs()...)
	if err == nil || out.Len() != 0 || handler.calls.Load() != 1 {
		t.Fatal("canceled read reported readiness or retried")
	}
}

func TestCtlHealthOutputFailureDoesNotRetry(t *testing.T) {
	handler := &healthRPC{get: func(context.Context, *apiv1.GetRequest) (*apiv1.GetResponse, error) {
		return healthReply(t, map[string]any{"ready": true}), nil
	}}
	setupHealthRPC(t, handler)
	_, err := executeHealth(context.Background(), actionFailWriter{}, healthArgs()...)
	if err == nil || handler.calls.Load() != 1 {
		t.Fatal("output failure was lost or re-read resource")
	}
}

func TestCtlHealthRequiresExplicitTargetAndBoundedTimeout(t *testing.T) {
	handler := &healthRPC{get: func(context.Context, *apiv1.GetRequest) (*apiv1.GetResponse, error) {
		return nil, errors.New("unexpected read")
	}}
	setupHealthRPC(t, handler)
	for _, args := range [][]string{{"Jellyfin", "media"}, {"Jellyfin", "--api-version", "apps.openctl.io/v1"}, {"", "media", "--api-version", "apps.openctl.io/v1"}} {
		var out bytes.Buffer
		if _, err := executeHealth(context.Background(), &out, args...); err == nil {
			t.Fatal("ambiguous target accepted")
		}
	}
	globalTimeout = -1
	var out bytes.Buffer
	if _, err := executeHealth(context.Background(), &out, healthArgs()...); err == nil {
		t.Fatal("negative deadline accepted")
	}
	if handler.calls.Load() != 0 {
		t.Fatal("invalid target/deadline reached controller")
	}
}
