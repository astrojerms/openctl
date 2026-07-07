<script lang="ts">
  import { onDestroy } from 'svelte';
  import { resources, UnauthorizedError, type Resource } from '../lib/api';
  import { watchResources } from '../lib/watch';
  import { statusBadge } from '../lib/format';
  import { routeHref } from '../lib/router';
  import { canMutate } from '../lib/auth';

  export let apiVersion: string;
  export let kind: string;
  export let provider: string;

  let rows: Resource[] = [];
  let loading = true;
  let error = '';

  let activeController: AbortController | null = null;
  let watching = '';

  $: void switchTo(apiVersion, kind);

  async function switchTo(av: string, k: string) {
    const target = `${av}/${k}`;
    if (target === watching) return;
    watching = target;
    activeController?.abort();
    activeController = new AbortController();
    loading = true;
    error = '';
    rows = [];

    // Initial List then live Watch. List populates immediately even for
    // empty kinds (so loading clears); Watch then merges in deltas. The
    // initial Watch snapshot (one ADDED per existing resource) is
    // idempotent against the List rows because mergeRow upserts by name.
    try {
      const resp = await resources.list(av, k);
      rows = (resp.resources ?? []).slice().sort((a, b) =>
        a.metadata.name.localeCompare(b.metadata.name),
      );
      loading = false;
    } catch (err) {
      if (err instanceof UnauthorizedError) return;
      error = err instanceof Error ? err.message : String(err);
      loading = false;
    }
    void run(av, k, activeController.signal);
  }

  async function run(av: string, k: string, signal: AbortSignal) {
    let backoffMs = 1000;
    while (!signal.aborted) {
      try {
        await watchResources(av, k, undefined, applyEvent, { signal });
        backoffMs = 1000;
      } catch (err) {
        if (signal.aborted) return;
        if (err instanceof UnauthorizedError) return;
        if (err instanceof DOMException && err.name === 'AbortError') return;
        error = err instanceof Error ? err.message : String(err);
        backoffMs = Math.min(backoffMs * 2, 15_000);
      }
      await new Promise((r) => setTimeout(r, backoffMs));
    }
  }

  function applyEvent(e: { type: string; resource: Resource }) {
    // Clear any stream-level error once events flow again.
    error = '';
    const incoming = e.resource;
    rows = mergeRow(rows, e.type, incoming);
  }

  function mergeRow(
    list: Resource[],
    type: string,
    incoming: Resource,
  ): Resource[] {
    const idx = list.findIndex((r) => r.metadata.name === incoming.metadata.name);
    if (type === 'DELETED') {
      if (idx < 0) return list;
      const next = list.slice();
      next.splice(idx, 1);
      return next;
    }
    if (idx >= 0) {
      const next = list.slice();
      next[idx] = incoming;
      return next;
    }
    return [...list, incoming].sort((a, b) =>
      a.metadata.name.localeCompare(b.metadata.name),
    );
  }

  onDestroy(() => {
    activeController?.abort();
  });

  // U8.16: list controls — free-text filter + column sort. Applied
  // client-side over the live `rows` snapshot, so the Watch stream
  // still populates and the view stays consistent.
  let filter = '';
  type SortKey = 'name' | 'state' | 'drift';
  type SortDir = 'asc' | 'desc';
  let sortKey: SortKey = 'name';
  let sortDir: SortDir = 'asc';

  function toggleSort(k: SortKey) {
    if (sortKey === k) {
      sortDir = sortDir === 'asc' ? 'desc' : 'asc';
    } else {
      sortKey = k;
      sortDir = 'asc';
    }
  }

  function sortIndicator(k: SortKey): string {
    if (sortKey !== k) return '';
    return sortDir === 'asc' ? ' ▲' : ' ▼';
  }

  // Reactive filtered+sorted view. Rebuilds on any of {rows, filter,
  // sortKey, sortDir} changing — cheap for the homelab-scale lists
  // this UI is aimed at.
  $: visibleRows = (() => {
    const q = filter.trim().toLowerCase();
    const matches = q === ''
      ? rows.slice()
      : rows.filter((r) => r.metadata.name.toLowerCase().includes(q));
    const dir = sortDir === 'asc' ? 1 : -1;
    matches.sort((a, b) => {
      switch (sortKey) {
        case 'name':
          return a.metadata.name.localeCompare(b.metadata.name) * dir;
        case 'state': {
          const sa = statusBadge(a.status).label;
          const sb2 = statusBadge(b.status).label;
          return sa.localeCompare(sb2) * dir;
        }
        case 'drift':
          return ((a.drift?.length ?? 0) - (b.drift?.length ?? 0)) * dir;
      }
    });
    return matches;
  })();
</script>

