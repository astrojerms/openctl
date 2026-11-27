package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openctl/openctl/internal/controller/auth"
	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/operations"
	"github.com/openctl/openctl/internal/controller/providers"
	pmprovider "github.com/openctl/openctl/internal/controller/providers/proxmox"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func keyedDelete(key string) *apiv1.DeleteRequest {
	return &apiv1.DeleteRequest{RequestId: key, ApiVersion: "fake.openctl.io/v1", Kind: "FakeKind", Name: "requested"}
}

func TestDeleteRequestConcurrentReplayAdmitsOneRoot(t *testing.T) {
	provider := &fakeProvider{}
	h, lookup, _ := requestHandlers(t, provider, 0)
	ctx := auth.ContextWithPrincipal(context.Background(), auth.Principal{UserID: "alice", Role: auth.RoleEditor})
	if err := h.manifests.Save(ctx, safetyManifest("requested", 1)); err != nil {
		t.Fatal(err)
	}
	request := keyedDelete("one-delete")
	const count = 20
	type result struct {
		response *apiv1.DeleteResponse
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, count)
	for range count {
		go func() {
			<-start
			response, err := h.Delete(ctx, request)
			results <- result{response, err}
		}()
	}
	close(start)
	var operationID string
	for range count {
		result := <-results
		if result.err != nil {
			t.Fatalf("concurrent Delete: %v", result.err)
		}
		if operationID == "" {
			operationID = result.response.GetOperationId()
		}
		if result.response.GetOperationId() != operationID || result.response.GetRequestId() != request.GetRequestId() {
			t.Fatalf("wrong Delete acknowledgement: %+v", result.response)
		}
	}
	ui := metadata.NewIncomingContext(ctx, metadata.Pairs(sourceMetadataKey, manifests.SourceUI))
	if response, err := h.Delete(ui, request); err != nil || response.GetOperationId() != operationID {
		t.Fatalf("source changed Delete intent: %+v err=%v", response, err)
	}
	viewer := auth.ContextWithPrincipal(ctx, auth.Principal{UserID: "reader", Role: auth.RoleViewer})
	found, err := lookup.GetOperationByRequest(viewer, &apiv1.GetOperationByRequestRequest{RequestId: request.GetRequestId()})
	if err != nil || found.GetId() != operationID || found.GetRequestId() != request.GetRequestId() || found.GetType() != operations.TypeDelete || found.GetParentId() != "" {
		t.Fatalf("viewer could not recover the root Delete: %+v err=%v", found, err)
	}
	all, err := h.ops.List(ctx, operations.ListFilter{})
	if err != nil || len(all) != 1 || len(provider.deletes) != 0 || len(provider.gets) != 0 {
		t.Fatalf("admission mutated provider or duplicated work: %+v err=%v", all, err)
	}
}

func TestDeleteRequestCanonicalRootActor(t *testing.T) {
	h, _, db := requestHandlers(t, &fakeProvider{}, 0)
	ctx := context.Background()
	if err := h.manifests.Save(ctx, safetyManifest("requested", 1)); err != nil {
		t.Fatal(err)
	}
	session, err := auth.NewSessionStore(db).Create(ctx, auth.DefaultUserID, "same-operator", auth.RoleAdmin, 0)
	if err != nil {
		t.Fatal(err)
	}
	var operationID string
	for _, caller := range []context.Context{
		auth.ContextWithPrincipal(ctx, auth.RootPrincipal()),
		auth.ContextWithPrincipal(ctx, session.Principal()),
		ctx,
	} {
		response, err := h.Delete(caller, keyedDelete("root-delete"))
		if err != nil {
			t.Fatal(err)
		}
		if operationID == "" {
			operationID = response.GetOperationId()
		}
		if response.GetOperationId() != operationID {
			t.Fatal("root credential changes admitted another Delete")
		}
	}
}

