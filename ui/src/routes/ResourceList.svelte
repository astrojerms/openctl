<script lang="ts">
  import { onDestroy } from 'svelte';
  import { schemas, UnauthorizedError } from '../lib/api';
  import {
    createResourceListState, resourceRowDrift, resourceRowStatus,
    type ResourceListRow,
  } from '../lib/resourceListState';
  import { routeHref } from '../lib/router';
  import { canEditDesired } from '../lib/authority';

  export let apiVersion: string;
  export let kind: string;
  export let provider: string;
  export let observedOnly = false;
  export let schemaAvailable = false;

  const list = createResourceListState();
  $: list.select(apiVersion, kind, observedOnly);
  $: rows = $list.rows;

  let showSchema = false;
  let cueSource = '';
  let schemaLoading = false;
  let schemaError = '';
  let schemaKey = '';
  let schemaController = new AbortController();

  $: resetSchema(apiVersion, kind, schemaAvailable);

  function resetSchema(av: string, k: string, available: boolean) {
    const key = `${av}/${k}/${available}`;
    if (key === schemaKey) return;
    schemaKey = key;
    schemaController.abort();
    schemaController = new AbortController();
    showSchema = false;
    cueSource = '';
    schemaLoading = false;
    schemaError = '';
  }

  async function toggleSchema() {
    if (!schemaAvailable) return;
    showSchema = !showSchema;
    if (!showSchema || cueSource || schemaLoading) return;
    const current = schemaController;
    schemaLoading = true;
    schemaError = '';
    try {
      const response = await schemas.get(apiVersion, kind, { signal: current.signal });
      if (schemaController !== current || current.signal.aborted) return;
      cueSource = response.cueSource ?? '';
      if (!cueSource) schemaError = 'No schema available for this kind.';
    } catch (error) {
      if (schemaController !== current || current.signal.aborted) return;
      if (error instanceof UnauthorizedError) {
        current.abort();
        cueSource = '';
        showSchema = false;
        schemaLoading = false;
        list.stop();
        return;
      }
      schemaError = error instanceof Error ? error.message : String(error);
    } finally {
      if (schemaController === current && !current.signal.aborted) schemaLoading = false;
    }
  }

  onDestroy(() => {
    schemaController.abort();
    list.stop();
  });

  let filter = '';
  type SortKey = 'name' | 'state' | 'drift';
  type SortDir = 'asc' | 'desc';
  let sortKey: SortKey = 'name';
  let sortDir: SortDir = 'asc';

  function toggleSort(key: SortKey) {
    if (sortKey === key) {
      sortDir = sortDir === 'asc' ? 'desc' : 'asc';
    } else {
      sortKey = key;
      sortDir = 'asc';
    }
  }

  function sortIndicator(key: SortKey): string {
    if (sortKey !== key) return '';
    return sortDir === 'asc' ? ' ▲' : ' ▼';
  }

  function matchesFilter(row: ResourceListRow, query: string): boolean {
    const metadata = row.saved?.metadata ?? row.observed?.metadata;
    return [
      row.name, row.saved ? 'saved desired intent' : 'observed only',
      row.saved?.source ?? '', resourceRowStatus(row).label,
      ...Object.entries(metadata?.labels ?? {}).flat(),
    ].some((value) => value.toLowerCase().includes(query));
  }

  $: visibleRows = (() => {
    const query = filter.trim().toLowerCase();
    const matches = query ? rows.filter((row) => matchesFilter(row, query)) : rows.slice();
    const direction = sortDir === 'asc' ? 1 : -1;
    matches.sort((a, b) => {
      let comparison = 0;
      switch (sortKey) {
        case 'name':
          comparison = a.name.localeCompare(b.name);
          break;
        case 'state':
          comparison = resourceRowStatus(a).label.localeCompare(resourceRowStatus(b).label);
          break;
        case 'drift': {
          const left = resourceRowDrift(a);
          const right = resourceRowDrift(b);
          comparison = left === null ? right === null ? 0 : 1 : right === null ? -1 : left - right;
          break;
        }
      }
      return comparison * direction || a.name.localeCompare(b.name);
    });
    return matches;
  })();
</script>

