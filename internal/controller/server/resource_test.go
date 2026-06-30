package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/storage"
	tlspkg "github.com/openctl/openctl/internal/controller/tls"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
)

// fakeProvider is a stub provider that records calls and returns canned
// responses. Used by the integration test to verify the controller's
// gRPC handlers route correctly through the registry.
type fakeProvider struct {
	applied   []*protocol.Resource
	gets      [][]string
	lists     []string
	deletes   [][]string
	getReturn *protocol.Resource
	getErr    error
	applyHook func() // optional: called inside Apply for blocking-test scenarios
}

func (f *fakeProvider) Name() string    { return "fake" }
func (f *fakeProvider) Kinds() []string { return []string{"FakeKind"} }

func (f *fakeProvider) Apply(_ context.Context, m *protocol.Resource) (*protocol.Resource, error) {
	if f.applyHook != nil {
		f.applyHook()
	}
	f.applied = append(f.applied, m)
	out := *m
	if out.Status == nil {
		out.Status = map[string]any{}
	}
	out.Status["applied"] = true
	return &out, nil
}

func (f *fakeProvider) Get(_ context.Context, kind, name string) (*protocol.Resource, error) {
	f.gets = append(f.gets, []string{kind, name})
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.getReturn, nil
}

func (f *fakeProvider) List(_ context.Context, kind string) ([]*protocol.Resource, error) {
	f.lists = append(f.lists, kind)
	return []*protocol.Resource{
		{APIVersion: "fake.openctl.io/v1", Kind: kind, Metadata: protocol.ResourceMetadata{Name: "a"}},
		{APIVersion: "fake.openctl.io/v1", Kind: kind, Metadata: protocol.ResourceMetadata{Name: "b"}},
	}, nil
}

func (f *fakeProvider) Delete(_ context.Context, kind, name string) error {
	f.deletes = append(f.deletes, []string{kind, name})
	return nil
}

// startTestServer spins up a controller server with real async operations
// wiring (Store + Dispatcher) so the tests exercise the production path.
func startTestServer(t *testing.T, registry *providers.Registry) (string, *tlspkg.Material) {
	addr, mat, _ := startTestServerWithManifests(t, registry)
	return addr, mat
}

