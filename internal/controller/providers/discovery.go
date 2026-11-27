package providers

import (
	"context"
	"github.com/openctl/openctl/pkg/protocol"
)

// Discoverer exposes read-only inventory without admitting resources to management.
type Discoverer interface {
	DiscoveryContexts() []DiscoveryContext
	Discover(ctx context.Context, contextName string) ([]DiscoveredResource, error)
	TestConnection(ctx context.Context, contextName string) error
}

type DiscoveryContext struct {
	Name     string
	Endpoint string
	Default  bool
}

type DiscoveredResource struct {
	Context          string
	Node             string
	VMID             int
	Identity         string
	Resource         *protocol.Resource
	Managed          bool
	Adoptable        bool
	Reason           string
	IdentityRecorded bool
}

// IdentityManager records or releases management bindings without provider writes.
// Adoption verifies identity before calling this; forgetting releases only tracking.
type IdentityManager interface {
	ObserveIdentity(ctx context.Context, manifest *protocol.Resource, identity string) (*protocol.Resource, error)
	BindIdentity(ctx context.Context, manifest *protocol.Resource, identity string) error
	ReleaseIdentity(ctx context.Context, name string) error
	ConfirmIdentity(ctx context.Context, manifest *protocol.Resource, identity string) error
}