func TestTerminalDeleteRequestReplaysWithoutLiveDeletionAuthority(t *testing.T) {
	for _, terminal := range []string{operations.StatusSucceeded, operations.StatusFailed, operations.StatusInterrupted, operations.StatusCancelled} {
		t.Run(terminal, func(t *testing.T) {
			ctx := context.Background()
			provider := &fakeProvider{}
			h, lookup, _ := requestHandlers(t, provider, 0)
			if err := h.manifests.Save(ctx, safetyManifest("requested", 1)); err != nil {
				t.Fatal(err)
			}
			request := keyedDelete("terminal-delete")
			first, err := h.Delete(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if terminal == operations.StatusInterrupted {
				if _, err := h.ops.ClaimNextPending(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err := h.ops.MarkRunningInterrupted(ctx); err != nil {
					t.Fatal(err)
				}
			} else if err := h.ops.Complete(ctx, first.GetOperationId(), terminal, "original result", ""); err != nil {
				t.Fatal(err)
			}
			if err := h.manifests.Delete(ctx, request.GetApiVersion(), request.GetKind(), request.GetName()); err != nil {
				t.Fatal(err)
			}
			// Completed Delete normally removes tracking. A replay must not need it.
			if response, err := h.Delete(ctx, request); err != nil || response.GetOperationId() != first.GetOperationId() {
				t.Fatalf("tracking removal blocked replay: %+v err=%v", response, err)
			}
			replacement := safetyManifest("requested", 2)
			replacement.Metadata.Labels = map[string]string{providers.LabelOwnerKind: "Cluster", providers.LabelOwnerName: "new-owner"}
			if err := h.manifests.Save(ctx, replacement); err != nil {
				t.Fatal(err)
			}
			later, err := h.ops.Submit(ctx, &operations.Operation{
				Type: operations.TypeApply, APIVersion: request.GetApiVersion(), Kind: request.GetKind(), ResourceName: request.GetName(),
			})
			if err != nil {
				t.Fatal(err)
			}
			provider.getErr = errors.New("provider unavailable after original Delete")
			h.manifestMode = "gitops"
			h.registry = providers.NewRegistry()
			response, err := h.Delete(ctx, request)
			if err != nil || response.GetOperationId() != first.GetOperationId() || response.GetRequestId() != request.GetRequestId() {
				t.Fatalf("replacement/provider/authority state changed replay: %+v err=%v", response, err)
			}
			found, err := lookup.GetOperationByRequest(ctx, &apiv1.GetOperationByRequestRequest{RequestId: request.GetRequestId()})
			if err != nil || found.GetStatus() != terminal || found.GetId() != first.GetOperationId() {
				t.Fatalf("terminal Delete recovery became a new attempt: %+v err=%v", found, err)
			}
			current, err := h.ops.Get(ctx, later.ID)
			if err != nil || current.Status != operations.StatusPending {
				t.Fatalf("replay changed later operation: %+v err=%v", current, err)
			}
			all, err := h.ops.List(ctx, operations.ListFilter{})
			if err != nil || len(all) != 2 || len(provider.deletes) != 0 || len(provider.gets) != 0 {
				t.Fatalf("replay admitted another Delete or touched provider: %+v err=%v", all, err)
			}
		})
	}
}

type deleteOwnershipProvider struct {
	fakeProvider
	check func() (string, string, bool, error)
}

func (p *deleteOwnershipProvider) CheckedOwnerOf(context.Context, string, string) (string, string, bool, error) {
	return p.check()
}

func TestDeleteAdmissionRaceRecoversCompletedRequest(t *testing.T) {
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	provider := &deleteOwnershipProvider{check: func() (string, string, bool, error) {
		close(entered)
		<-release
		return "", "", false, errors.New("ownership changed while another request finished")
	}}
	h, _, _ := requestHandlers(t, provider, 0)
	if err := h.manifests.Save(ctx, safetyManifest("requested", 1)); err != nil {
		t.Fatal(err)
	}
	request := keyedDelete("racing-delete")
	type result struct {
		response *apiv1.DeleteResponse
		err      error
	}
	completed := make(chan result, 1)
	go func() {
		response, err := h.Delete(ctx, request)
		completed <- result{response, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Delete did not reach mutable ownership check")
	}
	binding, err := operations.NewDeleteRequest(request.GetRequestId(), auth.DefaultUserID, request.GetApiVersion(), request.GetKind(), request.GetName())
	if err != nil {
		t.Fatal(err)
	}
	accepted, admitted, err := h.ops.SubmitRequest(ctx, &operations.Operation{
		Type: operations.TypeDelete, APIVersion: request.GetApiVersion(), Kind: request.GetKind(), ResourceName: request.GetName(),
	}, binding)
	if err != nil || !admitted {
		t.Fatalf("racing admission: %+v admitted=%v err=%v", accepted, admitted, err)
	}
	if err := h.ops.Complete(ctx, accepted.ID, operations.StatusSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.manifests.Delete(ctx, request.GetApiVersion(), request.GetKind(), request.GetName()); err != nil {
		t.Fatal(err)
	}
	unblock()
	select {
	case result := <-completed:
		if result.err != nil || result.response.GetOperationId() != accepted.ID || result.response.GetRequestId() != request.GetRequestId() {
			t.Fatalf("mutable check rejected committed identical admission: %+v err=%v", result.response, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("racing Delete did not return")
	}
}

func TestDeleteRequestConflictsAndAuthorization(t *testing.T) {
	ctx := auth.ContextWithPrincipal(context.Background(), auth.Principal{UserID: "alice", Role: auth.RoleEditor})
	h, _, _ := requestHandlers(t, &fakeProvider{}, 0)
	if err := h.manifests.Save(ctx, safetyManifest("requested", 1)); err != nil {
		t.Fatal(err)
	}
	request := keyedDelete("actor-action-target")
	first, err := h.Delete(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"actor", "apiVersion", "kind", "name"} {
		t.Run(field, func(t *testing.T) {
			changed := proto.Clone(request).(*apiv1.DeleteRequest)
			caller := ctx
			switch field {
			case "actor":
				caller = auth.ContextWithPrincipal(ctx, auth.Principal{UserID: "bob", Role: auth.RoleEditor})
			case "apiVersion":
				changed.ApiVersion = "unregistered.openctl.io/v1"
			case "kind":
				changed.Kind = "OtherKind"
			case "name":
				changed.Name = "other"
			}
			response, err := h.Delete(caller, changed)
			if status.Code(err) != codes.AlreadyExists || response != nil || !strings.Contains(err.Error(), first.GetOperationId()) {
				t.Fatalf("accepted Delete key reused: %+v err=%v", response, err)
			}
		})
	}
	if _, err := h.Apply(ctx, keyedApply(t, request.GetRequestId())); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("Delete key reused for Apply: %v", err)
	}
	apply, err := h.Apply(ctx, keyedApply(t, "apply-action"))
	if status.Code(err) != codes.AlreadyExists || apply != nil {
		t.Fatalf("in-flight Delete did not fence a new Apply: %+v err=%v", apply, err)
	}
	if err := h.ops.Complete(ctx, first.GetOperationId(), operations.StatusSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}
	apply, err = h.Apply(ctx, keyedApply(t, "apply-action"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Delete(ctx, keyedDelete("apply-action")); status.Code(err) != codes.AlreadyExists || !strings.Contains(err.Error(), apply.GetOperationId()) {
		t.Fatalf("Apply key reused for Delete: %v", err)
	}
	for _, role := range []auth.Role{auth.RoleViewer, auth.Role("unknown")} {
		caller := auth.ContextWithPrincipal(ctx, auth.Principal{UserID: "reader", Role: role})
		if _, err := h.Delete(caller, request); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("unauthorized actor replayed Delete: %v", err)
		}
	}
}

func TestDeleteRequestValidationAndLegacySync(t *testing.T) {
	provider := &fakeProvider{}
	h, lookup, _ := requestHandlers(t, provider, 0)
	ctx := context.Background()
	if err := h.manifests.Save(ctx, safetyManifest("requested", 1)); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{strings.Repeat("x", 129), "control\n", "non-ascii-ñ"} {
		if _, err := h.Delete(ctx, keyedDelete(key)); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid Delete key accepted: %v", err)
		}
	}
	for _, field := range []string{"apiVersion", "kind", "name"} {
		request := keyedDelete("missing-" + field)
		switch field {
		case "apiVersion":
			request.ApiVersion = ""
		case "kind":
			request.Kind = ""
		case "name":
			request.Name = ""
		}
		if _, err := h.Delete(ctx, request); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("missing %s accepted: %v", field, err)
		}
		if _, err := lookup.GetOperationByRequest(ctx, &apiv1.GetOperationByRequestRequest{RequestId: request.GetRequestId()}); status.Code(err) != codes.NotFound {
			t.Fatalf("invalid identity reserved a key: %v", err)
		}
	}
	for _, missing := range []string{"store", "dispatcher"} {
		copy := newResourceHandler(h.registry, h.ops, h.dispatcher, h.manifests)
		if missing == "store" {
			copy.ops = nil
		} else {
			copy.dispatcher = nil
		}
		if _, err := copy.Delete(ctx, keyedDelete("durable-required")); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("keyed Delete entered sync fallback without %s: %v", missing, err)
		}
	}
	if len(provider.deletes) != 0 || len(provider.gets) != 0 {
		t.Fatal("invalid keyed Delete called provider")
	}
	h.ops, h.dispatcher = nil, nil
	if response, err := h.Delete(ctx, keyedDelete("")); err != nil || response.GetRequestId() != "" || len(provider.deletes) != 1 {
		t.Fatalf("legacy unkeyed sync Delete changed: %+v err=%v", response, err)
	}
}

func TestFreshDeleteRequestsRetainAuthorityOwnershipAndDependencyGuards(t *testing.T) {
	for _, guard := range []string{"untracked", "tracking-required", "manifest-authority", "recorded-owner", "provider-owner", "ownership-unavailable", "dependent", "malformed-dependent", "active-dependent"} {
		t.Run(guard, func(t *testing.T) {
			ctx := context.Background()
			provider := &deleteOwnershipProvider{check: func() (string, string, bool, error) { return "", "", false, nil }}
			h, lookup, _ := requestHandlers(t, provider, 0)
			target := safetyManifest("requested", 1)
			if guard == "recorded-owner" {
				target.Metadata.Labels = map[string]string{providers.LabelOwnerName: "owner"}
			}
			if guard != "untracked" {
				if err := h.manifests.Save(ctx, target); err != nil {
					t.Fatal(err)
				}
			}
			expected := codes.FailedPrecondition
			switch guard {
			case "tracking-required":
				h.manifests = nil
			case "manifest-authority":
				h.manifestMode = "gitops"
			case "provider-owner":
				provider.check = func() (string, string, bool, error) { return "Cluster", "owner", true, nil }
			case "ownership-unavailable":
				provider.check = func() (string, string, bool, error) { return "", "", false, errors.New("ownership unavailable") }
			case "dependent", "malformed-dependent", "active-dependent":
				dependent := safetyManifest("dependent", 1)
				dependent.Spec["target"] = map[string]any{"$ref": map[string]any{"apiVersion": target.APIVersion, "kind": target.Kind, "name": target.Metadata.Name}}
				if guard == "malformed-dependent" {
					dependent.Spec["target"] = map[string]any{"$ref": map[string]any{"name": target.Metadata.Name}}
				}
				if guard == "active-dependent" {
					body, err := json.Marshal(dependent)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := h.ops.Submit(ctx, &operations.Operation{Type: operations.TypeApply, APIVersion: dependent.APIVersion, Kind: dependent.Kind, ResourceName: dependent.Metadata.Name, ManifestJSON: string(body)}); err != nil {
						t.Fatal(err)
					}
					expected = codes.AlreadyExists
				} else if err := h.manifests.Save(ctx, dependent); err != nil {
					t.Fatal(err)
				}
			}
			request := keyedDelete("fresh-" + guard)
			if _, err := h.Delete(ctx, request); status.Code(err) != expected {
				t.Fatalf("fresh keyed Delete bypassed %s: %v", guard, err)
			}
			if _, err := lookup.GetOperationByRequest(ctx, &apiv1.GetOperationByRequestRequest{RequestId: request.GetRequestId()}); status.Code(err) != codes.NotFound {
				t.Fatalf("rejected Delete reserved key: %v", err)
			}
			if len(provider.deletes) != 0 {
				t.Fatal("guard rejection called provider Delete")
			}
		})
	}
}

func TestAcceptedDeleteReplaysAfterForgetButFreshRequestIsFenced(t *testing.T) {
	pve := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		t.Errorf("Delete admission/replay unexpectedly contacted provider: %s", request.URL.Path)
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
	target := migrationVM()
	if err := h.manifests.Save(ctx, target); err != nil {
		t.Fatal(err)
	}
	request := &apiv1.DeleteRequest{RequestId: "delete-before-forget", ApiVersion: target.APIVersion, Kind: target.Kind, Name: target.Metadata.Name}
	first, err := h.Delete(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ops.Complete(ctx, first.GetOperationId(), operations.StatusCancelled, "", ""); err != nil {
		t.Fatal(err)
	}
	snapshot, err := h.manifests.ExportMigration(ctx, target.APIVersion, target.Kind, target.Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.manifests.Forget(ctx, snapshot.Resource, snapshot.ControllerID, snapshot.Digest); err != nil {
		t.Fatal(err)
	}
	if response, err := h.Delete(ctx, request); err != nil || response.GetOperationId() != first.GetOperationId() {
		t.Fatalf("release blocked accepted Delete replay: %+v err=%v", response, err)
	}
	request.RequestId = "fresh-after-forget"
	if _, err := h.Delete(ctx, request); status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "forgotten") {
		t.Fatalf("fresh Delete bypassed release fence: %v", err)
	}
	if _, err := lookup.GetOperationByRequest(ctx, &apiv1.GetOperationByRequestRequest{RequestId: request.GetRequestId()}); status.Code(err) != codes.NotFound {
		t.Fatalf("release rejection reserved key: %v", err)
	}
}

func TestPrunedDeleteRPCRemainsReserved(t *testing.T) {
	ctx := context.Background()
	provider := &fakeProvider{}
	h, lookup, _ := requestHandlers(t, provider, 1)
	if err := h.manifests.Save(ctx, safetyManifest("requested", 1)); err != nil {
		t.Fatal(err)
	}
	request := keyedDelete("pruned-delete")
	first, err := h.Delete(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ops.Complete(ctx, first.GetOperationId(), operations.StatusSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}
	later, err := h.Apply(ctx, keyedApply(t, "replacement-apply"))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ops.Complete(ctx, later.GetOperationId(), operations.StatusSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.manifests.Delete(ctx, request.GetApiVersion(), request.GetKind(), request.GetName()); err != nil {
		t.Fatal(err)
	}
	_, replayErr := h.Delete(ctx, request)
	_, lookupErr := lookup.GetOperationByRequest(ctx, &apiv1.GetOperationByRequestRequest{RequestId: request.GetRequestId()})
	for _, err := range []error{replayErr, lookupErr} {
		if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), first.GetOperationId()) {
			t.Fatalf("pruned Delete key became reusable: %v", err)
		}
	}
	request.Name = "different-target"
	if _, err := h.Delete(ctx, request); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("pruned Delete forgot original target: %v", err)
	}
	all, err := h.ops.List(ctx, operations.ListFilter{})
	if err != nil || len(all) != 1 || all[0].ID != later.GetOperationId() || len(provider.deletes) != 0 {
		t.Fatalf("pruned Delete replay changed history/provider: %+v err=%v", all, err)
	}
}

func TestLostDeleteHTTPAcknowledgementCannotDeleteReplacement(t *testing.T) {
	provider := &fakeProvider{}
	registry := providers.NewRegistry()
	registry.Register(provider)
	addr, material, intent := startTestServerWithManifests(t, registry)
	conn := dialTestServer(t, addr, material.CACertPath)
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := intent.Save(ctx, safetyManifest("requested", 1)); err != nil {
		t.Fatal(err)
	}
	ca, err := os.ReadFile(material.CACertPath)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := NewHTTPGateway(ctx, addr, ca, "localhost", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var drop atomic.Bool
	drop.Store(true)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/resources:delete" && drop.Swap(false) {
			// Complete real HTTP gateway/gRPC admission, then close the client
			// connection before transmitting any acknowledgement bytes.
			ack := httptest.NewRecorder()
			gateway.ServeHTTP(ack, r)
			if ack.Code != http.StatusOK {
				t.Errorf("Delete admission failed before dropped acknowledgement: %d %s", ack.Code, ack.Body.String())
			}
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack acknowledgement connection: %v", err)
				return
			}
			_ = connection.Close()
			return
		}
		gateway.ServeHTTP(w, r)
	}))
	defer proxy.Close()
	request := keyedDelete("lost-delete-http-ack")
	body, err := protojson.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(proxy.URL+"/v1/resources:delete", "application/json", bytes.NewReader(body))
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil {
		t.Fatal("client unexpectedly received the intentionally dropped Delete acknowledgement")
	}
	found, err := apiv1.NewOperationServiceClient(conn).GetOperationByRequest(ctx, &apiv1.GetOperationByRequestRequest{RequestId: request.GetRequestId()})
	if err != nil || found.GetType() != operations.TypeDelete || found.GetRequestId() != request.GetRequestId() {
		t.Fatalf("lost Delete acknowledgement was not recoverable: %+v err=%v", found, err)
	}
	if terminal := awaitOp(t, conn, found.GetId(), 3*time.Second); terminal.GetStatus() != operations.StatusSucceeded {
		t.Fatalf("recovered Delete did not succeed: %+v", terminal)
	}
	if saved, err := intent.Load(ctx, request.GetApiVersion(), request.GetKind(), request.GetName()); err != nil || saved != nil {
		t.Fatalf("successful Delete did not remove tracking: %+v err=%v", saved, err)
	}
	// A separately reviewed operation creates a replacement at the same name.
	replacement := keyedApply(t, "replacement-after-lost-delete-ack")
	replacement.Resource.Spec = &structpb.Struct{Fields: map[string]*structpb.Value{"value": structpb.NewNumberValue(2)}}
	created, err := apiv1.NewResourceServiceClient(conn).Apply(ctx, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if terminal := awaitOp(t, conn, created.GetOperationId(), 3*time.Second); terminal.GetStatus() != operations.StatusSucceeded {
		t.Fatalf("replacement Apply did not succeed: %+v", terminal)
	}
	response, err = http.Post(proxy.URL+"/v1/resources:delete", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	ack, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var replayed apiv1.DeleteResponse
	if err := protojson.Unmarshal(ack, &replayed); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || replayed.GetOperationId() != found.GetId() || replayed.GetRequestId() != request.GetRequestId() || len(provider.deletes) != 1 {
		t.Fatalf("lost acknowledgement replay deleted replacement: status=%d ack=%s deletes=%d", response.StatusCode, ack, len(provider.deletes))
	}
	saved, err := intent.Load(ctx, request.GetApiVersion(), request.GetKind(), request.GetName())
	if err != nil || saved == nil || saved.Spec["value"] != float64(2) {
		t.Fatalf("Delete replay changed replacement tracking: %+v err=%v", saved, err)
	}
}
