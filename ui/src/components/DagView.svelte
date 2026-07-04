<script lang="ts">
  import { resources, type GraphNode, type GraphEdge } from '../lib/api';
  import { operationStatusBadge } from '../lib/format';
  import { ops as opsStore } from '../lib/ops';
  import { focusOpsDrawer } from '../lib/opsDrawer';
  import type { OperationRow } from '../lib/watch';

  export let apiVersion: string;
  export let kind: string;
  export let resourceName: string;
  // Bump to force a re-fetch (Phase U9.3: Detail increments this on every
  // Watch-driven reload so node status pills stay live).
  export let version = 0;

  let nodes: GraphNode[] = [];
  let edges: GraphEdge[] = [];
  let loading = true;
  let error = '';
  let loadedKey = '';

  // Re-fetch on identity change or an explicit version bump.
  $: void maybeLoad(apiVersion, kind, resourceName, version);

  async function maybeLoad(av: string, k: string, n: string, v: number) {
    const key = `${av}/${k}/${n}#${v}`;
    if (key === loadedKey) return;
    loadedKey = key;
    try {
      const resp = await resources.childrenGraph(av, k, n);
      nodes = resp.nodes ?? [];
      edges = resp.edges ?? [];
      error = '';
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      loading = false;
    }
  }

  // --- Layout ------------------------------------------------------------
  // Hand-rolled longest-path layered layout. The graphs U9 targets are tiny
  // (5–15 nodes), so an O(V·E) relaxation is cheap and avoids pulling in a
  // graph-layout dependency (which the strict artifact/asset CSP would also
  // complicate). Each edge pushes its target at least one layer below its
  // source; capping the relaxation at |V| passes keeps it terminating even
  // if a malformed graph ever contained a cycle.
  const BOX_W = 150;
  const BOX_H = 58;
  const GAP_X = 28;
  const GAP_Y = 56;
  const PAD = 20;

  type Placed = { node: GraphNode; x: number; y: number };

  let placed: Placed[] = [];
  let posById: Record<string, Placed> = {};
  let width = 0;
  let height = 0;
  let opsByResource: Record<string, OperationRow> = {};

  $: layout(nodes, edges);
  $: opsByResource = indexOpsByResource($opsStore);

  function layout(ns: GraphNode[], es: GraphEdge[]) {
    if (ns.length === 0) {
      placed = [];
      posById = {};
      width = 0;
      height = 0;
      return;
    }
    const layer: Record<string, number> = {};
    const rootId = ns.find((n) => n.root)?.id ?? ns[0].id;
    for (const n of ns) layer[n.id] = n.id === rootId ? 0 : 0;
    // Relax: layer[to] = max(layer[to], layer[from] + 1). |V| passes bound it.
    for (let pass = 0; pass < ns.length; pass++) {
      let changed = false;
      for (const e of es) {
        if (!(e.from in layer) || !(e.to in layer)) continue;
        const cand = layer[e.from] + 1;
        if (cand > layer[e.to]) {
          layer[e.to] = cand;
          changed = true;
        }
      }
      if (!changed) break;
    }

    // Bucket by layer, preserving node insertion order for stable columns.
    const byLayer: Record<number, GraphNode[]> = {};
    let maxLayer = 0;
    for (const n of ns) {
      const l = layer[n.id];
      (byLayer[l] ??= []).push(n);
      if (l > maxLayer) maxLayer = l;
    }
    const widest = Math.max(...Object.values(byLayer).map((row) => row.length));
    const contentW = widest * BOX_W + (widest - 1) * GAP_X;

    const next: Placed[] = [];
    const byId: Record<string, Placed> = {};
    for (let l = 0; l <= maxLayer; l++) {
      const row = byLayer[l] ?? [];
      const rowW = row.length * BOX_W + (row.length - 1) * GAP_X;
      const offsetX = PAD + (contentW - rowW) / 2; // centre each row
      row.forEach((node, i) => {
        const p = {
          node,
          x: offsetX + i * (BOX_W + GAP_X),
          y: PAD + l * (BOX_H + GAP_Y),
        };
        next.push(p);
        byId[node.id] = p;
      });
    }
    placed = next;
    posById = byId;
    width = contentW + PAD * 2;
    height = (maxLayer + 1) * BOX_H + maxLayer * GAP_Y + PAD * 2;
  }

  // Edge path: from source box bottom-centre to target box top-centre, with a
  // gentle vertical cubic so overlapping straight lines stay distinguishable.
  function edgePath(e: GraphEdge): string {
    const a = posById[e.from];
    const b = posById[e.to];
    if (!a || !b) return '';
    const x1 = a.x + BOX_W / 2;
    const y1 = a.y + BOX_H;
    const x2 = b.x + BOX_W / 2;
    const y2 = b.y;
    // Same/upward layer (ref edges can point sideways): route from side.
    if (b.y <= a.y) {
      const sx1 = a.x + BOX_W;
      const sy1 = a.y + BOX_H / 2;
      const sx2 = b.x;
      const sy2 = b.y + BOX_H / 2;
      const mx = (sx1 + sx2) / 2;
      return `M ${sx1} ${sy1} C ${mx} ${sy1}, ${mx} ${sy2}, ${sx2} ${sy2}`;
    }
    const my = (y1 + y2) / 2;
    return `M ${x1} ${y1} C ${x1} ${my}, ${x2} ${my}, ${x2} ${y2}`;
  }

  function toneClass(node: GraphNode): string {
    if (!node.managed) return 'dim';
    switch (node.status) {
      case 'applied':
        return 'good';
      case 'pending':
        return 'warn';
      case 'missing':
        return 'bad';
      default:
        return 'unknown';
    }
  }

  const INTERESTING_OP_STATUSES = new Set(['pending', 'running', 'failed', 'interrupted', 'canceled']);

  function resourceKey(apiVersion: string | undefined, k: string | undefined, name: string | undefined): string {
    if (!apiVersion || !k || !name) return '';
    return `${apiVersion}/${k}/${name}`;
  }

  function flattenOps(list: OperationRow[]): OperationRow[] {
    const out: OperationRow[] = [];
    for (const op of list) {
      out.push(op);
      if (op.children?.length) out.push(...flattenOps(op.children));
    }
    return out;
  }

  function opTime(op: OperationRow): number {
    const raw = op.completedAt || op.startedAt || op.submittedAt || '';
    const t = Date.parse(raw);
    return Number.isFinite(t) ? t : 0;
  }

  function indexOpsByResource(list: OperationRow[]): Record<string, OperationRow> {
    const next: Record<string, OperationRow> = {};
    for (const op of flattenOps(list)) {
      if (!INTERESTING_OP_STATUSES.has(op.status)) continue;
      const key = resourceKey(op.apiVersion, op.kind, op.resourceName);
      if (!key) continue;
      const current = next[key];
      if (!current || opTime(op) >= opTime(current)) {
        next[key] = op;
      }
    }
    return next;
  }

  function opFor(node: GraphNode): OperationRow | null {
    return opsByResource[resourceKey(node.apiVersion, node.kind, node.name)] ?? null;
  }

  function opTone(op: OperationRow | null): string {
    return operationStatusBadge(op?.status).tone;
  }

  function openNodeOps(node: GraphNode) {
    focusOpsDrawer({
      apiVersion: node.apiVersion,
      kind: node.kind,
      resourceName: node.name,
    });
  }

  function onNodeKeydown(event: KeyboardEvent, node: GraphNode) {
    if (event.key !== 'Enter' && event.key !== ' ') return;
    event.preventDefault();
    openNodeOps(node);
  }
