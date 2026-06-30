<script lang="ts">
  import { onDestroy } from 'svelte';
  import { resources, UnauthorizedError, type GetResourceResponse, type Resource, type ResourceRef } from '../lib/api';
  import { watchResources } from '../lib/watch';
  import { statusBadge, type StatusBadge } from '../lib/format';
  import { routeHref } from '../lib/router';

  export let apiVersion: string;
  export let kind: string;
  export let resourceName: string;

  let data: GetResourceResponse | null = null;
  let loading = true;
  let error = '';

  let controller: AbortController | null = null;
  let watching = '';

  // U6: per-child status pills. Keyed by `<apiVersion>/<kind>/<name>`;
  // value is `null` while fetching, an object once resolved. We only
  // populate this for composite resources (parent has children). Each
  // entry is one extra Get RPC at mount — fine for the homelab fanout
  // (a Cluster has ≤ ~10 children in practice).
  type ChildState = { badge: StatusBadge; driftCount: number; error?: string };
  let childStates: Record<string, ChildState | null> = {};

  function childKey(ref: ResourceRef): string {
    return `${ref.apiVersion}/${ref.kind}/${ref.name}`;
  }

  // Reactive re-fetch + re-subscribe on route change.
  $: void switchTo(apiVersion, kind, resourceName);

  async function switchTo(av: string, k: string, n: string) {
    const target = `${av}/${k}/${n}`;
    if (target === watching) return;
    watching = target;
    controller?.abort();
    controller = new AbortController();
    data = null;
    loading = true;
    error = '';
    childStates = {};
    await load(av, k, n);
    void subscribe(av, k, n, controller.signal);
  }

  async function load(av: string, k: string, n: string) {
    try {
      data = await resources.get(av, k, n);
      error = '';
      // Fan out one Get per child to populate per-row badges.
      void loadChildStates(data.resource);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      loading = false;
    }
  }

  async function loadChildStates(parent: Resource) {
    const refs = parent.children ?? [];
    if (refs.length === 0) return;
    // Seed all keys as "loading" up-front so the row count is stable.
    const seed: Record<string, ChildState | null> = {};
    for (const ref of refs) seed[childKey(ref)] = null;
    childStates = seed;

    await Promise.all(refs.map(async (ref) => {
      try {
        const r = await resources.get(ref.apiVersion, ref.kind, ref.name);
        childStates[childKey(ref)] = {
          badge: statusBadge(r.resource.status),
          driftCount: r.resource.drift?.length ?? 0,
        };
      } catch (err) {
        childStates[childKey(ref)] = {
          badge: { label: '—', tone: 'unknown' },
          driftCount: 0,
          error: err instanceof Error ? err.message : String(err),
        };
      }
      // Trigger Svelte reactivity on object-key mutation.
      childStates = { ...childStates };
    }));
  }

  async function subscribe(av: string, k: string, n: string, signal: AbortSignal) {
    // Single-resource Watch — any event triggers a re-Get so the applied
    // manifest + drift come along, not just the observed state Watch
    // carries. Cheap: one extra RPC per change to this one resource.
    let backoffMs = 1000;
    while (!signal.aborted) {
      try {
        await watchResources(av, k, n, () => {
          void load(av, k, n);
        }, { signal });
        backoffMs = 1000;
      } catch (err) {
        if (signal.aborted) return;
        if (err instanceof UnauthorizedError) return;
        if (err instanceof DOMException && err.name === 'AbortError') return;
        // Stale-cache fallback: surface the error but keep showing
        // whatever we last fetched successfully.
        error = err instanceof Error ? err.message : String(err);
        backoffMs = Math.min(backoffMs * 2, 15_000);
      }
      await new Promise((r) => setTimeout(r, backoffMs));
    }
  }

  onDestroy(() => controller?.abort());

  // Pretty-print JSON for the desired/observed panes. The proto wire
  // format is JSON-on-the-fence anyway (grpc-gateway), and the editor
  // in U4 will switch to Monaco-YAML; for read-only display, JSON is the
  // honest representation.
  function pretty(v: unknown): string {
    if (v === undefined || v === null) return '—';
    try {
      return JSON.stringify(v, null, 2);
    } catch {
      return String(v);
    }
  }

  function timeAgo(rfc3339: string | undefined): string {
    if (!rfc3339) return '';
    const t = new Date(rfc3339);
    if (Number.isNaN(t.getTime())) return rfc3339;
    const seconds = Math.floor((Date.now() - t.getTime()) / 1000);
    if (seconds < 60) return `${seconds}s ago`;
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
    if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
    return `${Math.floor(seconds / 86400)}d ago`;
  }

  $: state = data ? statusBadge(data.resource.status) : null;

  // Manual reconcile: re-submit the stored manifest through the normal
  // Apply path so the dispatcher pushes desired state over observed. The
  // resulting op surfaces in the bottom drawer like any other apply, so
  // there's no separate "reconcile op" type to add on the wire.
  let reconciling = false;
  let reconcileMsg = '';
  let reconcileErr = '';

  async function doReconcile() {
    if (reconciling || !data?.applied) return;
    reconciling = true;
    reconcileMsg = '';
    reconcileErr = '';
    try {
      const resp = await resources.apply({
        resource: {
          apiVersion: data.applied.apiVersion,
          kind: data.applied.kind,
          metadata: { name: data.applied.metadata?.name ?? resourceName },
          spec: data.applied.spec as Record<string, unknown> | undefined,
        },
      });
      reconcileMsg = resp.operationId
        ? `Reconcile submitted (operation ${resp.operationId})`
        : 'Reconcile submitted';
    } catch (err) {
      if (err instanceof UnauthorizedError) return;
      reconcileErr = err instanceof Error ? err.message : String(err);
    } finally {
      reconciling = false;
    }
  }

  // U6 aggregations: count children we've heard back from that are
  // drifted or in a non-good state. Reactive on childStates so the
  // numbers tick up as the fanout completes.
  $: childTotal = data?.resource.children?.length ?? 0;
  $: childDrifted = Object.values(childStates).filter(
    (s): s is ChildState => s !== null && s.driftCount > 0,
  ).length;
  $: childUnhealthy = Object.values(childStates).filter(
    (s): s is ChildState => s !== null && (s.badge.tone === 'bad' || s.badge.tone === 'warn'),
  ).length;
