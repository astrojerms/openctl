package operations

import (
	"context"

	"github.com/openctl/openctl/pkg/protocol"
)

// ChildDispatcher lets a composite provider (k3s Cluster) fan its Apply
// out into a Plan()-emitted set of child manifests without spawning
// separate operation rows or re-implementing the resolve/cache/save
// pipeline.
//
// Wired onto ctx by dispatcher.execute() for the duration of a top-level
// Apply. Providers that don't compose ignore it; composite providers
// pull it out via ChildDispatcherFrom(ctx) and call ApplyChild on each
// planned manifest. The call runs synchronously in the caller's
// goroutine — no dispatcher queue involvement — so ordering is whatever
// the caller loops in.
//
// Split as an interface rather than *Dispatcher so tests can stub it
// without spinning up a full dispatcher + storage.
type ChildDispatcher interface {
	ApplyChild(ctx context.Context, manifest *protocol.Resource) (*protocol.Resource, error)
}

type childDispatchKey struct{}

// WithChildDispatcher attaches d to ctx so downstream provider.Apply
// calls can pull it out via ChildDispatcherFrom.
func WithChildDispatcher(ctx context.Context, d ChildDispatcher) context.Context {
	return context.WithValue(ctx, childDispatchKey{}, d)
}

// ChildDispatcherFrom returns the ChildDispatcher attached to ctx, or
// (nil, false) if none is set. Composite providers use this to detect
// whether they're running inside a dispatched op (fan out) vs a direct
// invocation (fall back to the imperative path).
func ChildDispatcherFrom(ctx context.Context) (ChildDispatcher, bool) {
	d, ok := ctx.Value(childDispatchKey{}).(ChildDispatcher)
	return d, ok && d != nil
}

// ApplyChild implements ChildDispatcher on *Dispatcher. Just delegates
// to ApplyManifest — kept as its own method so the interface stays
// narrow and doesn't expose the entire dispatcher surface to providers.
func (d *Dispatcher) ApplyChild(ctx context.Context, manifest *protocol.Resource) (*protocol.Resource, error) {
	return d.ApplyManifest(ctx, manifest)
}
