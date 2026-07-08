# Writing an external provider (v2 plugin protocol)

openctl can drive **out-of-process provider plugins** — separate binaries that
satisfy the same provider contract the built-in Proxmox and k3s providers do,
without being compiled into the controller. This is the "wide, any-provider
ecosystem" foundation from [direction.md](direction.md); the
[plugin-architecture.md](plugin-architecture.md) doc explains where it sits in
the design. This page is the practical reference for **authoring** one.

A complete, runnable reference plugin lives in
[`plugins/example`](../plugins/example) (a file-backed `Note` provider, plus a
`Notebook` composite that expands into `Note` children). Read it alongside this
page.

---

## How it works

The controller spawns your binary once and keeps it alive, exchanging
**id-correlated JSON messages over stdio** (`pkg/pluginproto`):

- **stdin** — requests from the controller (`handshake`, `configure`, `apply`, …)
- **stdout** — your responses (the protocol channel — never print to it directly)
- **stderr** — free-form diagnostics; forwarded into the controller log

One process serves many calls. It is configured once (after the handshake) and
then handles resource operations until the controller shuts it down.

> This is a different protocol from the legacy one-shot exec plugins in
> `pkg/protocol` (a fresh process per request). New providers should use the v2
> protocol described here.

## The fastest path: implement `pluginproto.Handler`

You do **not** parse the wire format yourself. Implement the `Handler`
interface and call `pluginproto.Serve`:

```go
package main

import (
	"os"

	"github.com/openctl/openctl/pkg/pluginproto"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "plugin-serve" {
		os.Exit(2)
	}
	_ = pluginproto.Serve(myProvider{})
}
```

`Handler` (see `pkg/pluginproto/server.go`) has five **required** methods and
several optional ones. Embed `pluginproto.UnimplementedHandler` to inherit safe
defaults for everything optional, then override what you support:

| Method | Required | Purpose |
|---|---|---|
| `Handshake` | ✅ | Announce provider name, kinds, capabilities, and (optionally) per-kind CUE schema |
| `Apply` | ✅ | Create/converge a resource |
| `Get` | ✅ | Observed state of one resource (`pluginproto.NotFound(...)` for a genuine miss) |
| `List` | ✅ | Observed state of all resources of a kind |
| `Delete` | ✅ | Idempotent removal (missing == deleted, return nil) |
| `Configure` | optional | One-time provider config injection |
| `Plan` | optional | Composite expansion into child manifests |
| `DryRun` | optional | Preview an Apply |
| `DoAction` | optional | Runtime actions on a live resource |
| `OwnerOf` / `ChildrenOf` | optional | Ownership / composition relationships |

## The handshake — declare who you are

`Handshake` returns a `HandshakeResult`:

```go
func (myProvider) Handshake(context.Context) (*pluginproto.HandshakeResult, error) {
	return &pluginproto.HandshakeResult{
		ProviderName:    "example",                 // → apiVersion "example.openctl.io/v1"
		ProtocolVersion: pluginproto.ProtocolVersion,
		Capabilities:    []string{pluginproto.CapabilitySchema, pluginproto.CapabilityActions, pluginproto.CapabilityPlan},
		Kinds: []pluginproto.KindInfo{
			{Kind: "Notebook", Schema: notebookSchema},                 // composite parent
			{Kind: "Note", Schema: noteSchema, Actions: []string{"touch"},
				OwnerKind:    "Notebook",                                // composite-child of Notebook
				AdvancedNote: "Usually one page of a Notebook, which creates its Notes for you."},
		},
	}, nil
}
```

- **`ProviderName`** must match the apiVersion prefix the controller routes on:
  name `example` ⇒ resources are `example.openctl.io/v1`.
- **`ProtocolVersion`** must equal `pluginproto.ProtocolVersion` or the
  controller rejects the plugin at load.
- **`Kinds`** lists every resource kind you handle. `Observed: true` marks a
  platform-discovered kind (bypasses the managed-only filter). `Actions`
  lists per-kind runtime actions.