</script>

<section>
  <header>
    <div>
      <p class="crumbs">
        <a href={routeHref({ name: 'list', apiVersion, kind })}>{kind}</a>
        <span> · </span>
        <span class="path">{apiVersion}</span>
      </p>
      <h2>{resourceName}</h2>
    </div>
    <div class="header-right">
      {#if state}
        <span class="state state-{state.tone}">{state.label}</span>
      {/if}
      {#if childTotal > 0}
        <span class="state state-{childDrifted > 0 || childUnhealthy > 0 ? 'warn' : 'good'}" title="Aggregated child status">
          {childTotal} {childTotal === 1 ? 'child' : 'children'}
          {#if childDrifted > 0} · {childDrifted} drifted{/if}
          {#if childUnhealthy > 0} · {childUnhealthy} unhealthy{/if}
        </span>
      {/if}
      <button
        type="button"
        class="reconcile-btn"
        disabled={reconciling || !data?.applied}
        title={data?.applied
          ? 'Re-apply the stored manifest to push desired state over observed'
          : 'No applied manifest on file — nothing to reconcile from'}
        on:click={doReconcile}
      >
        {reconciling ? 'Reconciling…' : 'Reconcile'}
      </button>
      <a class="edit-btn" href={routeHref({ name: 'edit', apiVersion, kind, resourceName })}>Edit</a>
    </div>
  </header>

  {#if reconcileMsg}
    <p class="muted small reconcile-msg">{reconcileMsg}</p>
  {/if}
  {#if reconcileErr}
    <p class="err small">{reconcileErr}</p>
  {/if}

  {#if loading}
    <p class="muted">Loading…</p>
  {:else if error}
    <p class="err">{error}</p>
  {:else if data}
    {@const drift = data.resource.drift ?? []}
    {@const lastApplied = data.appliedAt}

    {#if lastApplied}
      <p class="muted small">
        Last applied {timeAgo(lastApplied)}
        <span class="path"> · {lastApplied}</span>
      </p>
    {:else}
      <p class="muted small">
        No applied manifest on file — this resource was created out-of-band.
      </p>
    {/if}

    {#if drift.length > 0}
      <article class="card drift-card">
        <h3>Drift ({drift.length})</h3>
        <table>
          <thead>
            <tr><th>Path</th><th>Desired</th><th>Observed</th></tr>
          </thead>
          <tbody>
            {#each drift as d}
              <tr>
                <td class="mono">{d.path}</td>
                <td class="mono">{d.desired}</td>
                <td class="mono">{d.observed}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </article>
    {/if}

    <div class="grid">
      <article class="card">
        <h3>Desired manifest</h3>
        {#if data.applied}
          <pre>{pretty({
              apiVersion: data.applied.apiVersion,
              kind: data.applied.kind,
              metadata: data.applied.metadata,
              spec: data.applied.spec,
            })}</pre>
        {:else}
          <p class="muted">No manifest on file.</p>
        {/if}
      </article>

      <article class="card">
        <h3>Observed state</h3>
        <pre>{pretty(data.resource.status)}</pre>
      </article>
    </div>

    {@const ownerRefs = data.resource.metadata?.ownerRefs ?? []}
    {@const children = data.resource.children ?? []}

    {#if ownerRefs.length > 0}
      <article class="card owner-card">
        <h3>Owner</h3>
        <p class="muted small">
          This resource is owned by another and can only be modified by
          editing its owner. To change anything here, open the owner instead.
        </p>
        <ul class="ref-list">
          {#each ownerRefs as owner}
            <li>
              <a href={routeHref({ name: 'detail', apiVersion: owner.apiVersion, kind: owner.kind, resourceName: owner.name })}>
                <span class="ref-kind">{owner.kind}</span>
                <span class="ref-name">{owner.name}</span>
                <span class="path"> · {owner.apiVersion}</span>
              </a>
            </li>
          {/each}
        </ul>
      </article>
    {/if}

    {#if children.length > 0}
      <article class="card">
        <h3>Children ({children.length})</h3>
        <p class="muted small">
          Composed resources are read-only — edit this resource to add,
          remove, or respec them.
        </p>
        <ul class="ref-list">
          {#each children as child}
            {@const s = childStates[childKey(child)]}
            <li>
              <a href={routeHref({ name: 'detail', apiVersion: child.apiVersion, kind: child.kind, resourceName: child.name })}>
                <span class="ref-kind">{child.kind}</span>
                <span class="ref-name">{child.name}</span>
                {#if s === null}
                  <span class="state state-unknown shimmer">loading…</span>
                {:else if s?.error}
                  <span class="state state-bad" title={s.error}>error</span>
                {:else if s}
                  <span class="state state-{s.badge.tone}">{s.badge.label}</span>
                  {#if s.driftCount > 0}
                    <span class="state state-warn" title="{s.driftCount} field{s.driftCount === 1 ? '' : 's'} drifted">
                      drift {s.driftCount}
                    </span>
                  {/if}
                {/if}
                <span class="path"> · {child.apiVersion}</span>
              </a>
            </li>
          {/each}
        </ul>
      </article>
    {/if}
  {/if}
</section>

<style>
  section {
    max-width: 72rem;
  }
  header {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 1rem;
    margin-bottom: 1rem;
  }
  .crumbs {
    margin: 0 0 0.25rem;
    color: #888;
    font-size: 0.85rem;
  }
  .crumbs a {
    color: #6ea8ff;
    text-decoration: none;
  }
  .crumbs a:hover {
    text-decoration: underline;
  }
  .path {
    font-family: ui-monospace, SFMono-Regular, monospace;
    font-size: 0.8em;
    color: #888;
  }
  h2 {
    margin: 0;
    font-size: 1.4rem;
  }
  h3 {
    margin: 0 0 0.75rem;
    font-size: 0.95rem;
    color: #aaa;
  }
  .header-right {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }
  .edit-btn {
    background: #4a8ef0;
    color: white;
    padding: 0.4em 1em;
    border-radius: 6px;
    font-size: 0.9rem;
    text-decoration: none;
    font-weight: 500;
  }
  .edit-btn:hover {
    background: #3a7ee0;
  }
  .reconcile-btn {
    background: transparent;
    color: #4a8ef0;
    border: 1px solid #4a8ef0;
    padding: 0.4em 1em;
    border-radius: 6px;
    font-size: 0.9rem;
    font-weight: 500;
    cursor: pointer;
  }
  .reconcile-btn:hover:not(:disabled) {
    background: rgba(74, 142, 240, 0.1);
  }
  .reconcile-btn:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
  .reconcile-msg {
    color: #4a8ef0;
  }
  .state {
    padding: 0.15em 0.7em;
    border-radius: 999px;
    font-size: 0.85rem;
    font-weight: 500;
  }
  .state-good { background: rgba(46, 160, 67, 0.18); color: #5fdb78; }
  .state-warn { background: rgba(255, 184, 0, 0.18); color: #ffce4d; }
  .state-bad  { background: rgba(248, 81, 73, 0.18); color: #ff8980; }
  .state-unknown { background: rgba(127, 127, 127, 0.18); color: #aaa; }
  .small {
    font-size: 0.85rem;
  }
  .muted {
    color: #888;
  }
  .err {
    color: #f57171;
  }
  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 1rem;
    margin: 1.25rem 0;
  }
  @media (max-width: 70rem) {
    .grid {
      grid-template-columns: 1fr;
    }
  }
  .card {
    background: #232323;
    border-radius: 8px;
    padding: 1.25rem 1.5rem;
    margin: 0 0 1rem;
  }
  .drift-card {
    border-left: 3px solid #ffce4d;
  }
  @media (prefers-color-scheme: light) {
    .card {
      background: #fff;
      box-shadow: 0 1px 4px rgba(0, 0, 0, 0.04);
    }
  }
  pre {
    margin: 0;
    padding: 0.75rem 1rem;
    background: rgba(0, 0, 0, 0.25);
    border-radius: 4px;
    overflow-x: auto;
    font-family: ui-monospace, SFMono-Regular, monospace;
    font-size: 0.8rem;
    line-height: 1.5;
  }
  @media (prefers-color-scheme: light) {
    pre {
      background: #f4f4f4;
    }
  }
  .mono {
    font-family: ui-monospace, SFMono-Regular, monospace;
    font-size: 0.85rem;
  }
  table {
    width: 100%;
    border-collapse: collapse;
  }
  th, td {
    text-align: left;
    padding: 0.4rem 0.6rem;
    border-bottom: 1px solid rgba(127, 127, 127, 0.18);
  }
  th {
    font-size: 0.75rem;
    color: #888;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .owner-card {
    border-left: 3px solid #6ea8ff;
  }
  .ref-list {
    list-style: none;
    margin: 0.5rem 0 0;
    padding: 0;
  }
  .ref-list li {
    padding: 0.4rem 0;
    border-bottom: 1px solid rgba(127, 127, 127, 0.12);
  }
  .ref-list li:last-child {
    border-bottom: none;
  }
  .ref-list a {
    display: inline-flex;
    align-items: baseline;
    gap: 0.5rem;
    color: inherit;
    text-decoration: none;
  }
  .ref-list a:hover .ref-name {
    text-decoration: underline;
  }
  .ref-kind {
    color: #aaa;
    font-size: 0.85rem;
  }
  .ref-name {
    color: #6ea8ff;
    font-weight: 500;
  }
  .ref-list .state {
    font-size: 0.72rem;
    padding: 0.1em 0.55em;
  }
  .shimmer {
    opacity: 0.55;
  }
</style>
