package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/internal/controller/providers"
	pmprovider "github.com/openctl/openctl/internal/controller/providers/proxmox"
	"github.com/openctl/openctl/internal/controller/storage"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
)

func requestHandlers(t *testing.T, provider providers.Provider, retain int) (*resourceHandler, *operationHandler, *sql.DB) {
	t.Helper()
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "requests.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	registry := providers.NewRegistry()
	registry.Register(provider)
	store := operations.New(db, retain)
	intent := manifests.New(db)
	// Admission tests do not start dispatch. The real dispatcher is exercised
	// separately by the lost-ack RPC test and the operations secret test.
	dispatcher := operations.NewDispatcher(store, registry, intent, time.Hour)
	return newResourceHandler(registry, store, dispatcher, intent), newOperationHandler(store, dispatcher), db
}

func keyedApply(t *testing.T, key string) *apiv1.ApplyRequest {
	t.Helper()
	spec, err := structpb.NewStruct(map[string]any{
		"password": map[string]any{"$secret": map[string]any{"file": "password-reference"}},
		"upstream": map[string]any{"$ref": map[string]any{
			"apiVersion": "fake.openctl.io/v1", "kind": "FakeKind", "name": "upstream", "field": "status.ip",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &apiv1.ApplyRequest{RequestId: key, Resource: &apiv1.Resource{
		ApiVersion: "fake.openctl.io/v1", Kind: "FakeKind",
		Metadata: &apiv1.Metadata{Name: "requested", Labels: map[string]string{"team": "home"}, Annotations: map[string]string{"note": "preserved"}},
		Spec:     spec,
	}}
}

func TestApplyRequestConcurrentReplayPreservesConsentInput(t *testing.T) {
	provider := &fakeProvider{}
	h, lookup, _ := requestHandlers(t, provider, 0)
	request := keyedApply(t, "concurrent-request")
	request.AllowDestructive = true
	request.IKnowThisBreaksTheCluster = true
	original := proto.Clone(request).(*apiv1.ApplyRequest)
	ctx := auth.ContextWithPrincipal(context.Background(), auth.Principal{UserID: "alice", Role: auth.RoleEditor})
	const count = 20
	type result struct {
		response *apiv1.ApplyResponse
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, count)
	for range count {
		go func() {
			<-start
			response, err := h.Apply(ctx, request)
			results <- result{response, err}
		}()
	}
	close(start)
	var operationID string
	for range count {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent Apply: %v", result.err)
		}
		if operationID == "" {
			operationID = result.response.GetOperationId()
		}
		if result.response.GetOperationId() != operationID || result.response.GetRequestId() != request.GetRequestId() {
			t.Fatalf("wrong request acknowledgement: %+v", result.response)
		}
	}
	if !proto.Equal(request, original) {
		t.Fatal("consent injection mutated the request and changed its retry fingerprint")
	}
	all, err := h.ops.List(ctx, operations.ListFilter{})
	if err != nil || len(all) != 1 {
		t.Fatalf("duplicate operations: rows=%d err=%v", len(all), err)
	}
	// Transport source is diagnostic only, not a different Apply intent.
	ui := metadata.NewIncomingContext(ctx, metadata.Pairs(sourceMetadataKey, manifests.SourceUI))
	if response, err := h.Apply(ui, request); err != nil || response.GetOperationId() != operationID {
		t.Fatalf("source change prevented replay: %+v err=%v", response, err)
	}
	found, err := lookup.GetOperationByRequest(ctx, &apiv1.GetOperationByRequestRequest{RequestId: request.GetRequestId()})
	if err != nil || found.GetId() != operationID || found.GetRequestId() != request.GetRequestId() {
		t.Fatalf("request lookup: %+v err=%v", found, err)
	}
	for _, marker := range []string{"$secret", "password-reference", "$ref", "status.ip", "openctl.io/allow-destructive", "openctl.io/i-know-this-breaks-the-cluster"} {
		if !strings.Contains(found.GetManifestJson(), marker) {
			t.Fatalf("lookup lost submitted marker %q", marker)
		}
	}
	if len(provider.applied) != 0 || len(provider.gets) != 0 {
		t.Fatal("admission/recovery called the provider")
	}
}

func TestCompletedCreateOnlyRequestReplaysBeforeMutableChecks(t *testing.T) {
	provider := &fakeProvider{}
	h, _, _ := requestHandlers(t, provider, 0)
	request := keyedApply(t, "initial-create")
	request.CreateOnly = true
	ctx := context.Background()
	first, err := h.Apply(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.manifests.Save(ctx, protoToResource(request.GetResource())); err != nil {
		t.Fatal(err)
	}
	if err := h.ops.Complete(ctx, first.GetOperationId(), operations.StatusSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}
	provider.getReturn = protoToResource(request.GetResource())
	provider.getErr = errors.New("provider reads unavailable after success")
	reads := len(provider.gets)
	replayed, err := h.Apply(ctx, request)
	if err != nil || replayed.GetOperationId() != first.GetOperationId() || replayed.GetRequestId() != request.GetRequestId() {
		t.Fatalf("completed initial create did not replay: %+v err=%v", replayed, err)
	}
	if len(provider.gets) != reads {
		t.Fatal("replay reached provider existence checks")
	}
}

type requestRaceProvider struct {
	fakeProvider
	entered chan struct{}
	release chan struct{}
	live    *protocol.Resource
}

func (p *requestRaceProvider) Get(context.Context, string, string) (*protocol.Resource, error) {
	close(p.entered)
	<-p.release
	return p.live, nil
}

func TestCreateOnlyAdmissionRaceRecoversAlreadyCompletedRequest(t *testing.T) {
	ctx := context.Background()
	request := keyedApply(t, "creation-race")
	request.CreateOnly = true
	provider := &requestRaceProvider{entered: make(chan struct{}), release: make(chan struct{}), live: protoToResource(request.GetResource())}
	h, _, _ := requestHandlers(t, provider, 0)
	type result struct {
		response *apiv1.ApplyResponse
		err      error
	}
	completed := make(chan result, 1)
	go func() {
		response, err := h.Apply(ctx, request)
		completed <- result{response, err}
	}()
	select {
	case <-provider.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Apply did not enter provider absence check")
	}
	released := false
	defer func() {
		if !released {
			close(provider.release)
		}
	}()
	// Another request handler admits and finishes this exact initial create
	// after the first handler's lookup, before its provider Get returns.
	binding, err := operations.NewApplyRequest(request.GetRequestId(), auth.DefaultUserID, provider.live, true, false, false)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(provider.live)
	if err != nil {
		t.Fatal(err)
	}
	accepted, admitted, err := h.ops.SubmitRequest(ctx, &operations.Operation{
		Type: operations.TypeApply, APIVersion: provider.live.APIVersion, Kind: provider.live.Kind,
		ResourceName: provider.live.Metadata.Name, ManifestJSON: string(body), CreateOnly: true,
	}, binding)
	if err != nil || !admitted {
		t.Fatalf("racing admission: %+v admitted=%v err=%v", accepted, admitted, err)
	}
	if err := h.manifests.Save(ctx, provider.live); err != nil {
		t.Fatal(err)
	}
	if err := h.ops.Complete(ctx, accepted.ID, operations.StatusSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}
	// Release once here; the deferred close handles early failures only.
	close(provider.release)
	released = true
	select {
	case result := <-completed:
		if result.err != nil || result.response.GetOperationId() != accepted.ID {
			t.Fatalf("completed create race was rejected rather than recovered: %+v err=%v", result.response, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("racing Apply did not return")
	}
}

func TestApplyRequestConflictsAndAuthorizationFences(t *testing.T) {
	provider := &fakeProvider{}
	h, lookup, _ := requestHandlers(t, provider, 0)
	request := keyedApply(t, "actor-bound")
	alice := auth.ContextWithPrincipal(context.Background(), auth.Principal{UserID: "alice", Role: auth.RoleEditor})
	first, err := h.Apply(alice, request)
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"actor", "spec", "metadata", "create-only", "destructive", "cluster"} {
		t.Run(flag, func(t *testing.T) {
			changed := proto.Clone(request).(*apiv1.ApplyRequest)
			ctx := alice
			switch flag {
			case "actor":
				ctx = auth.ContextWithPrincipal(ctx, auth.Principal{UserID: "bob", Role: auth.RoleEditor})
			case "spec":
				changed.Resource.Spec = &structpb.Struct{Fields: map[string]*structpb.Value{"value": structpb.NewStringValue("changed")}}
			case "metadata":
				changed.Resource.Metadata.Annotations["note"] = "different"
			case "create-only":
				changed.CreateOnly = true
			case "destructive":
				changed.AllowDestructive = true
			case "cluster":
				changed.IKnowThisBreaksTheCluster = true
			}
			if response, err := h.Apply(ctx, changed); status.Code(err) != codes.AlreadyExists || response != nil {
				t.Fatalf("accepted key was reused: %+v err=%v", response, err)
			}
		})
	}
	viewer := auth.ContextWithPrincipal(context.Background(), auth.Principal{UserID: "reader", Role: auth.RoleViewer})
	if _, err := h.Apply(viewer, request); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("viewer could replay an Apply mutation: %v", err)
	}
	h.manifestMode = "gitops"
	if _, err := h.Apply(alice, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("accepted key bypassed running GitOps authority: %v", err)
	}
	child, err := h.ops.BeginChild(alice, first.GetOperationId(), &operations.Operation{Type: operations.TypeStep, Kind: "FakeKind", ResourceName: "child"})
	if err != nil {
		t.Fatal(err)
	}
	for _, include := range []bool{false, true} {
		found, err := lookup.GetOperationByRequest(viewer, &apiv1.GetOperationByRequestRequest{RequestId: request.GetRequestId(), IncludeChildren: include})
		if err != nil || found.GetId() != first.GetOperationId() || found.GetManifestJson() == "" {
			t.Fatalf("viewer/GitOps observation was blocked: %+v err=%v", found, err)
		}
		if include && (len(found.GetChildren()) != 1 || found.GetChildren()[0].GetId() != child.ID) {
			t.Fatalf("lookup did not include requested children: %+v", found)
		}
		if !include && len(found.GetChildren()) != 0 {
			t.Fatal("lookup included children without consent")
		}
	}
	invalidRole := auth.ContextWithPrincipal(context.Background(), auth.Principal{UserID: "invalid", Role: auth.Role("unknown")})
	if _, err := lookup.GetOperationByRequest(invalidRole, &apiv1.GetOperationByRequestRequest{RequestId: request.GetRequestId()}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("invalid role could observe request: %v", err)
	}
	if len(provider.applied) != 0 || len(provider.gets) != 0 {
		t.Fatal("request conflict or observation crossed the provider fence")
	}
}

func TestApplyRequestValidationAndLegacySync(t *testing.T) {
	provider := &fakeProvider{}
	h, lookup, _ := requestHandlers(t, provider, 0)
	for _, key := range []string{strings.Repeat("x", 129), "control\n", "non-ascii-ñ"} {
		if _, err := h.Apply(context.Background(), keyedApply(t, key)); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid Apply key accepted: %v", err)
		}
		if _, err := lookup.GetOperationByRequest(context.Background(), &apiv1.GetOperationByRequestRequest{RequestId: key}); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid lookup key accepted: %v", err)
		}
	}
	if _, err := lookup.GetOperationByRequest(context.Background(), &apiv1.GetOperationByRequestRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing lookup key: %v", err)
	}
	if _, err := lookup.GetOperationByRequest(context.Background(), &apiv1.GetOperationByRequestRequest{RequestId: "unaccepted"}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown lookup key was not NotFound: %v", err)
	}
	if _, err := h.Apply(context.Background(), &apiv1.ApplyRequest{RequestId: "missing-resource"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing resource accepted: %v", err)
	}
	if _, err := lookup.GetOperationByRequest(context.Background(), &apiv1.GetOperationByRequestRequest{RequestId: "missing-resource"}); status.Code(err) != codes.NotFound {
		t.Fatal("validation failure reserved a request key")
	}
	for _, missing := range []string{"store", "dispatcher"} {
		handler := newResourceHandler(h.registry, h.ops, h.dispatcher, h.manifests)
		if missing == "store" {
			handler.ops = nil
		} else {
			handler.dispatcher = nil
		}
		if _, err := handler.Apply(context.Background(), keyedApply(t, "durable-required")); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("keyed Apply entered unsafe sync fallback without %s: %v", missing, err)
		}
	}
	if len(provider.applied) != 0 || len(provider.gets) != 0 {
		t.Fatal("invalid keyed requests reached the provider")
	}
	legacy := keyedApply(t, "")
	h.ops, h.dispatcher = nil, nil
	if response, err := h.Apply(context.Background(), legacy); err != nil || response.GetRequestId() != "" || len(provider.applied) != 1 {
		t.Fatalf("unkeyed legacy sync behavior changed: %+v err=%v", response, err)
	}
}

func TestPrunedRequestRPCFailsClosedWithOriginalOperation(t *testing.T) {
	provider := &fakeProvider{}
	h, lookup, _ := requestHandlers(t, provider, 1)
	ctx := context.Background()
	request := keyedApply(t, "pruned-request")
	first, err := h.Apply(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ops.Complete(ctx, first.GetOperationId(), operations.StatusSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}
	secondRequest := proto.Clone(request).(*apiv1.ApplyRequest)
	secondRequest.RequestId = "new-reviewed-request"
	second, err := h.Apply(ctx, secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ops.Complete(ctx, second.GetOperationId(), operations.StatusSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}
	_, lookupErr := lookup.GetOperationByRequest(ctx, &apiv1.GetOperationByRequestRequest{RequestId: request.GetRequestId()})
	_, replayErr := h.Apply(ctx, request)
	for _, err := range []error{lookupErr, replayErr} {
		if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), first.GetOperationId()) {
			t.Fatalf("pruned binding was not actionable: %v", err)
		}
	}
	request.AllowDestructive = true
	if _, err := h.Apply(ctx, request); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("pruned binding forgot original intent: %v", err)
	}
}

func TestAcceptedRequestReplaysAfterForgetButNewRequestIsFenced(t *testing.T) {
	pve := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		t.Errorf("request admission/replay unexpectedly contacted the provider: %s", request.URL.Path)
		http.Error(w, "no provider calls expected", http.StatusMethodNotAllowed)
	}))
	defer pve.Close()
	provider, err := pmprovider.NewMulti(map[string]*pmprovider.Config{
		"source": {Endpoint: pve.URL, CAFile: proxmoxFixtureCA(t, pve), TokenID: "fixture", TokenSecret: "fixture"},
	}, "source")
	if err != nil {
		t.Fatal(err)
	}
	h, lookup, _ := requestHandlers(t, provider, 0)
	ctx := context.Background()
	request := keyedApply(t, "accepted-before-forget")
	desired := migrationVM()
	request.Resource.ApiVersion, request.Resource.Kind = desired.APIVersion, desired.Kind
	request.Resource.Spec, err = structpb.NewStruct(desired.Spec)
	if err != nil {
		t.Fatal(err)
	}
	first, err := h.Apply(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ops.Complete(ctx, first.GetOperationId(), operations.StatusSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.manifests.Save(ctx, protoToResource(request.GetResource())); err != nil {
		t.Fatal(err)
	}
	snapshot, err := h.manifests.ExportMigration(ctx, request.GetResource().GetApiVersion(), request.GetResource().GetKind(), request.GetResource().GetMetadata().GetName())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.manifests.Forget(ctx, snapshot.Resource, snapshot.ControllerID, snapshot.Digest); err != nil {
		t.Fatal(err)
	}
	if response, err := h.Apply(ctx, request); err != nil || response.GetOperationId() != first.GetOperationId() {
		t.Fatalf("accepted replay was blocked by later release: %+v err=%v", response, err)
	}
	request.RequestId = "not-accepted-after-forget"
	if _, err := h.Apply(ctx, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("new request bypassed the release fence: %v", err)
	}
	if _, err := lookup.GetOperationByRequest(ctx, &apiv1.GetOperationByRequestRequest{RequestId: request.GetRequestId()}); status.Code(err) != codes.NotFound {
		t.Fatal("release rejection reserved a key")
	}
}

func TestLostApplyAcknowledgementRecoversThroughRequestRPC(t *testing.T) {
	provider := &fakeProvider{}
	registry := providers.NewRegistry()
	registry.Register(provider)
	addr, material := startTestServer(t, registry)
	conn := dialTestServer(t, addr, material.CACertPath)
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request := keyedApply(t, "lost-response-request")
	request.Resource.Spec = &structpb.Struct{Fields: map[string]*structpb.Value{"value": structpb.NewNumberValue(1)}}
	client := apiv1.NewResourceServiceClient(conn)
	// Intentionally discard the operation identity, as if the HTTP response
	// had been lost. Recovery has only the client-persisted request ID.
	if _, err := client.Apply(ctx, request); err != nil {
		t.Fatal(err)
	}
	found, err := apiv1.NewOperationServiceClient(conn).GetOperationByRequest(ctx, &apiv1.GetOperationByRequestRequest{RequestId: request.GetRequestId()})
	if err != nil || found.GetRequestId() != request.GetRequestId() || found.GetManifestJson() == "" {
		t.Fatalf("lost acknowledgement recovery: %+v err=%v", found, err)
	}
	terminal := awaitOp(t, conn, found.GetId(), 3*time.Second)
	if terminal.GetStatus() != operations.StatusSucceeded {
		t.Fatalf("recovered operation did not succeed: %s", terminal.GetStatus())
	}
	replayed, err := client.Apply(ctx, request)
	if err != nil || replayed.GetOperationId() != found.GetId() || len(provider.applied) != 1 {
		t.Fatalf("lost response caused duplicate provider mutation: %+v applies=%d err=%v", replayed, len(provider.applied), err)
	}
}
