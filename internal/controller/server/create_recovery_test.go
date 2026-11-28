package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/openctl/openctl/internal/controller/providers"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
)

type partialCreationProvider struct {
	fakeProvider
	mu       sync.Mutex
	live     *protocol.Resource
	foreign  bool
	attempts int
}

func (p *partialCreationProvider) Get(context.Context, string, string) (*protocol.Resource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.live == nil {
		return nil, providers.NotFound("FakeKind", "x")
	}
	return p.live, nil
}

func (p *partialCreationProvider) CanResumeCreate(context.Context, string, string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.live != nil && !p.foreign, nil
}

func (p *partialCreationProvider) Apply(_ context.Context, r *protocol.Resource) (*protocol.Resource, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.attempts++
	if p.foreign {
		return nil, errors.New("unmanaged deployment must not be overwritten")
	}
	out := *r
	out.Status = map[string]any{"ready": p.attempts > 1}
	p.live = &out
	if p.attempts == 1 {
		return nil, errors.New("rollout interrupted after ownership claim")
	}
	return p.live, nil
}

func TestCreateOnlyRecoversOwnedPartialCreationWithoutOverwritingExisting(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned partial rollout", true: "unmanaged deployment"}[foreign], func(t *testing.T) {
			p := &partialCreationProvider{foreign: foreign}
			if foreign {
				p.live = &protocol.Resource{
					APIVersion: "fake.openctl.io/v1", Kind: "FakeKind",
					Metadata: protocol.ResourceMetadata{Name: "x"}, Spec: map[string]any{"value": "foreign"},
				}
			}
			registry := providers.NewRegistry()
			registry.Register(p)
			addr, material, intent := startTestServerWithManifests(t, registry)
			conn := dialTestServer(t, addr, material.CACertPath)
			defer conn.Close()
			client := apiv1.NewResourceServiceClient(conn)
			spec, err := structpb.NewStruct(map[string]any{"value": "desired"})
			if err != nil {
				t.Fatal(err)
			}
			request := &apiv1.ApplyRequest{
				RequestId: "initial-create", CreateOnly: true,
				Resource: &apiv1.Resource{
					ApiVersion: "fake.openctl.io/v1", Kind: "FakeKind",
					Metadata: &apiv1.Metadata{Name: "x"}, Spec: spec,
				},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			first, err := client.Apply(ctx, request)
			if foreign {
				if status.Code(err) != codes.AlreadyExists {
					t.Fatalf("unmanaged create must be rejected: %+v, %v", first, err)
				}
				p.mu.Lock()
				defer p.mu.Unlock()
				if p.attempts != 0 || p.live.Spec["value"] != "foreign" {
					t.Fatal("create overwrote unmanaged deployment")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if op := awaitOp(t, conn, first.GetOperationId(), 10*time.Second); op.GetStatus() != "failed" {
				t.Fatalf("expected the partial first rollout to fail: %+v", op)
			}
			retry := proto.Clone(request).(*apiv1.ApplyRequest)
			retry.RequestId = "reviewed-partial-recovery"
			second, err := client.Apply(ctx, retry)
			if err != nil {
				t.Fatalf("owned first-create recovery rejected: %v", err)
			}
			if op := awaitOp(t, conn, second.GetOperationId(), 10*time.Second); op.GetStatus() != "succeeded" {
				t.Fatalf("owned recovery failed: %+v", op)
			}
			saved, err := intent.Load(ctx, "fake.openctl.io/v1", "FakeKind", "x")
			if err != nil || saved == nil || saved.Spec["value"] != "desired" {
				t.Fatalf("recovery did not save the desired baseline: %+v, %v", saved, err)
			}
			retry.RequestId = "cannot-recreate-completed-deployment"
			if _, err := client.Apply(ctx, retry); status.Code(err) != codes.AlreadyExists {
				t.Fatalf("create-only updated completed deployment: %v", err)
			}
			replayed, err := client.Apply(ctx, request)
			if err != nil || replayed.GetOperationId() != first.GetOperationId() {
				t.Fatalf("original failed request did not replay its original outcome: %+v, %v", replayed, err)
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.attempts != 2 {
				t.Fatalf("create/recovery/replay executed %d mutations, expected exactly two", p.attempts)
			}
		})
	}
}
