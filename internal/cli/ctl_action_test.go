package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type actionRPC struct {
	apiv1.UnimplementedResourceServiceServer
	calls  atomic.Int32
	invoke func(context.Context, *apiv1.InvokeActionRequest) (*apiv1.InvokeActionResponse, error)
	list   func(context.Context, *apiv1.ListActionsRequest) (*apiv1.ListActionsResponse, error)
}

func (s *actionRPC) InvokeAction(ctx context.Context, req *apiv1.InvokeActionRequest) (*apiv1.InvokeActionResponse, error) {
	s.calls.Add(1)
	return s.invoke(ctx, req)
}

func (s *actionRPC) ListActions(ctx context.Context, req *apiv1.ListActionsRequest) (*apiv1.ListActionsResponse, error) {
	return s.list(ctx, req)
}

func setupActionRPC(t *testing.T, handler *actionRPC) {
	t.Helper()
	configureControllerRPC(t, func(server *grpc.Server) { apiv1.RegisterResourceServiceServer(server, handler) })
	oldTimeout, oldOutput := globalTimeout, outputFormat
	t.Cleanup(func() { globalTimeout, outputFormat = oldTimeout, oldOutput })
	globalTimeout, outputFormat = 0, "table"
}

func executeAction(ctx context.Context, input string, output io.Writer, args ...string) (string, error) {
	cmd := newCtlActionCommand()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	var diagnostic bytes.Buffer
	cmd.SetOut(output)
	cmd.SetErr(&diagnostic)
	cmd.SetIn(strings.NewReader(input))
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(ctx)
	return diagnostic.String(), err
}

func actionRunArgs(extra ...string) []string {
	return append([]string{"run", "Cluster", "sample", "kubeconfig", "--api-version", "k3s.openctl.io/v1"}, extra...)
}

func TestCtlActionRefusesExistingDownloadBeforeInvocation(t *testing.T) {
	handler := &actionRPC{invoke: func(context.Context, *apiv1.InvokeActionRequest) (*apiv1.InvokeActionResponse, error) {
		return &apiv1.InvokeActionResponse{DownloadContent: "must not be fetched"}, nil
	}}
	setupActionRPC(t, handler)
	dir := t.TempDir()
	target := filepath.Join(dir, "existing")
	if err := os.WriteFile(target, []byte("original"), 0640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(dir, "absent"), dangling); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{target, link, dangling} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			var out bytes.Buffer
			_, err := executeAction(context.Background(), "", &out, actionRunArgs("--download", path)...)
			if err == nil || handler.calls.Load() != 0 {
				t.Fatalf("err=%v calls=%d", err, handler.calls.Load())
			}
		})
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "original" {
		t.Fatalf("content=%q err=%v", content, err)
	}
}

func TestCtlActionDownloadsPrivatelyWithoutEchoingPayload(t *testing.T) {
	payload := "credential: secret\n\x00tail"
	handler := &actionRPC{invoke: func(ctx context.Context, req *apiv1.InvokeActionRequest) (*apiv1.InvokeActionResponse, error) {
		if got := metadata.ValueFromIncomingContext(ctx, "authorization"); len(got) != 1 || got[0] != "Bearer fixture-token" {
			return nil, status.Error(codes.Unauthenticated, "missing token")
		}
		if _, ok := ctx.Deadline(); !ok {
			return nil, status.Error(codes.InvalidArgument, "missing deadline")
		}
		if req.GetApiVersion() != "k3s.openctl.io/v1" || req.GetKind() != "Cluster" || req.GetResourceName() != "sample" || req.GetAction() != "kubeconfig" {
			return nil, status.Error(codes.InvalidArgument, "wrong identity")
		}
		return &apiv1.InvokeActionResponse{DownloadContent: payload, DownloadFilename: "../../untrusted", Message: "request accepted", Url: "https://console.invalid/task"}, nil
	}}
	setupActionRPC(t, handler)
	for _, format := range []string{"table", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			outputFormat = format
			path := filepath.Join(t.TempDir(), "credentials")
			var out bytes.Buffer
			diagnostic, err := executeAction(context.Background(), "", &out, actionRunArgs("--download", path)...)
			if err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(path)
			if err != nil || string(content) != payload {
				t.Fatalf("wrong content: %v", err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("non-private file: %v %v", info, err)
			}
			if strings.Contains(out.String()+diagnostic, "credential:") || strings.Contains(out.String(), "downloadContent") {
				t.Fatal("payload leaked")
			}
			if !strings.Contains(out.String(), "request accepted") || !strings.Contains(out.String(), "https://console.invalid/task") {
				t.Fatalf("missing provider result: %s", &out)
			}
		})
	}
}

