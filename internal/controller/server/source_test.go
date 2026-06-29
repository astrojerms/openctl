package server

import (
	"context"
	"testing"

	"google.golang.org/grpc/metadata"

	"github.com/openctl/openctl/internal/controller/manifests"
)

func TestSourceFromContextDefaultsToCLI(t *testing.T) {
	if got := sourceFromContext(context.Background()); got != manifests.SourceCLI {
		t.Errorf("no metadata: got %q, want %q", got, manifests.SourceCLI)
	}
}

func TestSourceFromContextReadsUIHeader(t *testing.T) {
	md := metadata.Pairs(sourceMetadataKey, manifests.SourceUI)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	if got := sourceFromContext(ctx); got != manifests.SourceUI {
		t.Errorf("ui metadata: got %q, want %q", got, manifests.SourceUI)
	}
}

func TestSourceFromContextIgnoresUnknownValues(t *testing.T) {
	md := metadata.Pairs(sourceMetadataKey, "bogus")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	// Anything that isn't exactly "ui" falls back to CLI — no surprises.
	if got := sourceFromContext(ctx); got != manifests.SourceCLI {
		t.Errorf("bogus metadata: got %q, want %q", got, manifests.SourceCLI)
	}
}
