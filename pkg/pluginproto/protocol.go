// Package pluginproto defines openctl's v2 external-plugin protocol: a
// persistent-process, id-correlated JSON message stream over stdio that
// serializes the in-process providers.Provider contract so third-party
// provider binaries can satisfy it out-of-process.
//
// This is deliberately distinct from the legacy one-shot exec protocol in
// pkg/protocol (a fresh process per request, no long-lived configure step).
// The v2 protocol keeps one process alive across many calls, configures it
// once, and threads opaque state/private blobs through Apply/Get/Delete so a
// stateful implementer (the forthcoming Terraform/OpenTofu provider host —
// see docs/plugin-architecture.md) fits the same wire format without a
// protocol revision.
//
// Roles are fixed: openctl is always the client (sends requests), the plugin
// is always the server (sends responses). Messages are newline-independent —
// json.Encoder/Decoder frame successive JSON values on the pipe.
package pluginproto

import (
	"encoding/json"

	"github.com/openctl/openctl/pkg/protocol"
)

// ProtocolVersion is the wire version a plugin must echo in its handshake.
// Bumped independently of the legacy pkg/protocol ProtocolVersion ("1.0").
const ProtocolVersion = "2.0"

// Method names for request Messages. openctl sends these; the plugin
// answers each with a Message carrying the same ID.
const (
	MethodHandshake  = "handshake"  // negotiate name/version/kinds/capabilities (no params)
	MethodConfigure  = "configure"  // one-time provider config injection
	MethodApply      = "apply"      // create/converge a resource
	MethodGet        = "get"        // observed state of one resource
	MethodList       = "list"       // observed state of all resources of a kind
	MethodDelete     = "delete"     // idempotent removal
	MethodPlan       = "plan"       // composite expansion into child manifests
	MethodDryRun     = "dryRun"     // preview an Apply without performing it
	MethodDoAction   = "doAction"   // runtime action on a live resource
	MethodOwnerOf    = "ownerOf"    // ownership query for delete-blocking
	MethodChildrenOf = "childrenOf" // composition query
	MethodShutdown   = "shutdown"   // graceful stop; plugin exits after replying
)

// Capability strings a plugin advertises in its handshake. openctl uses
// these to decide which optional providers.* interfaces the adapter exposes
// and whether a given call is worth a round-trip.
//
// Only CapabilityPlan is load-bearing for interface exposure: a Planner's
// mere presence can route dispatch through composite expansion, so the
// adapter must NOT claim Planner unless the plugin advertises it. The other
// capabilities gate round-trips only — the adapter always implements those
// interfaces but short-circuits to a safe empty/nil result when the
// capability is absent.
const (
	CapabilityPlan      = "plan"      // implements Plan (composite expansion)
	CapabilityDryRun    = "dryRun"    // implements DryRun (else spec-level diff is used)
	CapabilityActions   = "actions"   // implements DoAction for some kinds
	CapabilityChildren  = "children"  // implements ChildrenOf
	CapabilityOwnership = "ownership" // implements OwnerOf
	CapabilitySchema    = "schema"    // supplies CUE schema in KindInfo.Schema
	CapabilityState     = "state"     // round-trips opaque state/private blobs
)