- **`OwnerKind` + `AdvancedNote`** mark a kind as a *composite-child*: normally
  produced by that parent kind (a Planner) rather than authored directly. The
  controller re-exposes both through `SchemaService.ListSchemas`, so the UI
  flags the kind "advanced" and nudges toward creating the parent — no
  client-side list. Set these on the child (`Note`), not the parent
  (`Notebook`). See `plugins/example` for the full Notebook→Note composite.

### Capabilities

Advertise a capability only if you implement it. Their effect:

| Capability | Effect |
|---|---|
| `CapabilitySchema` | Your `KindInfo.Schema` CUE is registered for validation + UI listing |
| `CapabilityActions` | `DoAction` is reachable for kinds that list actions |
| `CapabilityDryRun` | `DryRun` is used; otherwise the controller does a spec-level diff |
| `CapabilityPlan` | Your provider is treated as a composite **Planner** |
| `CapabilityOwnership` | `OwnerOf` is consulted before deletes |
| `CapabilityChildren` | `ChildrenOf` is used to build the composition/DAG view |
| `CapabilityState` | You round-trip opaque state/private blobs (see below) |

The controller only round-trips to your plugin for a capability you actually
advertised — an un-advertised `OwnerOf`, for instance, is answered locally as
"unowned" without a call.

## Supplying a schema

If you advertise `CapabilitySchema`, put a **standalone** CUE document in each
`KindInfo.Schema`. It is compiled on its own (no openctl module imports), so it
must define a top-level definition named `#<Kind>` describing the whole
resource. Keep it open (`...`) so controller-managed fields aren't rejected:

```cue
#Note: {
	apiVersion: "example.openctl.io/v1"
	kind:       "Note"
	metadata: { name: string, ... }
	spec: { content: string }
	...
}
```

Resources are then validated against this at apply time, and the schema shows
up in the UI's schema list and editor. (Typed *form* generation for external
kinds isn't wired yet — the UI falls back to the YAML editor for them.)

## Configuration

If you implement `Configure`, the controller calls it once after the handshake
with an **opaque JSON bag** it built from your `providers:` entry in
`config.yaml`. Unmarshal it into whatever shape you expect. The built-in
config maps `defaults:` into `protocol.ProviderConfig.Defaults`, so:

```yaml
providers:
  example:
    command: openctl-example
    args: [plugin-serve]
    defaults:
      dir: /var/lib/openctl-example
```

reaches your plugin as `{"defaults":{"dir":"/var/lib/openctl-example"}}`.

## State (stateful providers)

`Apply`/`Get`/`Delete` carry optional opaque **state** and **private** blobs
(`ApplyParams.State`, `ApplyResult.State`, …). A stateless provider (like the
example, which reads files back off disk) ignores them. Providers that need to
persist opaque per-resource state across calls return updated blobs and read
them back next call.

To opt in, advertise **`CapabilityState`** in the handshake. The controller
then persists whatever your plugin returns in its `provider_state` store (one
opaque row per resource) and replays it verbatim on the next call:

- **Apply** — the controller loads the prior `state`/`private` and passes them
  in `ApplyParams`; it saves the `state`/`private` you return.
- **Get** — the prior `state` is passed in; if you return refreshed `state`
  (Terraform `ReadResource` semantics), the controller persists it so drift
  checks compare against your latest view.
- **Delete** — the current `state`/`private` are passed in, then the row is
  removed.

openctl never parses these blobs. Because the store is per-resource and opaque,
none of the monolithic-tfstate pain (global lock, whole-world file, state
surgery) applies. `schema_version` is stored alongside for a future
provider-driven state-upgrade path; simple plugins can ignore it.

## Installing your plugin

1. Build your binary (any name; the example uses `openctl-example`).
2. Put it on `PATH` or reference it by absolute path.
3. Add a `providers:` entry with `command:` (and optional `args:`/`defaults:`):

   ```yaml
   providers:
     example:
       command: openctl-example
       args: [plugin-serve]
   ```

