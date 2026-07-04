package operations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/internal/controller/refs"
	"github.com/openctl/openctl/pkg/protocol"
)

// DefaultPollInterval is the dispatcher's tick rate when no Notify wakes it.
// Keep small enough to feel responsive; large enough to avoid burning CPU.
const DefaultPollInterval = 500 * time.Millisecond

// ManifestSink lets the dispatcher persist desired-state on apply/delete
// success and look up the verifying-trace cache hash. Kept as a narrow
// interface so this package doesn't depend on internal/controller/manifests
// (the wiring lives in cmd/openctl-controller). nil is permitted — tests
// skip the sink, which also disables the cache.
type ManifestSink interface {
	Save(ctx context.Context, r *protocol.Resource) error
	// SaveWithRefsHash is Save plus the resolved-refs hash used by the
	// Phase 8 refs-invalidation dimension of the verifying-trace cache.
	// Callers that don't have a resolved-refs hash (disk mirror, GitOps
	// watcher) can pass refsHash="" — the store treats an empty stored
	// value as "unknown; force a cache miss on the next apply".
	SaveWithRefsHash(ctx context.Context, r *protocol.Resource, refsHash string) error
	Delete(ctx context.Context, apiVersion, kind, name string) error
	// LoadHash returns the input hash recorded by the previous successful
	// apply for this resource, or "" if no prior apply is on file.
	LoadHash(ctx context.Context, apiVersion, kind, name string) (string, error)
	// LoadHashes returns both stored hashes: input_hash (raw manifest)
	// and refs_hash (resolved ref values). Either may be "" if the row
	// predates that dimension of the cache.
	LoadHashes(ctx context.Context, apiVersion, kind, name string) (inputHash, refsHash string, err error)
	// Hash computes the verifying-trace key for a manifest. Must be a pure
	// function of the manifest's apply input.
	Hash(r *protocol.Resource) string
}

// Dispatcher pulls pending operations from the Store and runs them through
// the appropriate Provider. Runs as a single goroutine started by Start;
// Stop blocks until in-flight ops finish (or ctx cancels first).
type Dispatcher struct {
	store        *Store
	registry     *providers.Registry
	manifests    ManifestSink
	pollInterval time.Duration

	notify  chan struct{}
	done    chan struct{}
	stopMu  sync.Mutex
	stopped bool

	// runMu guards running + userCancelled. running holds a cancel func per
	// in-flight op so a user CancelOperation can abort a running op's
	// context. userCancelled records the ids a user explicitly canceled (vs.
	// a controller shutdown), so execute() completes those as Canceled
	// rather than Failed.
	runMu         sync.Mutex
	running       map[string]context.CancelFunc
	userCancelled map[string]bool
}

// NewDispatcher constructs a Dispatcher. pollInterval==0 uses
// DefaultPollInterval. manifests may be nil (tests).
func NewDispatcher(store *Store, registry *providers.Registry, manifests ManifestSink, pollInterval time.Duration) *Dispatcher {
	if pollInterval == 0 {
		pollInterval = DefaultPollInterval
	}
	return &Dispatcher{
		store:         store,
		registry:      registry,
		manifests:     manifests,
		pollInterval:  pollInterval,
		notify:        make(chan struct{}, 1),
		done:          make(chan struct{}),
		running:       make(map[string]context.CancelFunc),
		userCancelled: make(map[string]bool),
	}
}

