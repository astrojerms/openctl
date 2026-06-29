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
	Delete(ctx context.Context, apiVersion, kind, name string) error
	// LoadHash returns the input hash recorded by the previous successful
	// apply for this resource, or "" if no prior apply is on file.
	LoadHash(ctx context.Context, apiVersion, kind, name string) (string, error)
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
// Cache miss conditions (return false):
//   - manifests sink not configured (tests)
//   - no prior apply recorded for this resource
//   - prior hash differs from current input hash
//   - manifest carries the i-know-this-breaks-the-cluster annotation
//     (treated as "user is forcing a destructive operation" — never
//     short-circuit those)
//
// On hit, calls provider.Get to populate a useful result. If Get errors
// (e.g. the underlying resource was deleted out-of-band), the cache hit
// is abandoned and the caller falls through to normal Apply.
func (d *Dispatcher) tryCacheHit(ctx context.Context, p providers.Provider, op *Operation, manifest *protocol.Resource) bool {
	if d.manifests == nil {
		return false
	}
	if manifest.Metadata.Annotations["openctl.io/i-know-this-breaks-the-cluster"] == "true" {
		return false
	}
	storedHash, err := d.manifests.LoadHash(ctx, manifest.APIVersion, manifest.Kind, manifest.Metadata.Name)
	if err != nil || storedHash == "" {
		return false
	}
	if d.manifests.Hash(manifest) != storedHash {
		return false
	}
	// Hash match — confirm the resource still exists by fetching it.
	// If Get fails or returns nil, the cache is stale; fall through to
	// the normal Apply which will recreate from scratch.
	result, err := p.Get(ctx, manifest.Kind, manifest.Metadata.Name)
	if err != nil || result == nil {
		return false
	}
	_ = d.store.SetLabel(ctx, op.ID, "cached: input hash unchanged since last apply")
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
		var manifest protocol.Resource
		if err := json.Unmarshal([]byte(op.ManifestJSON), &manifest); err != nil {
			_ = d.store.Complete(ctx, op.ID, StatusFailed, fmt.Sprintf("decode manifest: %v", err), "")
			return
		}
		// Verifying-trace cache: if the manifest's input hash matches what
		// the previous successful apply stored, skip the provider call.
		// We still call provider.Get to populate a useful result for the
		// caller — Get is cheap (no mutation, no SSH, no Proxmox writes).
		if d.tryCacheHit(ctx, p, op, &manifest) {
			return
		}
		result, err := p.Apply(ctx, &manifest)
		if err != nil {
			_ = d.store.Complete(ctx, op.ID, StatusFailed, err.Error(), "")
			return
		}
		// Persist desired state for drift detection. Use the submitted
		// manifest, not the result — manifest is "intent", result is
		// "observed-after-apply" and may carry provider-set defaults that
		// would falsely match on the next Get.
		if d.manifests != nil {
			sinkCtx := manifests.WithSource(ctx, op.Source)
			if err := d.manifests.Save(sinkCtx, &manifest); err != nil {
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
