package operations

import "context"

// ChildRecorder is the contract providers use to surface sub-step progress
// under a parent operation. The dispatcher injects a real recorder into the
// context before calling Provider.Apply / Delete; provider code (typically
// for composite resources like Cluster) calls Begin before each sub-step
// and End when it finishes. Each Begin/End pair writes one child row to
// the operations table with parent_id set to the parent op.
//
// Providers that don't know about child ops simply never call the recorder
// — there's no behavior change for atomic resources like VirtualMachine.
//
// Tests that don't care about child rows pass no context value and get the
// no-op recorder via RecorderFrom, so providers can call recorder.Begin
// unconditionally without checking for nil.
type ChildRecorder interface {
	// Begin records the start of a child operation and returns its ID.
	// op.ParentID and op.Status are set by the recorder; the caller fills
	// Type, APIVersion, Kind, ResourceName, Label, ManifestJSON.
	Begin(ctx context.Context, op *Operation) (childID string, err error)
	// End writes the terminal status for a child. ok=true → succeeded;
	// false → failed (errMsg should carry the error). resultJSON is
	// optional and stored verbatim on succeeded.
	End(ctx context.Context, childID string, ok bool, errMsg, resultJSON string) error
}

type recorderKey struct{}

// WithRecorder returns ctx with the given recorder + parentID attached.
// Called by the dispatcher before invoking a provider.
func WithRecorder(ctx context.Context, r ChildRecorder, parentID string) context.Context {
	return context.WithValue(ctx, recorderKey{}, &boundRecorder{r: r, parentID: parentID})
}

// RecorderFrom returns the recorder attached to ctx, or a no-op recorder if
// none. Always returns a non-nil ChildRecorder so callers can use it
// directly without nil-checks.
func RecorderFrom(ctx context.Context) ChildRecorder {
	if v, ok := ctx.Value(recorderKey{}).(*boundRecorder); ok && v != nil {
		return v
	}
	return noopRecorder{}
}

// boundRecorder pairs a ChildRecorder with the parent op ID so callers
// don't have to thread parentID through every Begin call.
type boundRecorder struct {
	r        ChildRecorder
	parentID string
}

func (b *boundRecorder) Begin(ctx context.Context, op *Operation) (string, error) {
	op.ParentID = b.parentID
	return b.r.Begin(ctx, op)
}

func (b *boundRecorder) End(ctx context.Context, childID string, ok bool, errMsg, resultJSON string) error {
	return b.r.End(ctx, childID, ok, errMsg, resultJSON)
}

// StoreRecorder is the production ChildRecorder backed by the operations
// Store. The dispatcher constructs one per parent and injects it via
// WithRecorder.
type StoreRecorder struct {
	Store *Store
}

func (s StoreRecorder) Begin(ctx context.Context, op *Operation) (string, error) {
	out, err := s.Store.BeginChild(ctx, op.ParentID, op)
	if err != nil {
		return "", err
	}
	return out.ID, nil
}

func (s StoreRecorder) End(ctx context.Context, childID string, ok bool, errMsg, resultJSON string) error {
	st := StatusSucceeded
	if !ok {
		st = StatusFailed
	}
	return s.Store.EndChild(ctx, childID, st, errMsg, resultJSON)
}

// noopRecorder swallows Begin/End calls. Used outside the dispatcher (CLI
// direct invocation, tests) so providers can call the recorder
// unconditionally.
type noopRecorder struct{}

func (noopRecorder) Begin(_ context.Context, _ *Operation) (string, error) { return "", nil }
func (noopRecorder) End(_ context.Context, _ string, _ bool, _, _ string) error {
	return nil
}
