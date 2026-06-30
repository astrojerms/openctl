package server

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/operations"
	apiv1 "github.com/openctl/openctl/pkg/api/v1"
	"github.com/openctl/openctl/pkg/protocol"
)

// defaultWatchPollInterval is how often the v1 poll-based Watch
// implementation re-lists from the underlying store and emits diffs.
// Small enough to feel live in a UI; large enough not to hammer
// providers. Future iterations can replace this with notification
// hooks from the dispatcher (see UI.md U1).
const defaultWatchPollInterval = 500 * time.Millisecond

// Watch implements apiv1.ResourceServiceServer. Poll-based: lists the
// requested resources every poll-tick, diffs against the last snapshot,
// emits ADDED/MODIFIED/DELETED events. The first tick emits ADDED for
// every existing match so the client gets a snapshot before live deltas.
//
// Server-side terminates when the stream context is cancelled (client
// disconnect or grpc deadline). No goroutine outlives the stream.
func (h *resourceHandler) Watch(req *apiv1.WatchRequest, stream apiv1.ResourceService_WatchServer) error {
	if req.GetApiVersion() == "" || req.GetKind() == "" {
		return status.Error(codes.InvalidArgument, "api_version and kind are required")
	}
	if _, err := h.registry.For(req.GetApiVersion()); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	ctx := stream.Context()
	ticker := time.NewTicker(defaultWatchPollInterval)
	defer ticker.Stop()

	// snapshot: name -> hash. Detecting MODIFIED via the existing
	// manifests.Hash lets us compare on every poll without re-encoding
	// full specs.
	snapshot := map[string]string{}
	first := true

	emit := func(et apiv1.WatchEvent_Type, r *apiv1.Resource) error {
		return stream.Send(&apiv1.WatchEvent{Type: et, Resource: r})
	}

	for {
		matches, err := h.listForWatch(ctx, req.GetApiVersion(), req.GetKind(), req.GetName())
		if err != nil {
			// Transient list errors: log via gRPC and end the stream.
			return status.Errorf(codes.Internal, "list during watch: %v", err)
		}

		seen := map[string]string{}
		for _, m := range matches {
			r, err := resourceToProto(m)
			if err != nil {
				continue
			}
			// Drift surfaces as observed state changing; attach so MODIFIED
			// fires on drift transitions.
			_ = h.attachDrift(ctx, r, m)
			attachRelationships(h.registry, r)
			h := manifests.Hash(m)
			seen[m.Metadata.Name] = h

			prev, was := snapshot[m.Metadata.Name]
			switch {
			case first || !was:
				if err := emit(apiv1.WatchEvent_ADDED, r); err != nil {
					return err
				}
			case prev != h:
				if err := emit(apiv1.WatchEvent_MODIFIED, r); err != nil {
					return err
				}
			}
		}
		// Anything in the prior snapshot but not in `seen` was deleted.
		for name := range snapshot {
			if _, still := seen[name]; still {
				continue
			}
			if err := emit(apiv1.WatchEvent_DELETED, &apiv1.Resource{
				ApiVersion: req.GetApiVersion(),
				Kind:       req.GetKind(),
				Metadata:   &apiv1.Metadata{Name: name},
			}); err != nil {
				return err
			}
		}
		snapshot = seen
		first = false

		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// listForWatch returns the resources matching the watch filter — either
// all resources of the kind, or a single named resource. Errors from
// NotFound on a name-scoped watch are converted to "empty list" so the
// watcher fires DELETED rather than terminating. The managed-only filter
// (List/Get parity) applies here too: unmanaged resources never appear in
// the stream, so a Watch tail of an out-of-band resource looks identical
// to a real NotFound.
func (h *resourceHandler) listForWatch(ctx context.Context, apiVersion, kind, name string) ([]*protocol.Resource, error) {
	p, err := h.registry.For(apiVersion)
	if err != nil {
		return nil, err
	}
	if name != "" {
		r, err := p.Get(ctx, kind, name)
		if err != nil {
			// NotFound is normal during watch — return empty, let DELETED fire.
			return nil, nil // #nosec — intentional: surface as DELETED
		}
		if r == nil {
			return nil, nil
		}
		managed, mErr := h.isManaged(ctx, apiVersion, kind, name, nil, nil)
		if mErr != nil {
			return nil, mErr
		}
		if !managed {
			return nil, nil
		}
		return []*protocol.Resource{r}, nil
	}
	rs, err := p.List(ctx, kind)
	if err != nil {
		return nil, err
	}
	appliedNames, ownerCache, err := h.managedScope(ctx, apiVersion, kind)
	if err != nil {
		return nil, err
	}
	out := rs[:0]
	for _, r := range rs {
		managed, mErr := h.isManaged(ctx, r.APIVersion, r.Kind, r.Metadata.Name, appliedNames, ownerCache)
		if mErr != nil {
			return nil, mErr
		}
		if managed {
			out = append(out, r)
		}
	}
	return out, nil
}

// WatchOperations implements apiv1.OperationServiceServer. Poll-based,
// shape mirrors Watch — diff a "this id -> hash" snapshot per tick, emit
// events on adds/modifications. Deletes are ignored (ops aren't deleted;
// they're GC'd eventually, but a GC'd op was already terminal).
//
// When the request scopes to a single op id and that op reaches terminal
// status, the stream closes (terminal=true on the final event).
func (h *operationHandler) WatchOperations(req *apiv1.WatchOperationsRequest, stream apiv1.OperationService_WatchOperationsServer) error {
	ctx := stream.Context()
	ticker := time.NewTicker(defaultWatchPollInterval)
	defer ticker.Stop()

	type snapEntry struct {
		statusKey string // status + error + completedAt — captures terminal transitions
	}
	snapshot := map[string]snapEntry{}
	first := true

	for {
		ops, err := h.fetchOps(ctx, req)
		if err != nil {
			return status.Errorf(codes.Internal, "list ops during watch: %v", err)
		}

		seen := map[string]snapEntry{}
		var terminalForID bool
		for _, op := range ops {
			pb, err := opToProto(op)
			if err != nil {
				continue
			}
			if req.GetIncludeChildren() {
				children, _ := h.store.ListChildren(ctx, op.ID)
				for _, c := range children {
					if cpb, err := opToProto(c); err == nil {
						pb.Children = append(pb.Children, cpb)
					}
				}
			}
			key := op.Status + "|" + op.Error + "|" + op.CompletedAt
			seen[op.ID] = snapEntry{statusKey: key}
			prev, was := snapshot[op.ID]
			term := op.IsTerminal()
			switch {
			case first || !was:
				if err := stream.Send(&apiv1.OperationEvent{Operation: pb, Terminal: term}); err != nil {
					return err
				}
			case prev.statusKey != key:
				if err := stream.Send(&apiv1.OperationEvent{Operation: pb, Terminal: term}); err != nil {
					return err
				}
			}
			if req.GetId() != "" && op.ID == req.GetId() && term {
				terminalForID = true
			}
		}
		snapshot = seen
		first = false

		if terminalForID {
			return nil
		}

		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// fetchOps applies the WatchOperationsRequest filters to a List call.
// When the request scopes to a single op id, fetch directly via Get.
func (h *operationHandler) fetchOps(ctx context.Context, req *apiv1.WatchOperationsRequest) ([]*operations.Operation, error) {
	if req.GetId() != "" {
		op, err := h.store.Get(ctx, req.GetId())
		if err != nil {
			return nil, nil // gone (GC'd) - emit nothing
		}
		return []*operations.Operation{op}, nil
	}
	return h.store.List(ctx, operations.ListFilter{
		APIVersion:   req.GetApiVersion(),
		Kind:         req.GetKind(),
		ResourceName: req.GetResourceName(),
	})
}