</script>

{#if !loading && nodes.length > 1}
  <article class="card">
    <div class="head">
      <h3>Topology ({nodes.length})</h3>
      <div class="legend">
        <span class="key"><span class="swatch owns"></span>owns</span>
        <span class="key"><span class="swatch ref"></span>ref</span>
      </div>
    </div>
    {#if error}
      <p class="muted small err">Graph unavailable: {error}</p>
    {/if}
    <div class="scroll">
      <svg {width} {height} viewBox="0 0 {width} {height}" role="img" aria-label="Composite resource topology">
        <defs>
          <marker id="dag-arrow-owns" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
            <path d="M0,0 L8,4 L0,8 z" fill="#6b6b6b" />
          </marker>
          <marker id="dag-arrow-ref" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
            <path d="M0,0 L8,4 L0,8 z" fill="#4a8ef0" />
          </marker>
        </defs>

        {#each edges as e}
          {@const d = edgePath(e)}
          {#if d}
            <path
              class="edge {e.relation}"
              {d}
              marker-end={e.relation === 'ref' ? 'url(#dag-arrow-ref)' : 'url(#dag-arrow-owns)'}
            >
              {#if e.field}<title>ref → {e.field}</title>{/if}
            </path>
          {/if}
        {/each}

        {#each placed as p}
          {@const op = opFor(p.node)}
          <g
            class="node node-click {toneClass(p.node)}"
            class:root={p.node.root}
            transform="translate({p.x},{p.y})"
            role="button"
            tabindex="0"
            on:click={() => openNodeOps(p.node)}
            on:keydown={(event) => onNodeKeydown(event, p.node)}
          >
            <rect width={BOX_W} height={BOX_H} rx="7" />
            <text class="kind" x="10" y="18">{p.node.kind}</text>
            <text class="name" x="10" y="34">{p.node.name}</text>
            <circle class="pill" cx={BOX_W - 14} cy="15" r="5" />
            {#if op}
              <g class="op op-{opTone(op)}" transform="translate(10,40)">
                <rect width="78" height="14" rx="7" />
                <text x="39" y="10" text-anchor="middle">{op.status}</text>
              </g>
            {:else if !p.node.managed}
              <text class="ro" x="10" y="50">read-only</text>
            {/if}
            <title>
              {p.node.kind}/{p.node.name} · {p.node.managed ? p.node.status : 'observed (unmanaged)'}
              {op ? ` · op ${op.status} ${op.id.slice(0, 12)}` : ' · click for operations'}
            </title>
          </g>
        {/each}
      </svg>
    </div>
  </article>
{/if}

<style>
  .card {
    background: #232323;
    border-radius: 8px;
    padding: 1.25rem 1.5rem;
    margin: 0 0 1rem;
  }
  .head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 1rem;
  }
  h3 {
    margin: 0 0 0.25rem;
  }
  .legend {
    display: flex;
    gap: 0.9rem;
    font-size: 0.78rem;
    color: #aaa;
  }
  .key {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
  }
  .swatch {
    width: 18px;
    height: 0;
    border-top: 2px solid #6b6b6b;
    display: inline-block;
  }
  .swatch.ref {
    border-top: 2px solid #4a8ef0;
  }
  .scroll {
    overflow-x: auto;
    margin-top: 0.75rem;
  }
  svg {
    display: block;
  }
  .err {
    color: #ff8980;
  }
  .muted.small {
    color: #999;
    font-size: 0.85rem;
  }

  .edge {
    fill: none;
    stroke-width: 1.6;
  }
  .edge.owns {
    stroke: #6b6b6b;
    stroke-dasharray: 4 3;
  }
  .edge.ref {
    stroke: #4a8ef0;
  }

  .node-click {
    cursor: pointer;
    outline: none;
  }
  .node rect {
    fill: #2c2c2c;
    stroke: #444;
    stroke-width: 1.5;
    transition: stroke 120ms, fill 120ms;
  }
  .node-click:hover > rect,
  .node-click:focus > rect {
    stroke: #4a8ef0;
    fill: #313131;
  }
  .node.root rect {
    fill: #26324a;
    stroke: #4a8ef0;
  }
  .node.dim rect {
    fill: #242424;
    stroke: #3a3a3a;
    stroke-dasharray: 4 3;
  }
  .node .kind {
    fill: #e6e6e6;
    font-size: 12px;
    font-weight: 600;
  }
  .node .name {
    fill: #b7b7b7;
    font-size: 11px;
  }
  .node.dim .kind,
  .node.dim .name {
    fill: #777;
  }
  .node .ro {
    fill: #777;
    font-size: 9px;
    font-style: italic;
  }
  .pill {
    fill: #7f7f7f;
  }
  .node.good .pill {
    fill: #5fdb78;
  }
  .node.warn .pill {
    fill: #ffce4d;
  }
  .node.bad .pill {
    fill: #ff8980;
  }
  .node.dim .pill {
    fill: #555;
  }
  .op rect {
    stroke: none;
  }
  .op text {
    font-size: 9px;
    font-weight: 600;
  }
  .op-good rect { fill: rgba(46, 160, 67, 0.24); }
  .op-good text { fill: #5fdb78; }
  .op-warn rect { fill: rgba(255, 184, 0, 0.24); }
  .op-warn text { fill: #ffce4d; }
  .op-bad rect { fill: rgba(248, 81, 73, 0.24); }
  .op-bad text { fill: #ff8980; }
  .op-unknown rect { fill: rgba(127, 127, 127, 0.24); }
  .op-unknown text { fill: #aaa; }
  .node-click:focus > rect {
    stroke-width: 2;
  }
</style>