func TestCtlActionRawDownloadHasNoMixedOutput(t *testing.T) {
	const payload = "secret raw\n"
	handler := &actionRPC{invoke: func(context.Context, *apiv1.InvokeActionRequest) (*apiv1.InvokeActionResponse, error) {
		return &apiv1.InvokeActionResponse{DownloadContent: payload, Message: "must not mix", Url: "https://sensitive.invalid"}, nil
	}}
	setupActionRPC(t, handler)
	outputFormat = "json"
	var out bytes.Buffer
	diagnostic, err := executeAction(context.Background(), "", &out, actionRunArgs("--download", "-")...)
	if err != nil || out.String() != payload || diagnostic != "" || handler.calls.Load() != 1 {
		t.Fatalf("err=%v output=%q diagnostic=%q calls=%d", err, &out, diagnostic, handler.calls.Load())
	}
}

func TestCtlActionNeverPrintsUnrequestedPayload(t *testing.T) {
	handler := &actionRPC{invoke: func(context.Context, *apiv1.InvokeActionRequest) (*apiv1.InvokeActionResponse, error) {
		return &apiv1.InvokeActionResponse{DownloadContent: "sensitive-payload"}, nil
	}}
	setupActionRPC(t, handler)
	for _, format := range []string{"table", "json", "yaml"} {
		outputFormat = format
		var out bytes.Buffer
		diagnostic, err := executeAction(context.Background(), "", &out, actionRunArgs()...)
		if err == nil || !strings.Contains(err.Error(), "request already completed") || !strings.Contains(err.Error(), "not saved") || !strings.Contains(err.Error(), "before repeating") {
			t.Fatalf("misleading error: %v", err)
		}
		if strings.Contains(out.String()+diagnostic+err.Error(), "sensitive-payload") || out.Len() != 0 {
			t.Fatal("payload leaked")
		}
	}
	if handler.calls.Load() != 3 {
		t.Fatalf("calls=%d", handler.calls.Load())
	}
}

func TestCtlActionRejectsMalformedAndConflictingParamsBeforeInvocation(t *testing.T) {
	handler := &actionRPC{invoke: func(context.Context, *apiv1.InvokeActionRequest) (*apiv1.InvokeActionResponse, error) {
		return &apiv1.InvokeActionResponse{}, nil
	}}
	setupActionRPC(t, handler)
	cases := []struct {
		input   string
		options []string
	}{
		{`{"key":"secret-value"`, nil},
		{`{"key":123}`, nil},
		{`{"key":null}`, nil},
		{`{"key":true}`, nil},
		{`{"key":["secret-value"]}`, nil},
		{`{"key":{"nested":"secret-value"}}`, nil},
		{`[]`, nil},
		{`{"key":"secret-value"} {}`, nil},
		{`{"key":"secret-value"} trailing-secret-value`, nil},
		{`{"key":"secret-value","key":"other-secret-value"}`, nil},
		{`{"key":"secret-value"}`, []string{"--param", "key=other-secret-value"}},
		{`{}`, []string{"--param", "key=secret-value", "--param", "key=other-secret-value"}},
		{`{}`, []string{"--param", "secret-value"}},
	}
	for i, tc := range cases {
		var out bytes.Buffer
		args := actionRunArgs("--params-file", "-")
		args = append(args, tc.options...)
		diagnostic, err := executeAction(context.Background(), tc.input, &out, args...)
		if err == nil {
			t.Fatalf("case %d accepted invalid params", i)
		}
		if strings.Contains(out.String()+diagnostic+err.Error(), "secret-value") {
			t.Fatalf("case %d leaked a parameter", i)
		}
	}
	if handler.calls.Load() != 0 {
		t.Fatalf("calls=%d", handler.calls.Load())
	}
}

