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

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/pkg/protocol"
)

// DefaultPollInterval is the dispatcher's tick rate when no Notify wakes it.
// Keep small enough to feel responsive; large enough to avoid burning CPU.
const DefaultPollInterval = 500 * time.Millisecond

// ManifestSink lets the dispatcher persist desired-state on apply/delete
// success. Kept as a narrow interface so this package doesn't depend on
// internal/controller/manifests (the wiring lives in cmd/openctl-controller).
// nil is permitted — tests skip the sink.
type ManifestSink interface {
	Save(ctx context.Context, r *protocol.Resource) error
	Delete(ctx context.Context, apiVersion, kind, name string) error
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

func (d *Dispatcher) execute(ctx context.Context, op *Operation) {
	p, err := d.registry.For(op.APIVersion)
	if err != nil {
		_ = d.store.Complete(ctx, op.ID, StatusFailed, err.Error(), "")
		return
	}

	switch op.Type {
	case TypeApply:
		var manifest protocol.Resource
		if err := json.Unmarshal([]byte(op.ManifestJSON), &manifest); err != nil {
			_ = d.store.Complete(ctx, op.ID, StatusFailed, fmt.Sprintf("decode manifest: %v", err), "")
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
			if err := d.manifests.Save(ctx, &manifest); err != nil {
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
			if err := d.manifests.Delete(ctx, op.APIVersion, op.Kind, op.ResourceName); err != nil {
				log.Printf("dispatcher: delete manifest for %s %q: %v", op.Kind, op.ResourceName, err)
			}
		}
		_ = d.store.Complete(ctx, op.ID, StatusSucceeded, "", "")

	default:
		_ = d.store.Complete(ctx, op.ID, StatusFailed, fmt.Sprintf("unknown op type %q", op.Type), "")
	}
}
