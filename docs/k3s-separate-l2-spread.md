# k3s cluster spread across separate L2 networks — design proposal

**Status:** proposal, awaiting sign-off. Not implemented.
**Author:** autonomous session, 2026-07-05.
**Follows:** the shipped multi-endpoint placement (#64 across hosts, #65 across
Proxmox *endpoints*), which is explicitly **scoped to endpoints sharing one L2
network**. This designs the follow-on epic: spreading one cluster across
endpoints on **different subnets** (different bridges / IP ranges / broadcast
domains), where nodes cannot reach each other by bare LAN IP.

Design is autonomous; the implementation needs a genuine two-site setup to
validate (that's the one gate that requires hardware).

## What forces this to be its own epic

The current cluster hardcodes three single-L2 assumptions
(`pkg/k3s/resources/cluster.go`, `internal/controller/providers/k3s/cluster_plan.go`):

1. **One `network.bridge`** stamped on every VM (`cluster_plan.go:203`,
   `create.go:126`). Nodes on a second Proxmox site sit on a *different* bridge
   and subnet.
2. **One contiguous IP range.** `AllocateIPs` takes a single `startIP` and
   increments the last octet (`cluster.go:501-522`) — it can't straddle two
   subnets.
3. **Join by bare `status.vmIP`.** Every joiner's `joinURLFrom` resolves to the
   first control plane's `status.vmIP` (`cluster_plan.go:276-281`) — a LAN IP
   that a node on another subnet may not route to. Likewise the agent dials
   nodes directly by `vmIP`.

Plus one runtime assumption **outside** openctl: k3s's default flannel VXLAN
backend assumes all nodes share an L2 segment for the pod overlay. Across
subnets that either needs routed multicast (usually unavailable) or a different
backend.

What already survives the jump: the **mTLS CA bundle is topology-independent**
(certs bind to identities, not IPs), and the #65 context-routing spine
(`pmprovider.NewMulti`, `spec.context` per VM) is exactly the placement
mechanism this builds on — it already lands VMs on the right endpoint. The gap
is purely *networking*, not orchestration.

## Proposed model

Make the three assumptions **per-endpoint** instead of cluster-wide, and give
k3s a cross-subnet-capable overlay.

### 1. Per-endpoint network config

Extend the `targets` list (#65) so each endpoint carries its own L2 params:

```yaml
network:
  # Optional per-endpoint network blocks, keyed by context. A node inherits
  # the block for the context it's placed on. The top-level network stays the
  # default for single-L2 clusters (fully backward compatible).
  perContext:
    siteA:
      bridge: vmbr0
      staticIPs: { startIP: 192.168.1.100, gateway: 192.168.1.1, netmask: 24 }
    siteB:
      bridge: vmbr1
      staticIPs: { startIP: 10.20.0.100, gateway: 10.20.0.1, netmask: 24 }
```

- `AllocateIPs` becomes **per-context**: group nodes by their placed context,
  allocate each group from its context's range. Signature grows to return
  `map[node]IP` still, but iterates per context block.
- The VM child's `bridge` is stamped from its context's block, not the single
  `spec.network.bridge`.

### 2. A routable join address (the crux)

A joiner on siteB must reach siteA's first control plane at an address that
*routes between the subnets*. Options, in order of preference:

- **Per-node `node-external-ip` + a routable server URL.** Each node advertises
  a routable address (the VM's IP if the subnets are routed to each other via
  the homelab's gateway/firewall, or a VPN/overlay address otherwise). The
  join URL uses the first CP's routable address, not its bare `vmIP`. Concretely:
  the K3sNode/agent specs gain an optional `routableIP` (or
  `status.routableIP`), and `joinURLFrom` resolves to *that* when set, falling
  back to `status.vmIP` for the single-L2 case.
- If the two subnets are **already routed** at the homelab layer (common: one
  router, two VLANs with inter-VLAN routing), `routableIP == vmIP` and little
  changes beyond letting each subnet have its own range — this is the cheap
  path worth supporting first.
- If they are **not routed** (two physically separate sites), a WireGuard mesh
  or an existing overlay (Tailscale/Netbird) supplies the routable addresses;
  openctl would need to know each node's overlay IP (config or a discovery
  hook). This is the expensive path — defer behind the routed-VLAN case.

### 3. Cross-subnet pod overlay

Set k3s's flannel backend to **`wireguard-native`** (encrypted, routes across
subnets without L2 adjacency) and pass each node's `--node-external-ip` so
flannel advertises the routable address. This is a k3s install-arg change on
the K3sNode provider, gated on the cluster being multi-L2:

- Add `spec.network.flannelBackend` (default `vxlan` = today's behavior;
  `wireguard-native` for multi-L2).
- The K3sNode install command appends `--flannel-backend=<backend>` and
  `--node-external-ip=<routableIP>` when set.

## Decisions (each needs a call; recommendation given)

1. **Scope of v1: routed-VLAN only, or full separate-site?**
   **(Recommended) Ship routed-VLAN first** (two subnets with inter-VLAN
   routing at the homelab gateway — `routableIP == vmIP`, per-context ranges +
   bridges + `wireguard-native`). It covers the realistic homelab case (one
   Proxmox cluster, VLAN-segmented) with no new moving parts. Treat true
   separate-site (WireGuard mesh / overlay) as a follow-on.
2. **Where routable IPs come from.** Recommended: an optional per-node
   `routableIP` (config-supplied or equal to `vmIP` for routed VLANs); an
   overlay-discovery hook is the separate-site follow-on.
3. **Flannel backend default.** Recommended: keep `vxlan` default (zero change
   for existing single-L2 clusters); opt into `wireguard-native` only when
   `network.perContext` (or an explicit `flannelBackend`) is set.
4. **Config shape.** Recommended: additive `network.perContext` map keyed by
   context, inheriting from top-level `network` — single-L2 clusters are
   untouched, and it composes cleanly with the #65 `targets`.
5. **IP allocation across subnets.** Recommended: per-context allocation from
   each block's `startIP`; reject a cluster that places a node on a context
   lacking a network block (fail fast at Plan, like the #65 unknown-context
   check).

## Implementation sketch

- `resources.ClusterSpec.Network` gains `PerContext map[string]NetworkBlock`
  and `FlannelBackend string`; `AllocateIPs` groups nodes by placed context and
  allocates per block.
- `cluster_plan.go`: stamp each VM's `bridge` + IP from its context block;
  stamp `routableIP` on K3sNode/agent specs; when multi-L2, add
  `--flannel-backend` + `--node-external-ip` to the K3sNode install args and
  point `joinURLFrom` at `status.routableIP`.
- K3sNode provider: thread the two new install args (`node_ops.go` install
  command builder).
- Validation: Plan rejects a placement on a context with no network block.

## Testing plan (no two-site hardware for the unit layer)

- `AllocateIPs` per-context: nodes split across two blocks get IPs from the
  right ranges; a node on a block-less context fails fast.
- Plan output: VMs carry the right per-context bridge + IP; K3sNode install
  args include `--flannel-backend=wireguard-native` + `--node-external-ip` only
  when multi-L2; `joinURLFrom` resolves to `routableIP`.
- Backward compat: a single-L2 cluster (no `perContext`) produces byte-identical
  Plan output to today.
- The **one** gate that needs hardware: a real two-VLAN (or two-site) apply
  reaching Ready with cross-subnet pod-to-pod traffic. That's the rollout
  validation, not a unit test.

## Non-goals

- True multi-*site* over the public internet in v1 (routed-VLAN first).
- Managing the homelab's inter-VLAN routing / firewall rules (assumed to
  exist).
- Replacing flannel with Cilium/Calico (a separate, larger choice).
- Per-node subnet auto-discovery — subnets are configured, not inferred.

## Rollout

Ship the routed-VLAN slice behind the additive `network.perContext` config
(absent = today's single-L2 behavior, unchanged). Unit-test the allocation +
Plan-arg layer. Validate on a two-VLAN homelab setup (the hardware gate).
Separate-site / overlay support is a later epic. See the shipped multi-endpoint
foundation (#64/#65) this builds on.