4. Restart the controller. It spawns the plugin, handshakes, and registers your
   kinds — they now flow through apply/get/list/delete, drift reconciliation,
   the git mirror, and the UI exactly like built-in kinds. A plugin that fails
   to load is logged and skipped (it can't stop the controller), and built-in
   provider names (`proxmox`, `k3s`) always win a name collision.

## The provider contract

The controller reaches your plugin through an in-process adapter that turns
your `Handler` into the same `providers.Provider` interface the compiled-in
providers implement. The controller — dispatcher, reconciler, gRPC layer —
relies on a small set of invariants holding for *every* provider. Get these
right and your plugin behaves like a first-party one; get them wrong and the
failure is subtle (a duplicate resource, a delete that reports success but
leaves state, a not-found that surfaces as an internal error). The invariants:

- **Apply returns the observed resource with its identity intact.** The result
  carries the same `kind` and `metadata.name` you were handed, and a non-empty
  `apiVersion`. Put observed/computed outputs (an assigned IP, an ID) in
  `status` so other resources can `$ref` them.
- **Get on a missing resource returns `pluginproto.NotFound(...)`.** The adapter
  maps that sentinel to `*providers.NotFoundError`, which the server maps to
  gRPC `codes.NotFound`. Returning a generic error instead turns "doesn't
  exist" into "internal error" and breaks Get-after-Delete and reconcile.
- **Get after a successful Apply returns the resource** — the round-trip must
  hold, so a resource you just created is immediately observable by name.
- **Delete is idempotent.** Delete of a name that doesn't exist returns success
  (nil), not an error — reconcile and retries depend on it.
- **Delete removes.** After Delete succeeds, a Get for that name returns
  NotFound.
- **Re-Apply of an unchanged manifest is never destructive.** Whether you no-op
  (return the observed state unchanged, like the Proxmox VM provider) or update
  in place, the resource still round-trips afterward. Do not recreate it.
- **List (if you support it) enumerates by kind** and returns an empty slice —
  not an error — when there are none. If your backend has no enumeration API,
  it's fine to return an error from List; declare that so callers don't rely on
  it.

These are exactly the invariants the shared conformance battery in
`internal/controller/providers/providertest` enforces. The external adapter is
bound to it (`internal/controller/providers/external/conformance_test.go`), so
a `Handler` that upholds the obligations above passes the battery through the
adapter automatically.

### Composite providers (`Plan`)

If your provider advertises `CapabilityPlan` — expanding one resource into
child manifests instead of applying it directly — its contract is its `Plan`,
not the atomic CRUD obligations above (which is why composite providers are out
of the conformance battery's scope). A composite `Plan` must:

- **Stamp owner references** on every child (`openctl.io/owner-kind` /
  `owner-name`), so the controller can attribute children to their parent and
  block orphaning them.
- **Emit an acyclic, self-contained child `$ref` graph.** Any `$ref` between
  children must point at another child in the same plan, and the graph must be
  acyclic — the controller schedules the children with a real dependency DAG
  (`operations.RunGraph`), which errors on a cycle or a dangling reference.
- **Be deterministic.** The same input manifest must produce the same children
  (same set, same specs). Reconciliation, drift surfacing, and the
  verifying-trace cache all assume a stable plan; don't derive child names or
  values from wall-clock time or map-iteration order.

The k3s `Cluster` provider is the reference composite; its `Plan` contract is
pinned by `internal/controller/providers/k3s/cluster_plan_contract_test.go`.
For a minimal, file-backed composite in plugin form, see the `Notebook` kind in
[`plugins/example`](../plugins/example): it `Plan`s into owner-labeled `Note`
children and declares `Note` as an advanced composite-child of `Notebook`.

## Testing tips

- Unit-test your `Handler` directly (no process needed) — call its methods, or
  wire it to a `pluginproto.Client` over an `io.Pipe` with
  `pluginproto.ServeConn`. See `pkg/pluginproto/pluginproto_test.go`.
- For an end-to-end check, build your binary and load it through
  `internal/controller/providers/external.Load`. See
  `internal/controller/providers/external/e2e_test.go`.
- To check contract conformance, run the shared `providertest.Suite` against
  your adapter-wrapped provider (as the external adapter does in
  `conformance_test.go`) — it exercises the invariants above and reports the
  specific one you miss.