func TestCtlActionRPCFailureIsUncertainAndNotRetried(t *testing.T) {
	handler := &actionRPC{invoke: func(context.Context, *apiv1.InvokeActionRequest) (*apiv1.InvokeActionResponse, error) {
		return nil, status.Error(codes.Unavailable, "secret-value in backend error")
	}}
	setupActionRPC(t, handler)
	path := filepath.Join(t.TempDir(), "download")
	var out bytes.Buffer
	diagnostic, err := executeAction(context.Background(), "", &out, actionRunArgs("--download", path)...)
	if err == nil || !strings.Contains(err.Error(), "uncertain") || !strings.Contains(err.Error(), "before repeating") {
		t.Fatalf("err=%v", err)
	}
	if handler.calls.Load() != 1 || out.Len() != 0 || strings.Contains(diagnostic+err.Error(), "secret-value") {
		t.Fatalf("unsafe failure: calls=%d err=%v", handler.calls.Load(), err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("failed download retained: %v", err)
	}
}

func TestCtlActionCancellationIsNotRetried(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler := &actionRPC{invoke: func(ctx context.Context, _ *apiv1.InvokeActionRequest) (*apiv1.InvokeActionResponse, error) {
		cancel()
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}}
	setupActionRPC(t, handler)
	var out bytes.Buffer
	_, err := executeAction(ctx, "", &out, actionRunArgs()...)
	if err == nil || !strings.Contains(err.Error(), "uncertain") || handler.calls.Load() != 1 || out.Len() != 0 {
		t.Fatalf("err=%v calls=%d output=%q", err, handler.calls.Load(), &out)
	}
}

type actionFailWriter struct{}

func (actionFailWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func TestCtlActionOutputFailureAcknowledgesSuccessfulRequest(t *testing.T) {
	handler := &actionRPC{invoke: func(context.Context, *apiv1.InvokeActionRequest) (*apiv1.InvokeActionResponse, error) {
		return &apiv1.InvokeActionResponse{Message: "task started"}, nil
	}}
	setupActionRPC(t, handler)
	_, err := executeAction(context.Background(), "", actionFailWriter{}, actionRunArgs()...)
	if err == nil || !strings.Contains(err.Error(), "request already completed") || handler.calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, handler.calls.Load())
	}
}

func TestCtlActionMissingRequestedPayloadFailsAndRemovesFile(t *testing.T) {
	handler := &actionRPC{invoke: func(context.Context, *apiv1.InvokeActionRequest) (*apiv1.InvokeActionResponse, error) {
		return &apiv1.InvokeActionResponse{}, nil
	}}
	setupActionRPC(t, handler)
	path := filepath.Join(t.TempDir(), "empty")
	var out bytes.Buffer
	_, err := executeAction(context.Background(), "", &out, actionRunArgs("--download", path)...)
	if err == nil || !strings.Contains(err.Error(), "no download content") || !strings.Contains(err.Error(), "request already completed") {
		t.Fatalf("err=%v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("empty download retained: %v", err)
	}
}

func TestCtlActionRequiresExplicitVersionAndRejectsNegativeTimeout(t *testing.T) {
	handler := &actionRPC{}
	setupActionRPC(t, handler)
	for _, args := range [][]string{{"run", "Cluster", "sample", "start"}, {"list", "Cluster"}} {
		var out bytes.Buffer
		_, err := executeAction(context.Background(), "", &out, args...)
		if err == nil || !strings.Contains(err.Error(), "--api-version") {
			t.Fatalf("err=%v", err)
		}
	}
	globalTimeout = -time.Second
	var out bytes.Buffer
	_, err := executeAction(context.Background(), "", &out, actionRunArgs()...)
	if err == nil || !strings.Contains(err.Error(), "negative") {
		t.Fatalf("err=%v", err)
	}
	if handler.calls.Load() != 0 {
		t.Fatalf("calls=%d", handler.calls.Load())
	}
}