<section>
  <header>
    <div>
      <h2>{kind}</h2>
      <p class="path">{provider} · {apiVersion}</p>
    </div>
    <span class="badge-count">{rows.length}</span>
    {#if $canMutate}
      <a class="new-btn" href={routeHref({ name: 'create', apiVersion, kind })}>+ New {kind}</a>
    {/if}
  </header>

  {#if loading}
    <p class="muted">Loading…</p>
  {:else if error}
    <p class="err">{error}</p>
  {:else if rows.length === 0}
    <p class="muted">No resources of this kind yet — click <strong>+ New {kind}</strong> above,
      or apply one via CLI: <code>openctl ctl apply -f manifest.yaml</code></p>
  {:else}
    <div class="list-controls">
      <input
        type="search"
        placeholder="Filter by name…"
        bind:value={filter}
        class="filter-input"
      />
      {#if filter}
        <span class="muted small">{visibleRows.length} of {rows.length}</span>
      {/if}
    </div>
    {#if visibleRows.length === 0}
      <p class="muted">No matches for "{filter}".</p>
    {:else}
    <div class="table-scroll">
    <table>
      <thead>
        <tr>
          <th>
            <button type="button" class="sort-th" on:click={() => toggleSort('name')}>
              Name{sortIndicator('name')}
            </button>
          </th>
          <th>
            <button type="button" class="sort-th" on:click={() => toggleSort('state')}>
              State{sortIndicator('state')}
            </button>
          </th>
          <th>
            <button type="button" class="sort-th" on:click={() => toggleSort('drift')}>
              Drift{sortIndicator('drift')}
            </button>
          </th>
        </tr>
      </thead>
      <tbody>
        {#each visibleRows as r (r.metadata.name)}
          {@const sb = statusBadge(r.status)}
          {@const driftN = r.drift?.length ?? 0}
          <tr>
            <td>
              <a href={routeHref({ name: 'detail', apiVersion, kind, resourceName: r.metadata.name })}>
                {r.metadata.name}
              </a>
            </td>
            <td>
              <span class="state state-{sb.tone}">{sb.label}</span>
            </td>
            <td>
              {#if driftN === 0}
                <span class="drift drift-clean">in sync</span>
              {:else}
                <span class="drift drift-warn" title={r.drift?.map((d) => d.path).join(', ')}>
                  {driftN} {driftN === 1 ? 'field' : 'fields'}
                </span>
              {/if}
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
    </div>
    {/if}
  {/if}
</section>

<style>
  section {
    max-width: 64rem;
  }
  header {
    display: flex;
    align-items: flex-end;
    gap: 1rem;
    margin-bottom: 1.25rem;
  }
  h2 {
    margin: 0;
    font-size: 1.25rem;
  }
  .path {
    color: #888;
    margin: 0.2rem 0 0;
    font-size: 0.85rem;
    font-family: ui-monospace, SFMono-Regular, monospace;
  }
  .badge-count {
    margin-left: auto;
    color: #aaa;
    font-variant-numeric: tabular-nums;
    background: rgba(127, 127, 127, 0.12);
    padding: 0.1em 0.7em;
    border-radius: 999px;
    font-size: 0.85rem;
  }
  .new-btn {
    background: #4a8ef0;
    color: white;
    padding: 0.4em 0.9em;
    border-radius: 6px;
    text-decoration: none;
    font-size: 0.85rem;
    font-weight: 500;
  }
  .new-btn:hover {
    background: #3a7eda;
  }
  .list-controls {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    margin-bottom: 0.75rem;
  }
  .filter-input {
    flex: 0 0 20rem;
    padding: 0.4em 0.7em;
    background: rgba(0, 0, 0, 0.2);
    color: inherit;
    border: 1px solid rgba(127, 127, 127, 0.3);
    border-radius: 6px;
    font-size: 0.85rem;
  }
  @media (prefers-color-scheme: light) {
    .filter-input {
      background: #fff;
      border-color: #ccc;
    }
  }
  .small {
    font-size: 0.8rem;
  }
  .sort-th {
    background: transparent;
    border: none;
    color: inherit;
    font: inherit;
    padding: 0;
    cursor: pointer;
    font-weight: 600;
    color: #888;
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .sort-th:hover {
    color: #ccc;
  }
  .muted {
    color: #888;
  }
  .err {
    color: #f57171;
  }
  code {
    background: rgba(127, 127, 127, 0.15);
    padding: 0 0.3em;
    border-radius: 3px;
  }
  table {
    width: 100%;
    border-collapse: collapse;
  }
  th, td {
    text-align: left;
    padding: 0.55rem 0.75rem;
    border-bottom: 1px solid rgba(127, 127, 127, 0.18);
    font-size: 0.9rem;
  }
  th {
    font-weight: 600;
    color: #888;
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  td a {
    color: #6ea8ff;
    text-decoration: none;
  }
  td a:hover {
    text-decoration: underline;
  }
  .state {
    display: inline-block;
    padding: 0.05em 0.6em;
    border-radius: 999px;
    font-size: 0.8rem;
    font-weight: 500;
  }
  .state-good { background: rgba(46, 160, 67, 0.18); color: #5fdb78; }
  .state-warn { background: rgba(255, 184, 0, 0.18); color: #ffce4d; }
  .state-bad  { background: rgba(248, 81, 73, 0.18); color: #ff8980; }
  .state-unknown { background: rgba(127, 127, 127, 0.18); color: #aaa; }
  .drift {
    font-size: 0.8rem;
  }
  .drift-clean { color: #5fdb78; }
  .drift-warn { color: #ffce4d; }
</style>
