package external

import (
	"context"
	"testing"

	"github.com/openctl/openctl/internal/controller/providers"
	"github.com/openctl/openctl/pkg/pluginproto"
)

func TestCheckedOwnershipDoesNotAuthorizeAfterCancellation(t *testing.T) {
	p, closeAdapter := newAdapter(t, newTestHandler("ownership", pluginproto.CapabilityOwnership))
	defer closeAdapter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	checker := p.(providers.CheckedOwnershipChecker)
	if _, _, _, err := checker.CheckedOwnerOf(ctx, "Thing", "owned"); err == nil {
		t.Fatal("canceled ownership lookup must not authorize deletion")
	}
}