// CancelRunning aborts the context of an in-flight op, if one with this id is
// currently executing, and marks it as user-canceled so execute() records it
// as Canceled rather than Failed. Returns true when a running op was
// signaled. Cancellation is cooperative — the op stops as soon as its
// provider yields to the canceled context (proxmox threads ctx through its
// HTTP client; k3s checks ctx between install steps), so a long SSH step may
// run to completion before the cancel takes effect.
func (d *Dispatcher) CancelRunning(id string) bool {
	d.runMu.Lock()
	cancel, ok := d.running[id]
	if ok {
		d.userCancelled[id] = true
	}
	d.runMu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// Start launches the dispatcher goroutine. It runs until ctx cancels or
// Stop is called.
func (d *Dispatcher) Start(ctx context.Context) {
	go d.run(ctx)
}

// Stop signals the dispatcher to stop and blocks until it does. Safe to
// call multiple times.
func (d *Dispatcher) Stop() {
	d.stopMu.Lock()
	if d.stopped {
		d.stopMu.Unlock()
		return
	}
	d.stopped = true
	d.stopMu.Unlock()
	<-d.done
}

// Notify wakes the dispatcher immediately so the caller doesn't have to
// wait for the next poll tick. Called by the server's Apply/Delete after
// inserting a new pending op.
func (d *Dispatcher) Notify() {
	select {
	case d.notify <- struct{}{}:
	default:
		// channel already has a pending notify; coalesce.
	}
}

func (d *Dispatcher) run(ctx context.Context) {
	defer close(d.done)
	ticker := time.NewTicker(d.pollInterval)
	defer ticker.Stop()

	for {
		d.drain(ctx)

		// Check stop flag without blocking — Stop sets stopped=true and the
		// timer/notify channels won't fire.
		d.stopMu.Lock()
		stopped := d.stopped
		d.stopMu.Unlock()
		if stopped {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-d.notify:
		}
	}
}

func (d *Dispatcher) drain(ctx context.Context) {
	for {
		op, err := d.store.ClaimNextPending(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return
		}
		if err != nil {
			log.Printf("dispatcher: claim error: %v", err)
			return
		}
		d.execute(ctx, op)
	}
}

// tryCacheHitInline is a pure cache-check: on hit returns the cached
// provider.Get result and hit=true; on miss returns (nil, false). No op
// status is touched here — callers that own an op row (execute()) handle
// completion themselves; ApplyManifest uses this to serve child manifests
// through the same cache without an op row.
//
// Two-dimensional cache (Phase 8 step 5):
//   - input_hash of the RAW manifest must match the stored value
//     (user intent unchanged)
//   - refs_hash of the RESOLVED manifest must match the stored value
//     (upstream ref targets haven't updated their status.*)
//
// Additional miss conditions:
//   - manifests sink not configured (tests)
//   - no prior apply recorded for this resource
//   - manifest carries the i-know-this-breaks-the-cluster annotation
//     (treated as "user is forcing a destructive operation" — never
//     short-circuit those)
func (d *Dispatcher) tryCacheHitInline(ctx context.Context, p providers.Provider, raw, resolved *protocol.Resource) (*protocol.Resource, bool) {
	if d.manifests == nil {
		return nil, false
	}
	if raw.Metadata.Annotations["openctl.io/i-know-this-breaks-the-cluster"] == "true" {
		return nil, false
	}
	storedInput, storedRefs, err := d.manifests.LoadHashes(ctx, raw.APIVersion, raw.Kind, raw.Metadata.Name)
	if err != nil || storedInput == "" {
		return nil, false
	}
	if d.manifests.Hash(raw) != storedInput {
		return nil, false
	}
	if storedRefs == "" || d.manifests.Hash(resolved) != storedRefs {
		return nil, false
	}
	result, err := p.Get(ctx, resolved.Kind, resolved.Metadata.Name)
	if err != nil || result == nil {
		return nil, false
	}
	return result, true
}

// ApplyManifest runs the full Apply pipeline (resolve refs → cache check
// → provider.Apply → save state) for a single manifest and returns the
// applied resource. Does not touch operation status.
//
// Two call sites:
//  1. execute() for top-level ops (wraps the result in op completion).
//  2. Composite providers via the ChildDispatcher on ctx, to run Plan()
//     child manifests through the same pipeline without spawning
//     separate ops. This gives children per-resource cache hits, ref
//     resolution, and manifest persistence for free.
//
// Errors:
//   - unknown apiVersion (no provider) → wrapped as "no provider for apiVersion"
//   - ref resolve failure → wrapped as "resolve refs"
//   - provider.Apply failure → returned unwrapped so callers see the
//     underlying error
func (d *Dispatcher) ApplyManifest(ctx context.Context, raw *protocol.Resource) (*protocol.Resource, error) {
	p, err := d.registry.For(raw.APIVersion)
	if err != nil {
		return nil, fmt.Errorf("no provider for apiVersion %q: %w", raw.APIVersion, err)
	}
	resolved := *raw
	if resolver := refs.New(d.registry); raw.Spec != nil {
		resolvedSpec, err := resolver.Resolve(ctx, raw.Spec)
		if err != nil {
			return nil, fmt.Errorf("resolve refs: %w", err)
		}
		resolved.Spec = resolvedSpec
	}
	if cached, hit := d.tryCacheHitInline(ctx, p, raw, &resolved); hit {
		return cached, nil
	}
	result, err := p.Apply(ctx, &resolved)
	if err != nil {
		return nil, err
	}
	if d.manifests != nil {
		refsHash := d.manifests.Hash(&resolved)
		if err := d.manifests.SaveWithRefsHash(ctx, raw, refsHash); err != nil {
			log.Printf("dispatcher: save manifest for %s %q: %v", raw.Kind, raw.Metadata.Name, err)
		}
	}
	return result, nil
}

// DeleteManifest routes a single manifest through provider.Delete and
// removes it from the manifest store — the delete-direction mirror of
// ApplyManifest, and the same pipeline the top-level TypeDelete op runs.
//
// Used by composite providers via the ChildDispatcher on ctx to remove
// Plan()-emitted children (scale-down, the respec destroy step) without
// spawning separate delete ops. No ref resolution or cache is involved:
// Delete takes only kind+name, and there is nothing to cache.
//
// Idempotent to the extent the provider's Delete is — providers return
// nil for an already-absent resource — so deleting a child twice is safe.
// A manifest-store removal failure is logged, not returned, matching the
// top-level TypeDelete path: the resource is already gone from the
// provider, so a lingering manifest row is a soft error, not a reason to
// fail the caller.
func (d *Dispatcher) DeleteManifest(ctx context.Context, raw *protocol.Resource) error {
	p, err := d.registry.For(raw.APIVersion)
	if err != nil {
		return fmt.Errorf("no provider for apiVersion %q: %w", raw.APIVersion, err)
	}
	if err := p.Delete(ctx, raw.Kind, raw.Metadata.Name); err != nil {
		return err
	}
	if d.manifests != nil {
		if err := d.manifests.Delete(ctx, raw.APIVersion, raw.Kind, raw.Metadata.Name); err != nil {
			log.Printf("dispatcher: delete manifest for %s %q: %v", raw.Kind, raw.Metadata.Name, err)
		}
	}
	return nil
}

// terminalOutcome maps a provider error to the op's terminal status +
// message: StatusCancelled with a clean message when the op was canceled by
// a user while running (its context was aborted), else StatusFailed with the
// underlying error.
func (d *Dispatcher) terminalOutcome(id string, err error) (status, message string) {
	d.runMu.Lock()
	canceled := d.userCancelled[id]
	d.runMu.Unlock()
	if canceled {
		return StatusCancelled, "canceled by user while running"
	}
	return StatusFailed, err.Error()
}

func (d *Dispatcher) execute(ctx context.Context, op *Operation) {
	// Per-op cancelable context so a user CancelOperation can abort this
	// specific in-flight op (see CancelRunning). Registered for the op's
	// lifetime; cleaned up on return.
	ctx, cancel := context.WithCancel(ctx)
	d.runMu.Lock()
	d.running[op.ID] = cancel
	d.runMu.Unlock()
	defer func() {
		cancel()
		d.runMu.Lock()
		delete(d.running, op.ID)
		delete(d.userCancelled, op.ID)
		d.runMu.Unlock()
	}()

	// completeCtx detaches the terminal bookkeeping write from cancellation:
	// when a user cancels a running op we cancel `ctx`, which makes the
	// provider return — but the Complete() that records the canceled status
	// must still run, so it can't share the canceled context.
	completeCtx := context.WithoutCancel(ctx)

	p, err := d.registry.For(op.APIVersion)
	if err != nil {
		_ = d.store.Complete(completeCtx, op.ID, StatusFailed, err.Error(), "")
		return
	}

	// Inject a recorder so composite-resource providers (Cluster) can record
	// per-VM and per-step child ops under this parent. Atomic providers
	// (VirtualMachine) never call it, so this is a no-op for them.
	ctx = WithRecorder(ctx, StoreRecorder{Store: d.store}, op.ID)

	switch op.Type {
	case TypeApply:
		var rawManifest protocol.Resource
		if err := json.Unmarshal([]byte(op.ManifestJSON), &rawManifest); err != nil {
			_ = d.store.Complete(completeCtx, op.ID, StatusFailed, fmt.Sprintf("decode manifest: %v", err), "")
			return
		}
		// Wire a ChildDispatcher and manifest-source label onto ctx so
		// composite providers (k3s Cluster) can invoke the same Apply
		// pipeline on Plan()-emitted children without re-implementing
		// resolve/cache/save. Atomic providers ignore both.
		applyCtx := WithChildDispatcher(ctx, d)
		if d.manifests != nil {
			applyCtx = manifests.WithSource(applyCtx, op.Source)
		}
		// Cache-hit path needs to touch op.Label before the shared
		// ApplyManifest helper serves the cached result.
		if d.manifests != nil {
			resolved := rawManifest
			if resolver := refs.New(d.registry); rawManifest.Spec != nil {
				if resolvedSpec, err := resolver.Resolve(applyCtx, rawManifest.Spec); err == nil {
					resolved.Spec = resolvedSpec
					if cached, hit := d.tryCacheHitInline(applyCtx, p, &rawManifest, &resolved); hit {
						_ = d.store.SetLabel(applyCtx, op.ID, "cached: input+refs hashes unchanged since last apply")
						cachedJSON, _ := json.Marshal(cached)
						_ = d.store.Complete(completeCtx, op.ID, StatusSucceeded, "", string(cachedJSON))
						return
					}
				}
			}
		}
		result, err := d.ApplyManifest(applyCtx, &rawManifest)
		if err != nil {
			st, msg := d.terminalOutcome(op.ID, err)
			_ = d.store.Complete(completeCtx, op.ID, st, msg, "")
			return
		}
		var resultJSON []byte
		if result != nil {
			resultJSON, _ = json.Marshal(result)
		}
		_ = d.store.Complete(completeCtx, op.ID, StatusSucceeded, "", string(resultJSON))

	case TypeDelete:
		if err := p.Delete(ctx, op.Kind, op.ResourceName); err != nil {
			st, msg := d.terminalOutcome(op.ID, err)
			_ = d.store.Complete(completeCtx, op.ID, st, msg, "")
			return
		}
		if d.manifests != nil {
			sinkCtx := manifests.WithSource(ctx, op.Source)
			if err := d.manifests.Delete(sinkCtx, op.APIVersion, op.Kind, op.ResourceName); err != nil {
				log.Printf("dispatcher: delete manifest for %s %q: %v", op.Kind, op.ResourceName, err)
			}
		}
		_ = d.store.Complete(completeCtx, op.ID, StatusSucceeded, "", "")

	default:
		_ = d.store.Complete(completeCtx, op.ID, StatusFailed, fmt.Sprintf("unknown op type %q", op.Type), "")
	}
}
