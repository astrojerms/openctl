package providers

import (
	"context"

	"github.com/openctl/openctl/pkg/protocol"
)

// MigrationIdentityReader verifies an existing resource through read-only
// provider calls. Identity must bind the actual instance, not only its name.
// Providers without this capability cannot export or adopt resources.
type MigrationIdentityReader interface {
	MigrationIdentity(ctx context.Context, manifest *protocol.Resource) (string, error)
}