// startTestServerWithManifests is startTestServer plus a returned
// manifests.Store so tests can populate desired state directly without
// going through the dispatcher.
func startTestServerWithManifests(
	t *testing.T,
	registry *providers.Registry,
) (string, *tlspkg.Material, *manifests.Store) {
	t.Helper()
	dir := t.TempDir()
	mat, err := tlspkg.EnsureMaterial(dir, "localhost", []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}

	db, err := storage.Open(context.Background(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := operations.New(db, 50)
	manifestStore := manifests.New(db)
	// Wire the manifest store as the dispatcher's sink so production-path
	// applies persist desired state. The managed-only filter relies on that
	// — applies that don't reach applied_manifests would be hidden from
	// subsequent List/Get/Watch.
	dispatcher := operations.NewDispatcher(store, registry, manifestStore, 50*time.Millisecond)
	dispatcher.Start(context.Background())
	t.Cleanup(dispatcher.Stop)

	srv, err := New(Options{
		CertFile:   mat.ServerCertPath,
		KeyFile:    mat.ServerKeyPath,
		Token:      "",
		Registry:   registry,
		Operations: store,
		Dispatcher: dispatcher,
		Manifests:  manifestStore,
	})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.ServeListener(ln) }()
	t.Cleanup(srv.Stop)
	return ln.Addr().String(), mat, manifestStore
}

// awaitOp polls OperationService.GetOperation until terminal or timeout.
func awaitOp(t *testing.T, conn *grpc.ClientConn, opID string, timeout time.Duration) *apiv1.Operation {
	t.Helper()
	client := apiv1.NewOperationServiceClient(conn)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		op, err := client.GetOperation(ctx, &apiv1.GetOperationRequest{Id: opID})
		cancel()
		if err != nil {
			t.Fatalf("GetOperation: %v", err)
		}
		switch op.GetStatus() {
		case "succeeded", "failed", "interrupted":
			return op
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("op %s did not reach terminal status within %s", opID, timeout)
	return nil
}

func dialTestServer(t *testing.T, addr, caPath string) *grpc.ClientConn {
	t.Helper()
	caData, err := os.ReadFile(caPath) // #nosec G304 -- test
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caData)
	creds := credentials.NewTLS(&tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12})
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestResourceServiceApplyEnqueuesOpAndDispatches(t *testing.T) {
	fake := &fakeProvider{}
	reg := providers.NewRegistry()
	reg.Register(fake)

	addr, mat := startTestServer(t, reg)
	conn := dialTestServer(t, addr, mat.CACertPath)
	defer func() { _ = conn.Close() }()

	spec, _ := structpb.NewStruct(map[string]any{"key": "value"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = metadata.AppendToOutgoingContext // keep import alive

	resp, err := apiv1.NewResourceServiceClient(conn).Apply(ctx, &apiv1.ApplyRequest{
		Resource: &apiv1.Resource{
			ApiVersion: "fake.openctl.io/v1",
			Kind:       "FakeKind",
			Metadata:   &apiv1.Metadata{Name: "x"},
			Spec:       spec,
		},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if resp.GetOperationId() == "" {
		t.Fatal("Apply did not return an operation_id")
	}

	final := awaitOp(t, conn, resp.GetOperationId(), 3*time.Second)
	if final.GetStatus() != "succeeded" {
		t.Fatalf("op status = %q, want succeeded (error=%q)", final.GetStatus(), final.GetError())
	}
	if len(fake.applied) != 1 {
		t.Errorf("provider Apply called %d times, want 1", len(fake.applied))
	}
	if got := final.GetResult().GetStatus().AsMap()["applied"]; got != true {
		t.Errorf("op.result.status.applied = %v, want true", got)
	}
}

// TestResourceServiceApplyConflictOnSameResource verifies the fail-fast
// concurrency rule: a second Apply for the same resource while one is
// in flight returns AlreadyExists.
func TestResourceServiceApplyConflictOnSameResource(t *testing.T) {
	released := make(chan struct{})
	// Defer the release in the test body — this runs BEFORE the dispatcher's
	// Stop in t.Cleanup, so the in-flight op completes first and Stop
	// doesn't deadlock waiting for it.
	defer close(released)

	fake := &fakeProvider{}
	fake.applyHook = func() { <-released }
	reg := providers.NewRegistry()
	reg.Register(fake)

	addr, mat := startTestServer(t, reg)
	conn := dialTestServer(t, addr, mat.CACertPath)
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := apiv1.NewResourceServiceClient(conn)

	resource := &apiv1.Resource{
		ApiVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
		Metadata:   &apiv1.Metadata{Name: "blocker"},
	}
	first, err := client.Apply(ctx, &apiv1.ApplyRequest{Resource: resource})
	if err != nil {
		t.Fatalf("first Apply: %v", err)
	}

	// Wait until the dispatcher has picked it up (status=running).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		op, err := apiv1.NewOperationServiceClient(conn).GetOperation(ctx, &apiv1.GetOperationRequest{Id: first.GetOperationId()})
		if err == nil && op.GetStatus() == "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Second submission should fail-fast.
	_, err = client.Apply(ctx, &apiv1.ApplyRequest{Resource: resource})
	if err == nil {
		t.Fatal("want AlreadyExists error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.AlreadyExists {
		t.Errorf("code = %v, want AlreadyExists", st.Code())
	}
}

func TestResourceServiceGetReturnsAppliedManifestAndDrift(t *testing.T) {
	// Desired manifest has cpus=2; provider reports cpus=4 (drift). The
	// response should carry Applied (desired) + AppliedAt populated and
	// drift listing "spec.cpus" with both values.
	fake := &fakeProvider{
		getReturn: &protocol.Resource{
			APIVersion: "fake.openctl.io/v1",
			Kind:       "FakeKind",
			Metadata:   protocol.ResourceMetadata{Name: "drifty"},
			Spec:       map[string]any{"cpus": 4.0, "memory": "2Gi"},
			Status:     map[string]any{"state": "running"},
		},
	}
	reg := providers.NewRegistry()
	reg.Register(fake)
	addr, mat, mstore := startTestServerWithManifests(t, reg)
	conn := dialTestServer(t, addr, mat.CACertPath)
	defer func() { _ = conn.Close() }()

	if err := mstore.Save(context.Background(), &protocol.Resource{
		APIVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
		Metadata:   protocol.ResourceMetadata{Name: "drifty"},
		Spec:       map[string]any{"cpus": 2.0, "memory": "2Gi"},
	}); err != nil {
		t.Fatalf("Save manifest: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := apiv1.NewResourceServiceClient(conn).Get(ctx, &apiv1.GetRequest{
		ApiVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
		Name:       "drifty",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if resp.GetApplied() == nil {
		t.Fatal("response missing Applied; want desired manifest")
	}
	appliedSpec := resp.GetApplied().GetSpec().AsMap()
	if got := appliedSpec["cpus"]; got != 2.0 {
		t.Errorf("applied.spec.cpus = %v, want 2.0", got)
	}
	if resp.GetAppliedAt() == "" {
		t.Error("response missing AppliedAt; want non-empty RFC3339 timestamp")
	}
	// Drift should call out the changed key.
	drift := resp.GetResource().GetDrift()
	if len(drift) != 1 || drift[0].GetPath() != "spec.cpus" {
		t.Errorf("drift = %+v, want one entry at spec.cpus", drift)
	}
}

func TestResourceServiceGetReturnsNotFoundForUnmanaged(t *testing.T) {
	// Resource was created out-of-band — observed in the provider but never
	// applied through openctl. The managed-only filter hides it; Get must
	// return NotFound so a stale UI link looks the same as a deleted one.
	fake := &fakeProvider{
		getReturn: &protocol.Resource{
			APIVersion: "fake.openctl.io/v1",
			Kind:       "FakeKind",
			Metadata:   protocol.ResourceMetadata{Name: "orphan"},
			Spec:       map[string]any{"cpus": 1.0},
		},
	}
	reg := providers.NewRegistry()
	reg.Register(fake)
	addr, mat, _ := startTestServerWithManifests(t, reg)
	conn := dialTestServer(t, addr, mat.CACertPath)
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := apiv1.NewResourceServiceClient(conn).Get(ctx, &apiv1.GetRequest{
		ApiVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
		Name:       "orphan",
	})
	if err == nil {
		t.Fatal("Get on unmanaged resource: want NotFound, got nil error")
	}
	if got := status.Code(err); got != codes.NotFound {
		t.Errorf("Get error code = %v, want NotFound", got)
	}
}

func TestResourceServiceGetReturnsObservedWhenManagedButNoSpecDrift(t *testing.T) {
	// Counterpart to the NotFound test above: when the resource IS in
	// applied_manifests but observed state matches, Get returns the resource
	// with Applied populated and no drift.
	fake := &fakeProvider{
		getReturn: &protocol.Resource{
			APIVersion: "fake.openctl.io/v1",
			Kind:       "FakeKind",
			Metadata:   protocol.ResourceMetadata{Name: "managed"},
			Spec:       map[string]any{"cpus": 2.0},
		},
	}
	reg := providers.NewRegistry()
	reg.Register(fake)
	addr, mat, mstore := startTestServerWithManifests(t, reg)
	conn := dialTestServer(t, addr, mat.CACertPath)
	defer func() { _ = conn.Close() }()

	if err := mstore.Save(context.Background(), &protocol.Resource{
		APIVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
		Metadata:   protocol.ResourceMetadata{Name: "managed"},
		Spec:       map[string]any{"cpus": 2.0},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := apiv1.NewResourceServiceClient(conn).Get(ctx, &apiv1.GetRequest{
		ApiVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
		Name:       "managed",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp.GetApplied() == nil {
		t.Error("Applied should be populated for managed resource")
	}
	if len(resp.GetResource().GetDrift()) != 0 {
		t.Errorf("drift = %+v, want empty when observed matches applied", resp.GetResource().GetDrift())
	}
}

// fakeDryRunner is a fakeProvider extension that implements
// providers.DryRunner. Used by the DryRunApply tests below.
type fakeDryRunner struct {
	*fakeProvider
	result *providers.DryRunResult
	err    error
}

func (f *fakeDryRunner) DryRun(_ context.Context, _ *protocol.Resource) (*providers.DryRunResult, error) {
	return f.result, f.err
}

func TestResourceServiceDryRunApplyComputesDriftWhenManifestExists(t *testing.T) {
	// Atomic provider (no DryRunner) — handler returns just the spec
	// diff against the persisted applied manifest.
	fake := &fakeProvider{}
	reg := providers.NewRegistry()
	reg.Register(fake)
	addr, mat, mstore := startTestServerWithManifests(t, reg)
	conn := dialTestServer(t, addr, mat.CACertPath)
	defer func() { _ = conn.Close() }()

	if err := mstore.Save(context.Background(), &protocol.Resource{
		APIVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
		Metadata:   protocol.ResourceMetadata{Name: "preview"},
		Spec:       map[string]any{"cpus": 2.0, "memory": "2Gi"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	specStruct, _ := structpb.NewStruct(map[string]any{"cpus": 4.0, "memory": "2Gi"})
	resp, err := apiv1.NewResourceServiceClient(conn).DryRunApply(ctx, &apiv1.DryRunApplyRequest{
		Resource: &apiv1.Resource{
			ApiVersion: "fake.openctl.io/v1",
			Kind:       "FakeKind",
			Metadata:   &apiv1.Metadata{Name: "preview"},
			Spec:       specStruct,
		},
	})
	if err != nil {
		t.Fatalf("DryRunApply: %v", err)
	}
	if len(resp.GetDiff()) != 1 || resp.GetDiff()[0].GetPath() != "spec.cpus" {
		t.Errorf("diff = %+v, want one entry at spec.cpus", resp.GetDiff())
	}
	if resp.GetSummary() == "" {
		t.Error("summary should be populated even for atomic providers")
	}
	if len(resp.GetValidationErrors()) != 0 {
		t.Errorf("validation errors = %v, want none", resp.GetValidationErrors())
	}
}

func TestResourceServiceDryRunApplyForwardsProviderPlan(t *testing.T) {
	// Composite provider — handler returns the provider's per-child
	// actions + required gates verbatim.
	fake := &fakeProvider{}
	dr := &fakeDryRunner{
		fakeProvider: fake,
		result: &providers.DryRunResult{
			Children: []providers.ChildAction{
				{Verb: "create", Kind: "VirtualMachine", Name: "node-1", Detail: "new worker"},
				{Verb: "destroy", Kind: "VirtualMachine", Name: "node-0", Detail: "drop worker"},
			},
			RequiredGates: []string{providers.GateAllowDestructive},
			Summary:       "would add 1, remove 1 node(s)",
		},
	}
	reg := providers.NewRegistry()
	reg.Register(dr)
	addr, mat, _ := startTestServerWithManifests(t, reg)
	conn := dialTestServer(t, addr, mat.CACertPath)
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := apiv1.NewResourceServiceClient(conn).DryRunApply(ctx, &apiv1.DryRunApplyRequest{
		Resource: &apiv1.Resource{
			ApiVersion: "fake.openctl.io/v1",
			Kind:       "FakeKind",
			Metadata:   &apiv1.Metadata{Name: "composite"},
		},
	})
	if err != nil {
		t.Fatalf("DryRunApply: %v", err)
	}
	if got := len(resp.GetChildren()); got != 2 {
		t.Errorf("children = %d, want 2", got)
	}
	if resp.GetChildren()[0].GetVerb() != "create" || resp.GetChildren()[1].GetVerb() != "destroy" {
		t.Errorf("unexpected child verbs: %+v", resp.GetChildren())
	}
	if len(resp.GetRequiredGates()) != 1 || resp.GetRequiredGates()[0] != providers.GateAllowDestructive {
		t.Errorf("gates = %v, want [allow_destructive]", resp.GetRequiredGates())
	}
	if resp.GetSummary() != "would add 1, remove 1 node(s)" {
		t.Errorf("summary = %q, want provider-supplied", resp.GetSummary())
	}
}

func TestResourceServiceDryRunApplyReturnsValidationErrorsInline(t *testing.T) {
	// Bad apiVersion (no provider registered for it) — surfaces as a
	// gRPC InvalidArgument so the editor distinguishes "wrong manifest
	// shape" from "schema validation failed".
	fake := &fakeProvider{}
	reg := providers.NewRegistry()
	reg.Register(fake)
	addr, mat, _ := startTestServerWithManifests(t, reg)
	conn := dialTestServer(t, addr, mat.CACertPath)
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Missing metadata.name — surfaces in validation_errors (not as RPC error).
	resp, err := apiv1.NewResourceServiceClient(conn).DryRunApply(ctx, &apiv1.DryRunApplyRequest{
		Resource: &apiv1.Resource{
			ApiVersion: "fake.openctl.io/v1",
			Kind:       "FakeKind",
			Metadata:   &apiv1.Metadata{},
		},
	})
	if err != nil {
		t.Fatalf("DryRunApply: %v", err)
	}
	if len(resp.GetValidationErrors()) == 0 {
		t.Error("validation_errors should be populated for missing name")
	}
}

func TestResourceServiceGetMapsNotFound(t *testing.T) {
	fake := &fakeProvider{getErr: providers.NotFound("FakeKind", "missing")}
	reg := providers.NewRegistry()
	reg.Register(fake)

	addr, mat := startTestServer(t, reg)
	conn := dialTestServer(t, addr, mat.CACertPath)
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := apiv1.NewResourceServiceClient(conn).Get(ctx, &apiv1.GetRequest{
		ApiVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
		Name:       "missing",
	})
	if err == nil {
		t.Fatal("want NotFound, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("error not a gRPC status: %v", err)
	}
	if st.Code() != codes.NotFound {
		t.Errorf("code = %v, want NotFound", st.Code())
	}
}

func TestResourceServiceListReturnsAll(t *testing.T) {
	fake := &fakeProvider{}
	reg := providers.NewRegistry()
	reg.Register(fake)

	addr, mat, mstore := startTestServerWithManifests(t, reg)
	conn := dialTestServer(t, addr, mat.CACertPath)
	defer func() { _ = conn.Close() }()

	// fakeProvider.List always returns observed resources "a" and "b".
	// The managed-only filter requires both to be in applied_manifests
	// for List to surface them.
	for _, name := range []string{"a", "b"} {
		if err := mstore.Save(context.Background(), &protocol.Resource{
			APIVersion: "fake.openctl.io/v1",
			Kind:       "FakeKind",
			Metadata:   protocol.ResourceMetadata{Name: name},
		}); err != nil {
			t.Fatalf("Save %q: %v", name, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := apiv1.NewResourceServiceClient(conn).List(ctx, &apiv1.ListRequest{
		ApiVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(resp.GetResources()) != 2 {
		t.Errorf("got %d resources, want 2", len(resp.GetResources()))
	}
}

func TestResourceServiceListFiltersUnmanaged(t *testing.T) {
	// fakeProvider.List returns "a" and "b". Save only "a" — the filter
	// should hide "b" since it was created out-of-band.
	fake := &fakeProvider{}
	reg := providers.NewRegistry()
	reg.Register(fake)

	addr, mat, mstore := startTestServerWithManifests(t, reg)
	conn := dialTestServer(t, addr, mat.CACertPath)
	defer func() { _ = conn.Close() }()

	if err := mstore.Save(context.Background(), &protocol.Resource{
		APIVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
		Metadata:   protocol.ResourceMetadata{Name: "a"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := apiv1.NewResourceServiceClient(conn).List(ctx, &apiv1.ListRequest{
		ApiVersion: "fake.openctl.io/v1", Kind: "FakeKind",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(resp.GetResources()) != 1 || resp.GetResources()[0].GetMetadata().GetName() != "a" {
		t.Errorf("List returned %v, want only [a]", resp.GetResources())
	}
}

func TestResourceServiceDeleteEnqueuesAndDispatches(t *testing.T) {
	fake := &fakeProvider{}
	reg := providers.NewRegistry()
	reg.Register(fake)

	addr, mat := startTestServer(t, reg)
	conn := dialTestServer(t, addr, mat.CACertPath)
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := apiv1.NewResourceServiceClient(conn).Delete(ctx, &apiv1.DeleteRequest{
		ApiVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
		Name:       "x",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if resp.GetOperationId() == "" {
		t.Fatal("Delete did not return an operation_id")
	}

	final := awaitOp(t, conn, resp.GetOperationId(), 3*time.Second)
	if final.GetStatus() != "succeeded" {
		t.Fatalf("op status = %q, want succeeded (error=%q)", final.GetStatus(), final.GetError())
	}
	if len(fake.deletes) != 1 || fake.deletes[0][1] != "x" {
		t.Errorf("delete not recorded correctly: %v", fake.deletes)
	}
}

// ownerProvider is a minimal Provider that also implements OwnershipChecker.
// Used to verify the resource handler's block-on-references logic.
type ownerProvider struct {
	owned map[string]bool // "kind:name" keys
}

func (o *ownerProvider) Name() string    { return "owner" }
func (o *ownerProvider) Kinds() []string { return []string{"OwnedThing"} }
func (o *ownerProvider) Apply(context.Context, *protocol.Resource) (*protocol.Resource, error) {
	return nil, nil
}
func (o *ownerProvider) Get(context.Context, string, string) (*protocol.Resource, error) {
	return nil, nil
}
func (o *ownerProvider) List(context.Context, string) ([]*protocol.Resource, error) {
	return nil, nil
}
func (o *ownerProvider) Delete(context.Context, string, string) error { return nil }

func (o *ownerProvider) OwnerOf(kind, name string) (string, string, bool) {
	if o.owned[kind+":"+name] {
		return "OwnedThing", "demo", true
	}
	return "", "", false
}

func TestResourceServiceDeleteBlockedWhenOwned(t *testing.T) {
	target := &fakeProvider{}
	owner := &ownerProvider{owned: map[string]bool{"FakeKind:locked": true}}

	reg := providers.NewRegistry()
	reg.Register(target) // name "fake"
	reg.Register(owner)  // name "owner"

	addr, mat := startTestServer(t, reg)
	conn := dialTestServer(t, addr, mat.CACertPath)
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := apiv1.NewResourceServiceClient(conn).Delete(ctx, &apiv1.DeleteRequest{
		ApiVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
		Name:       "locked",
	})
	if err == nil {
		t.Fatal("want FailedPrecondition, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition {
		t.Errorf("code = %v, want FailedPrecondition (%v)", st.Code(), err)
	}
}

// relationshipProvider implements OwnershipChecker + ChildrenLister so the
// resource handler's owner-ref / children plumbing (arch Phase 8) can be
// exercised end-to-end via Get.
type relationshipProvider struct {
	owned    map[string]string // "kind:name" → owner name; owner kind is constant ("Cluster")
	children map[string][]providers.ResourceRef
}

func (p *relationshipProvider) Name() string    { return "k3s" }
func (p *relationshipProvider) Kinds() []string { return []string{"Cluster"} }
func (p *relationshipProvider) Apply(context.Context, *protocol.Resource) (*protocol.Resource, error) {
	return nil, nil
}
func (p *relationshipProvider) Get(_ context.Context, kind, name string) (*protocol.Resource, error) {
	return &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       kind,
		Metadata:   protocol.ResourceMetadata{Name: name},
		Status:     map[string]any{"phase": "running"},
	}, nil
}
func (p *relationshipProvider) List(context.Context, string) ([]*protocol.Resource, error) {
	return nil, nil
}
func (p *relationshipProvider) Delete(context.Context, string, string) error { return nil }
func (p *relationshipProvider) OwnerOf(kind, name string) (string, string, bool) {
	if owner, ok := p.owned[kind+":"+name]; ok {
		return "Cluster", owner, true
	}
	return "", "", false
}
func (p *relationshipProvider) ChildrenOf(kind, name string) []providers.ResourceRef {
	return p.children[kind+":"+name]
}

func TestResourceServiceGetSurfacesChildren(t *testing.T) {
	parent := &relationshipProvider{
		children: map[string][]providers.ResourceRef{
			"Cluster:dev": {
				{APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine", Name: "dev-cp-0"},
				{APIVersion: "proxmox.openctl.io/v1", Kind: "VirtualMachine", Name: "dev-w-0"},
			},
		},
	}
	vm := &fakeProvider{
		getReturn: &protocol.Resource{
			APIVersion: "proxmox.openctl.io/v1",
			Kind:       "FakeKind",
			Metadata:   protocol.ResourceMetadata{Name: "dev-cp-0"},
		},
	}
	reg := providers.NewRegistry()
	reg.Register(parent)
	reg.Register(vm)

	addr, mat, mstore := startTestServerWithManifests(t, reg)
	conn := dialTestServer(t, addr, mat.CACertPath)
	defer func() { _ = conn.Close() }()

	// Cluster:dev must be in applied_manifests for the filter to surface it.
	if err := mstore.Save(context.Background(), &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       "Cluster",
		Metadata:   protocol.ResourceMetadata{Name: "dev"},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := apiv1.NewResourceServiceClient(conn).Get(ctx, &apiv1.GetRequest{
		ApiVersion: "k3s.openctl.io/v1",
		Kind:       "Cluster",
		Name:       "dev",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got := resp.GetResource().GetChildren()
	if len(got) != 2 {
		t.Fatalf("children len = %d, want 2: %+v", len(got), got)
	}
	if got[0].GetName() != "dev-cp-0" || got[0].GetApiVersion() != "proxmox.openctl.io/v1" {
		t.Errorf("children[0] = %+v", got[0])
	}
}

func TestResourceServiceGetSurfacesOwnerRefs(t *testing.T) {
	parent := &relationshipProvider{
		owned: map[string]string{"FakeKind:dev-cp-0": "dev"},
	}
	vm := &fakeProvider{
		getReturn: &protocol.Resource{
			APIVersion: "fake.openctl.io/v1",
			Kind:       "FakeKind",
			Metadata:   protocol.ResourceMetadata{Name: "dev-cp-0"},
		},
	}
	reg := providers.NewRegistry()
	reg.Register(parent)
	reg.Register(vm)

	addr, mat, mstore := startTestServerWithManifests(t, reg)
	conn := dialTestServer(t, addr, mat.CACertPath)
	defer func() { _ = conn.Close() }()

	// Owner promotion: dev-cp-0 isn't applied directly, but its owner
	// Cluster:dev is. The filter should treat the child as managed.
	if err := mstore.Save(context.Background(), &protocol.Resource{
		APIVersion: "k3s.openctl.io/v1",
		Kind:       "Cluster",
		Metadata:   protocol.ResourceMetadata{Name: "dev"},
	}); err != nil {
		t.Fatalf("Save owner: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := apiv1.NewResourceServiceClient(conn).Get(ctx, &apiv1.GetRequest{
		ApiVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
		Name:       "dev-cp-0",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	owners := resp.GetResource().GetMetadata().GetOwnerRefs()
	if len(owners) != 1 {
		t.Fatalf("owner_refs len = %d, want 1: %+v", len(owners), owners)
	}
	if owners[0].GetKind() != "Cluster" || owners[0].GetName() != "dev" {
		t.Errorf("owners[0] = %+v", owners[0])
	}
	if owners[0].GetApiVersion() != "k3s.openctl.io/v1" {
		t.Errorf("owner apiVersion = %q, want derived from registering provider", owners[0].GetApiVersion())
	}
}

func TestResourceServiceUnknownProviderRejected(t *testing.T) {
	reg := providers.NewRegistry()
	addr, mat := startTestServer(t, reg)
	conn := dialTestServer(t, addr, mat.CACertPath)
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := apiv1.NewResourceServiceClient(conn).Get(ctx, &apiv1.GetRequest{
		ApiVersion: "missing.openctl.io/v1",
		Kind:       "X",
		Name:       "y",
	})
	if err == nil {
		t.Fatal("want error for missing provider")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", st.Code())
	}
}

// Force fakeProvider to satisfy errors.As path expectations elsewhere if
// they ever get called with this type.
var _ = errors.As