<section>
  <header>
    <div>
      <h2>{kind}</h2>
      <p class="path">{provider || 'Provider unavailable'} · {apiVersion}</p>
    </div>
    <span class="badge-count">{rows.length}</span>
    {#if schemaAvailable}
      <button class="schema-btn" class:active={showSchema} on:click={toggleSchema}
        title="View the CUE schema for this kind">
        {showSchema ? 'Hide schema' : 'Schema'}
      </button>
    {/if}
    {#if $canEditDesired && schemaAvailable && !observedOnly}
      <a class="new-btn" href={routeHref({ name: 'create', apiVersion, kind })}>+ New {kind}</a>
    {/if}
  </header>

  {#if showSchema}
    <div class="schema-panel">
      {#if schemaLoading}
        <p class="muted small">Loading schema…</p>
      {:else if schemaError}
        <p class="err small">{schemaError}</p>
      {:else}
        <pre class="cue">{cueSource}</pre>
      {/if}
    </div>
  {/if}

  {#if observedOnly}
    <p class="muted small">Discovery observations only; this kind does not represent saved desired resources.</p>
  {:else if !schemaAvailable}
    <p class="muted small">The schema is unavailable. Saved desired intent remains inspectable; schema and provider capabilities are not assumed.</p>
  {/if}
  {#if $list.savedError}
    <p class="err small" role="alert">Saved metadata unavailable: {$list.savedError}
      {$list.savedKnown ? 'Showing the last saved metadata snapshot.' : 'Saved inventory is unknown.'}</p>
  {/if}
  {#if $list.observationError}
    <p class="err small" role="alert">Live observation unavailable: {$list.observationError} Retained observations are stale.</p>
  {/if}

  {#if rows.length === 0}
    {#if $list.savedLoading || $list.liveLoading}
      <p class="muted">Loading…</p>
    {:else if observedOnly}
      <p class="muted">No observations returned.</p>
    {:else if !$list.savedKnown}
      <p class="muted">Saved inventory is unknown.</p>
    {:else}
      <p class="muted">No saved desired resources or live observations returned.</p>
    {/if}
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
          <th>Inventory</th>
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
        {#each visibleRows as row (row.name)}
          {@const badge = resourceRowStatus(row)}
          {@const driftCount = resourceRowDrift(row)}
          <tr>
            <td>
              <a href={routeHref({ name: 'detail', apiVersion, kind, resourceName: row.name })}>
                {row.name}
              </a>
            </td>
            <td class="small" title={row.saved ? [row.saved.source, row.saved.appliedAt].filter(Boolean).join(' · ') : ''}>
              {row.saved ? 'Saved desired intent' : 'Observed only'}
            </td>
            <td>
              <span class="state state-{badge.tone}">{badge.label}</span>
            </td>
            <td>
              {#if driftCount === null}
                <span class="drift muted">{row.observation === 'stale' ? 'stale / unknown' : 'unknown'}</span>
              {:else if driftCount === 0}
                <span class="drift drift-clean">in sync</span>
              {:else}
                <span class="drift drift-warn" title={row.observed?.drift?.map((drift) => drift.path).join(', ')}>
                  {driftCount} {driftCount === 1 ? 'field' : 'fields'}
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
  .schema-btn {
    background: transparent;
    color: #ccc;
    border: 1px solid rgba(127, 127, 127, 0.35);
    padding: 0.4em 0.85em;
    border-radius: 6px;
    font-size: 0.85rem;
    cursor: pointer;
  }
  .schema-btn:hover, .schema-btn.active {
    background: rgba(127, 127, 127, 0.12);
    color: #fff;
  }
  .schema-panel {
    margin-bottom: 1.25rem;
    border: 1px solid rgba(127, 127, 127, 0.25);
    border-radius: 8px;
    overflow: hidden;
  }
  .cue {
    margin: 0;
    padding: 1rem 1.1rem;
    overflow-x: auto;
    max-height: 26rem;
    overflow-y: auto;
    font-family: ui-monospace, SFMono-Regular, monospace;
    font-size: 0.8rem;
    line-height: 1.5;
    background: rgba(0, 0, 0, 0.2);
    white-space: pre;
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
