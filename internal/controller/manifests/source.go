package manifests

import "context"

// Source values for the controller's op originator tracking. Free-form
// strings on the wire, but these constants name the two we ship: requests
// from the CLI vs. from the browser UI. Plumbed through Operation.Source
// at submit time and read back by the git hook to format commit messages.
const (
	SourceCLI            = "cli"
	SourceUI             = "ui"
	SourceAutoReconcile  = "auto-reconcile"
)

// sourceKey is the context key for the originator of a dispatcher
// operation. The dispatcher attaches op.Source to ctx before calling
// the ManifestSink, so sinks that care (DiskMirror's git hook) can read
// it back via SourceFromContext.
type sourceKey struct{}

// WithSource attaches the originator string to ctx. Empty source clears
// the attached value — readers fall back to SourceCLI in that case.
func WithSource(ctx context.Context, source string) context.Context {
	if source == "" {
		return ctx
	}
	return context.WithValue(ctx, sourceKey{}, source)
}

// SourceFromContext returns the originator attached to ctx, or "" if no
// source was attached. Callers that want a sensible default should fall
// back to SourceCLI themselves.
func SourceFromContext(ctx context.Context) string {
	v, _ := ctx.Value(sourceKey{}).(string)
	return v
}
