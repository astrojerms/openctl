package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/openctl/openctl/internal/controller/providers"
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
}

func (f *fakeProvider) Name() string    { return "fake" }
func (f *fakeProvider) Kinds() []string { return []string{"FakeKind"} }

func (f *fakeProvider) Apply(_ context.Context, m *protocol.Resource) (*protocol.Resource, error) {
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

func startTestServer(t *testing.T, registry *providers.Registry) (string, *tlspkg.Material) {
	t.Helper()
	dir := t.TempDir()
	mat, err := tlspkg.EnsureMaterial(dir, "localhost", []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{
		CertFile: mat.ServerCertPath,
		KeyFile:  mat.ServerKeyPath,
		Token:    "",
		Registry: registry,
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
	return ln.Addr().String(), mat
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

func TestResourceServiceApplyRoutesToProvider(t *testing.T) {
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

	// FakeKind has no embedded CUE schema → Validate is a no-op pass.
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
	if resp.GetMessage() == "" {
		t.Error("response message is empty")
	}
	if len(fake.applied) != 1 {
		t.Fatalf("provider Apply not called: %d", len(fake.applied))
	}
	if fake.applied[0].Metadata.Name != "x" {
		t.Errorf("applied name = %q, want x", fake.applied[0].Metadata.Name)
	}
	if got := resp.GetResource().GetStatus().AsMap()["applied"]; got != true {
		t.Errorf("response status.applied = %v, want true", got)
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

	addr, mat := startTestServer(t, reg)
	conn := dialTestServer(t, addr, mat.CACertPath)
	defer func() { _ = conn.Close() }()

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

func TestResourceServiceDeletePassesThrough(t *testing.T) {
	fake := &fakeProvider{}
	reg := providers.NewRegistry()
	reg.Register(fake)

	addr, mat := startTestServer(t, reg)
	conn := dialTestServer(t, addr, mat.CACertPath)
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := apiv1.NewResourceServiceClient(conn).Delete(ctx, &apiv1.DeleteRequest{
		ApiVersion: "fake.openctl.io/v1",
		Kind:       "FakeKind",
		Name:       "x",
	})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(fake.deletes) != 1 || fake.deletes[0][1] != "x" {
		t.Errorf("delete not recorded correctly: %v", fake.deletes)
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