// Message is the envelope for both directions. A request carries Method +
// Params; a response carries Result or Error. ID correlates a response to
// its request (handshake is ID 1; the client increments from there).
type Message struct {
	ID     uint64          `json:"id"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error codes. CodeNotFound is significant: the adapter maps it to
// providers.NotFoundError so Get misses become gRPC NotFound, matching the
// in-process providers' contract.
const (
	CodeNotFound    = "NOT_FOUND"
	CodeInvalid     = "INVALID"
	CodeUnsupported = "UNSUPPORTED"
	CodeInternal    = "INTERNAL"
)

// Error is a structured plugin error carried on a response Message. It
// implements the error interface so the client can surface it directly.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return "[" + e.Code + "] " + e.Message
}

// --- handshake ---

// HandshakeResult is the plugin's self-description. Kinds enumerates every
// resource kind the plugin handles (with optional per-kind schema, actions,
// and observed-only marking); Capabilities lists provider-wide behaviors.
type HandshakeResult struct {
	ProviderName    string     `json:"providerName"`
	ProtocolVersion string     `json:"protocolVersion"`
	Kinds           []KindInfo `json:"kinds"`
	Capabilities    []string   `json:"capabilities,omitempty"`
}

// KindInfo describes one resource kind. Schema is optional CUE source (only
// meaningful when the plugin advertises CapabilitySchema). Actions is the
// per-kind runtime action list; Observed marks platform-discovered kinds
// that bypass the managed-only filter.
type KindInfo struct {
	Kind     string   `json:"kind"`
	Schema   string   `json:"schema,omitempty"`
	Actions  []string `json:"actions,omitempty"`
	Observed bool     `json:"observed,omitempty"`

	// OwnerKind, when set, marks this kind as a composite-child: normally
	// produced by that parent kind (a Planner) rather than authored directly —
	// e.g. a Cluster that fans out into node kinds. AdvancedNote is the human
	// explanation openctl's create form shows. openctl re-exposes both via
	// SchemaService.ListSchemas so any client can flag the kind as "advanced"
	// and nudge toward the owning composite. Empty for ordinary top-level kinds.
	OwnerKind    string `json:"ownerKind,omitempty"`
	AdvancedNote string `json:"advancedNote,omitempty"`
}

// --- configure ---

// ConfigureParams injects provider configuration once, after the handshake.
// Config is an opaque, provider-defined JSON bag: openctl marshals whatever
// it has (endpoint/token/defaults today; a Terraform provider block later)
// and the plugin unmarshals into its own type. Keeping it opaque is what
// lets one wire format serve both a native provider and the TF host.
type ConfigureParams struct {
	Config json.RawMessage `json:"config,omitempty"`
}

// --- apply / get / list / delete ---

// ApplyParams carries the desired manifest plus the prior opaque state and
// private blobs. State/Private are nil for stateless providers; a stateful
// implementer reads them, mutates, and returns updated blobs in ApplyResult.
type ApplyParams struct {
	Manifest *protocol.Resource `json:"manifest"`
	State    json.RawMessage    `json:"state,omitempty"`
	Private  json.RawMessage    `json:"private,omitempty"`
}

// ApplyResult returns the observed resource and any updated state/private
// blobs to persist. Stateless providers leave State/Private nil.
type ApplyResult struct {
	Resource *protocol.Resource `json:"resource"`
	State    json.RawMessage    `json:"state,omitempty"`
	Private  json.RawMessage    `json:"private,omitempty"`
}

// GetParams identifies a resource and carries its prior state so a stateful
// provider can refresh against it (Terraform ReadResource semantics).
type GetParams struct {
	Kind  string          `json:"kind"`
	Name  string          `json:"name"`
	State json.RawMessage `json:"state,omitempty"`
}

// GetResult mirrors ApplyResult minus the private blob (Read does not
// produce one).
type GetResult struct {
	Resource *protocol.Resource `json:"resource"`
	State    json.RawMessage    `json:"state,omitempty"`
}

// ListParams identifies the kind to enumerate.
type ListParams struct {
	Kind string `json:"kind"`
}

// ListResult carries the observed resources of a kind.
type ListResult struct {
	Resources []*protocol.Resource `json:"resources"`
}

// DeleteParams identifies a resource and carries its state/private so a
// stateful provider can destroy with full context.
type DeleteParams struct {
	Kind    string          `json:"kind"`
	Name    string          `json:"name"`
	State   json.RawMessage `json:"state,omitempty"`
	Private json.RawMessage `json:"private,omitempty"`
}

// --- plan / dryRun ---

// PlanParams carries the composite manifest to expand.
type PlanParams struct {
	Manifest *protocol.Resource `json:"manifest"`
}

// PlanResult is the set of child manifests the composite expands into,
// carrying $ref pointers per the Planner contract.
type PlanResult struct {
	Children []*protocol.Resource `json:"children"`
}

// DryRunParams carries the manifest to preview.
type DryRunParams struct {
	Manifest *protocol.Resource `json:"manifest"`
}

// DryRunResult mirrors providers.DryRunResult on the wire.
type DryRunResult struct {
	Children      []ChildAction `json:"children,omitempty"`
	RequiredGates []string      `json:"requiredGates,omitempty"`
	Summary       string        `json:"summary,omitempty"`
}

// ChildAction mirrors providers.ChildAction.
type ChildAction struct {
	Verb   string `json:"verb"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
}

// --- actions ---

// DoActionParams names the action and its target.
type DoActionParams struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Action string `json:"action"`
}

// DoActionResult mirrors providers.ActionResult.
type DoActionResult struct {
	Message          string `json:"message,omitempty"`
	URL              string `json:"url,omitempty"`
	DownloadContent  string `json:"downloadContent,omitempty"`
	DownloadFilename string `json:"downloadFilename,omitempty"`
}

// --- ownership / children ---

// RefParams identifies a resource for an ownership or composition query.
type RefParams struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// OwnerOfResult reports ownership. Owned=false means unclaimed.
type OwnerOfResult struct {
	OwnerKind string `json:"ownerKind,omitempty"`
	OwnerName string `json:"ownerName,omitempty"`
	Owned     bool   `json:"owned"`
}

// ResourceRef mirrors providers.ResourceRef on the wire.
type ResourceRef struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
}

// ChildrenOfResult lists composed children.
type ChildrenOfResult struct {
	Children []ResourceRef `json:"children,omitempty"`
}
