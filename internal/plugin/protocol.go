package plugin

import (
	"github.com/openctl/openctl/pkg/protocol"
)

// Re-export protocol types for convenience
type (
	Request        = protocol.Request
	Response       = protocol.Response
	Resource       = protocol.Resource
	Capabilities   = protocol.Capabilities
	ProviderConfig = protocol.ProviderConfig
)

// Constants
const (
	ActionGet    = protocol.ActionGet
	ActionList   = protocol.ActionList
	ActionCreate = protocol.ActionCreate
	ActionDelete = protocol.ActionDelete
	ActionApply  = protocol.ActionApply

	StatusSuccess = protocol.StatusSuccess
	StatusError   = protocol.StatusError

	ProtocolVersion = protocol.ProtocolVersion
)
