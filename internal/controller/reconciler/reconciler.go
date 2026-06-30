// Package reconciler periodically re-checks the observed state of every
// managed resource and logs drift transitions. It's a lightweight central
// alternative to the per-stream polling that ResourceService.Watch does:
//
//   - When no client is watching, this is the only thing that surfaces
//     out-of-band changes (someone clicked in the Proxmox UI) to the
//     controller's logs.
//   - When clients are watching, it doesn't conflict — the Watch poll
//     still detects drift on its own cadence; this just centralizes the
//     refresh and gives operational visibility today, and is the natural
//     hook point for arch Phase 10 continuous reconcile later.
//
// The reconciler does NOT auto-remediate: detected drift gets logged.
// Pushing desired state back over observed is the user's call via the
// manual reconcile path (UI button → re-apply stored manifest), so we
// don't surprise anyone with background mutation.
package reconciler

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/openctl/openctl/internal/controller/manifests"
	"github.com/openctl/openctl/internal/controller/providers"
)

// DefaultInterval is the standard reconcile cadence: slow enough not to
// hammer Proxmox or SSH into k3s nodes, fast enough that out-of-band
// changes surface within minutes.
const DefaultInterval = 5 * time.Minute

// StartupDelay is how long the loop waits after Start before its first
// tick. Gives the rest of the controller (dispatcher, gRPC server, HTTP
// gateway) time to finish wiring before we start firing provider calls.
const StartupDelay = 5 * time.Second

// Reconciler iterates applied_manifests on a ticker and calls provider.Get
// on each entry, logging drift transitions. Safe for concurrent calls to
// Start/Stop, but only one loop runs at a time.
type Reconciler struct {
	registry  *providers.Registry
	manifests *manifests.Store
	interval  time.Duration

	// driftState tracks whether each managed resource was drifted at the
	// last check, so we only log on transitions (clean→drifted or
	// drifted→clean) instead of one line per tick per resource.
	mu         sync.Mutex
	driftState map[string]bool

	stopMu  sync.Mutex
	stopped bool
	done    chan struct{}
}

// New constructs a Reconciler. interval==0 uses DefaultInterval. registry
// and manifests are required; passing nil panics so config errors surface
// at startup rather than on the first tick.
func New(reg *providers.Registry, m *manifests.Store, interval time.Duration) *Reconciler {
	if reg == nil || m == nil {
		panic("reconciler: registry and manifests are required")
	}
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Reconciler{
		registry:   reg,
		manifests:  m,
		interval:   interval,
		driftState: map[string]bool{},
		done:       make(chan struct{}),
	}
}

// Start launches the periodic loop in a goroutine and returns immediately.
func (r *Reconciler) Start(ctx context.Context) {
	go r.loop(ctx)
}

// Stop signals the loop to exit at the next tick boundary and waits for
// it to finish.
func (r *Reconciler) Stop() {
	r.stopMu.Lock()
	if r.stopped {
		r.stopMu.Unlock()
		<-r.done
		return
	}
	r.stopped = true
	r.stopMu.Unlock()
	<-r.done
}

// ReconcileOnce runs a single pass over every managed resource. Useful
// for tests and for callers that want an immediate check without
// restarting the loop.
func (r *Reconciler) ReconcileOnce(ctx context.Context) {
	r.tick(ctx)
}

func (r *Reconciler) loop(ctx context.Context) {
	defer close(r.done)

	select {
	case <-ctx.Done():
		return
	case <-time.After(StartupDelay):
	}
	r.tick(ctx)

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.stopMu.Lock()
			stopped := r.stopped
			r.stopMu.Unlock()
			if stopped {
				return
			}
			r.tick(ctx)
		}
	}
}

func (r *Reconciler) tick(ctx context.Context) {
	refs, err := r.manifests.ListAll(ctx)
	if err != nil {
		log.Printf("reconciler: list applied_manifests: %v", err)
		return
	}
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return
		}
		r.reconcileOne(ctx, ref)
	}
}

func (r *Reconciler) reconcileOne(ctx context.Context, ref manifests.Ref) {
	key := ref.APIVersion + "/" + ref.Kind + "/" + ref.Name

	p, err := r.registry.For(ref.APIVersion)
	if err != nil {
		// No provider for this apiVersion. Could happen mid-config-change;
		// skip rather than spamming logs.
		return
	}

	observed, err := p.Get(ctx, ref.Kind, ref.Name)
	if err != nil {
		var nf *providers.NotFoundError
		if errors.As(err, &nf) {
			// NotFound is itself drift — the resource we expected to manage
			// no longer exists. Log the transition once.
			r.markAndLogIfChanged(key, ref, true, "missing from provider")
			return
		}
		log.Printf("reconciler: %s: get: %v", key, err)
		return
	}

	desired, err := r.manifests.Load(ctx, ref.APIVersion, ref.Kind, ref.Name)
	if err != nil {
		log.Printf("reconciler: %s: load manifest: %v", key, err)
		return
	}
	if desired == nil {
		// Race: row vanished between ListAll and Load. Skip.
		return
	}

	drifted := !specsEqual(desired.Spec, observed.Spec)
	reason := ""
	if drifted {
		reason = "spec drift"
	}
	r.markAndLogIfChanged(key, ref, drifted, reason)
}

// markAndLogIfChanged updates driftState and logs only when the drift
// status flipped. Keeps the log readable instead of one line per tick
// per resource.
func (r *Reconciler) markAndLogIfChanged(key string, ref manifests.Ref, drifted bool, reason string) {
	r.mu.Lock()
	prev, hadPrev := r.driftState[key]
	r.driftState[key] = drifted
	r.mu.Unlock()

	if !hadPrev {
		// First observation: only log if currently drifted. Clean is the
		// normal startup state and would be noisy.
		if drifted {
			log.Printf("reconciler: drift detected on %s/%s/%s (%s)",
				ref.APIVersion, ref.Kind, ref.Name, reason)
		}
		return
	}
	if prev == drifted {
		return
	}
	if drifted {
		log.Printf("reconciler: drift detected on %s/%s/%s (%s)",
			ref.APIVersion, ref.Kind, ref.Name, reason)
	} else {
		log.Printf("reconciler: drift cleared on %s/%s/%s",
			ref.APIVersion, ref.Kind, ref.Name)
	}
}
