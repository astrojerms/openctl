package server

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/openctl/openctl/internal/controller/providers"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
)

// Replace complete immutable observations under the existing watch fixture's
// lock; reference changes must not mutate the application's live observation.
type referenceDriftWatchLookup struct{ *watchableProvider }

func (*referenceDriftWatchLookup) Name() string    { return "proxmox" }
func (*referenceDriftWatchLookup) Kinds() []string { return []string{"VirtualMachine"} }

func TestReferenceDriftWatchEmitsReferenceOnlyTransitions(t *testing.T) {
	desired := referenceDriftManifest()
	app := newWatchableProvider()
	observed := &protocol.Resource{
		APIVersion: desired.APIVersion, Kind: desired.Kind, Metadata: desired.Metadata,
		Spec: map[string]any{"target": map[string]any{"host": "192.168.11.125"}},
	}
	app.preload(observed)
	lookup := &referenceDriftWatchLookup{newWatchableProvider()}
	lookup.preload(referenceDriftGuest("192.168.11.125"))
	reg := providers.NewRegistry()
	reg.Register(app)
	reg.Register(lookup)
	addr, mat, store := startTestServerWithManifests(t, reg)
	if err := store.Save(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	conn := dialTestServer(t, addr, mat.CACertPath)
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	stream, err := apiv1.NewResourceServiceClient(conn).Watch(ctx, &apiv1.WatchRequest{
		ApiVersion: desired.APIVersion, Kind: desired.Kind, Name: desired.Metadata.Name,
	})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		event *apiv1.WatchEvent
		err   error
	}
	results := make(chan result, 8)
	go func() {
		for {
			event, err := stream.Recv()
			results <- result{event: event, err: err}
			if err != nil {
				return
			}
		}
	}()
	next := func() result {
		t.Helper()
		select {
		case got := <-results:
			return got
		case <-time.After(3 * time.Second):
			t.Fatal("timeout waiting for reference drift transition")
			return result{}
		}
	}
	first := next()
	if first.err != nil || first.event.GetType() != apiv1.WatchEvent_ADDED {
		t.Fatalf("initial snapshot: %v %v", first.event, first.err)
	}
	assertReferenceHostDrift(t, first.event.Resource.Drift, "192.168.11.125")

	lookup.preload(referenceDriftGuest("192.168.11.126"))
	changed := next()
	if changed.err != nil || changed.event.GetType() != apiv1.WatchEvent_MODIFIED {
		t.Fatalf("reference-only change: %v %v", changed.event, changed.err)
	}
	assertReferenceHostDrift(t, changed.event.Resource.Drift, "192.168.11.126")
	select {
	case duplicate := <-results:
		t.Fatalf("unchanged reference emitted duplicate: %v %v", duplicate.event, duplicate.err)
	case <-time.After(defaultWatchPollInterval + 150*time.Millisecond):
	}

	lookup.preload(referenceDriftGuest("192.168.11.125"))
	restored := next()
	if restored.err != nil || restored.event.GetType() != apiv1.WatchEvent_MODIFIED {
		t.Fatalf("reference convergence: %v %v", restored.event, restored.err)
	}
	assertReferenceHostDrift(t, restored.event.Resource.Drift, "192.168.11.125")

	lookup.remove("guest")
	failed := next()
	if failed.event != nil || status.Code(failed.err) != codes.FailedPrecondition || !strings.Contains(failed.err.Error(), "spec.target.host") {
		t.Fatalf("unresolvable reference emitted false healthy state: %v %v", failed.event, failed.err)
	}
	saved, err := store.Load(context.Background(), desired.APIVersion, desired.Kind, desired.Metadata.Name)
	if err != nil || saved == nil || !reflect.DeepEqual(saved.Spec, desired.Spec) {
		t.Fatalf("watch changed saved reference markers: %v %v", saved, err)
	}
	live, err := app.Get(context.Background(), desired.Kind, desired.Metadata.Name)
	if err != nil || live != observed || live.Spec["target"].(map[string]any)["host"] != "192.168.11.125" {
		t.Fatal("watch modified live observation")
	}
}

func TestReferenceDriftWatchResolutionFailureIsVisible(t *testing.T) {
	desired := referenceDriftManifest()
	client, _, store, app, lookup := referenceDriftFixture(t, desired, nil, errors.New("PRIVATE-TOKEN PRIVATE-BODY"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.Watch(ctx, &apiv1.WatchRequest{
		ApiVersion: desired.APIVersion, Kind: desired.Kind, Name: desired.Metadata.Name,
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err := stream.Recv()
	if event != nil || status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "spec.target.host") || strings.Contains(err.Error(), "PRIVATE-") {
		t.Fatalf("watch swallowed or leaked reference failure: %v %v", event, err)
	}
	assertReferenceInspectionReadOnly(t, desired, store, app.fakeProvider, lookup.fakeProvider)
}
