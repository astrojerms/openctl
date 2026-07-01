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
}

// NewDispatcher constructs a Dispatcher. pollInterval==0 uses
// DefaultPollInterval. manifests may be nil (tests).
func NewDispatcher(store *Store, registry *providers.Registry, manifests ManifestSink, pollInterval time.Duration) *Dispatcher {
	if pollInterval == 0 {
		pollInterval = DefaultPollInterval
	}
	return &Dispatcher{
		store:        store,
		registry:     registry,
		manifests:    manifests,
		pollInterval: pollInterval,
		notify:       make(chan struct{}, 1),
		done:         make(chan struct{}),
	}
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

// tryCacheHit checks the verifying-trace cache and, on a hit, fast-paths
// the op to succeeded without calling provider.Apply. Returns true if the
// op was completed via the cache; false if the caller should fall through
// to the normal Apply path.
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
//
// On hit, calls provider.Get to populate a useful result. If Get errors
// (e.g. the underlying resource was deleted out-of-band), the cache hit
// is abandoned and the caller falls through to normal Apply.
func (d *Dispatcher) tryCacheHit(ctx context.Context, p providers.Provider, op *Operation, raw, resolved *protocol.Resource) bool {
	if d.manifests == nil {
		return false
	}
	if raw.Metadata.Annotations["openctl.io/i-know-this-breaks-the-cluster"] == "true" {
		return false
	}
	storedInput, storedRefs, err := d.manifests.LoadHashes(ctx, raw.APIVersion, raw.Kind, raw.Metadata.Name)
	if err != nil || storedInput == "" {
		return false
	}
	if d.manifests.Hash(raw) != storedInput {
		return false
	}
	// refs_hash dimension: any manifest with resolvable $refs writes a
	// non-empty refs_hash on save. An empty stored refs_hash means "no
	// ref-hash information on file" (pre-Phase-8 row) and forces a miss
	// so we recompute it. A non-empty stored value must match.
	if storedRefs == "" || d.manifests.Hash(resolved) != storedRefs {
		return false
	}
	// Both hashes match — confirm the resource still exists by fetching
	// it. If Get fails or returns nil, the cache is stale; fall through
	// to the normal Apply which will recreate from scratch.
	result, err := p.Get(ctx, resolved.Kind, resolved.Metadata.Name)
	if err != nil || result == nil {
		return false
	}
	_ = d.store.SetLabel(ctx, op.ID, "cached: input+refs hashes unchanged since last apply")
	resultJSON, _ := json.Marshal(result)
	_ = d.store.Complete(ctx, op.ID, StatusSucceeded, "", string(resultJSON))
	return true
}

func (d *Dispatcher) execute(ctx context.Context, op *Operation) {
	p, err := d.registry.For(op.APIVersion)
	if err != nil {
		_ = d.store.Complete(ctx, op.ID, StatusFailed, err.Error(), "")
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
			_ = d.store.Complete(ctx, op.ID, StatusFailed, fmt.Sprintf("decode manifest: %v", err), "")
			return
		}
		// Phase 8 step 1: resolve any ResourceRefs in the spec before
		// handing to the provider. Step 5: keep the RAW manifest
		// intact (it's what we persist for intent + drift diffs), and
		// mutate a separate resolvedManifest that gets passed to
		// provider.Apply. Errors here (e.g. ref target missing) fail
		// the op — schedulers can retry after the referenced resource
		// is ready.
		resolvedManifest := rawManifest
		if resolver := refs.New(d.registry); rawManifest.Spec != nil {
			resolvedSpec, err := resolver.Resolve(ctx, rawManifest.Spec)
			if err != nil {
				_ = d.store.Complete(ctx, op.ID, StatusFailed,
					fmt.Sprintf("resolve refs: %v", err), "")
				return
			}
			resolvedManifest.Spec = resolvedSpec
		}
		// Two-dimensional verifying-trace cache: intent unchanged
		// (input_hash matches raw manifest) AND upstream refs
		// unchanged (refs_hash matches resolved spec). Provider.Get
		// still runs on the resolved manifest so the cached success
		// carries an accurate current view.
		if d.tryCacheHit(ctx, p, op, &rawManifest, &resolvedManifest) {
			return
		}
		result, err := p.Apply(ctx, &resolvedManifest)
		if err != nil {
			_ = d.store.Complete(ctx, op.ID, StatusFailed, err.Error(), "")
			return
		}
		// Persist desired state for drift detection. Save the RAW
		// manifest (preserving $ref markers) alongside a refs_hash
		// computed from the resolved spec — that way subsequent Gets
		// echo user intent, and the cache still invalidates when an
		// upstream ref target updates.
		if d.manifests != nil {
			sinkCtx := manifests.WithSource(ctx, op.Source)
			refsHash := d.manifests.Hash(&resolvedManifest)
			if err := d.manifests.SaveWithRefsHash(sinkCtx, &rawManifest, refsHash); err != nil {
				log.Printf("dispatcher: save manifest for %s %q: %v", op.Kind, op.ResourceName, err)
			}
		}
		var resultJSON []byte
		if result != nil {
			resultJSON, _ = json.Marshal(result)
		}
		_ = d.store.Complete(ctx, op.ID, StatusSucceeded, "", string(resultJSON))

	case TypeDelete:
		if err := p.Delete(ctx, op.Kind, op.ResourceName); err != nil {
			_ = d.store.Complete(ctx, op.ID, StatusFailed, err.Error(), "")
			return
		}
		if d.manifests != nil {
			sinkCtx := manifests.WithSource(ctx, op.Source)
			if err := d.manifests.Delete(sinkCtx, op.APIVersion, op.Kind, op.ResourceName); err != nil {
				log.Printf("dispatcher: delete manifest for %s %q: %v", op.Kind, op.ResourceName, err)
			}
		}
		_ = d.store.Complete(ctx, op.ID, StatusSucceeded, "", "")

	default:
		_ = d.store.Complete(ctx, op.ID, StatusFailed, fmt.Sprintf("unknown op type %q", op.Type), "")
	}
}
