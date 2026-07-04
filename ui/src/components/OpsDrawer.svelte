<script lang="ts">
  import { ops, opsError } from '../lib/ops';
  import { routeHref, navigate } from '../lib/router';
  import { operations as opsApi } from '../lib/api';
  import type { OperationRow } from '../lib/watch';

  // Persisted across re-renders inside the shell. Collapsed by default
  // so it doesn't eat screen real estate on first visit.
  let open = false;
  // Per-op expanded state for the substep checklist.
  let expanded: Record<string, boolean> = {};
  // Per-op pending-cancel state so we can disable the button while the
  // RPC is in flight and surface a row-level error.
  let cancelling: Record<string, boolean> = {};
  let cancelError: Record<string, string> = {};

  // U7 filter controls — all client-side against the in-memory stream.
  // The watcher already streams everything; per-filter re-subscribes
  // would be more code for less convenience here.
  let filterStatus = '';
  let filterSource = '';
  let filterText = '';

  function toggle() {
    open = !open;
  }

  function toggleRow(id: string) {
    expanded = { ...expanded, [id]: !expanded[id] };
  }

  function timeAgo(rfc3339: string | undefined): string {
    if (!rfc3339) return '';
    const t = new Date(rfc3339);
    if (Number.isNaN(t.getTime())) return rfc3339;
    const s = Math.floor((Date.now() - t.getTime()) / 1000);
    if (s < 60) return `${s}s`;
    if (s < 3600) return `${Math.floor(s / 60)}m`;
    if (s < 86400) return `${Math.floor(s / 3600)}h`;
    return `${Math.floor(s / 86400)}d`;
  }

  function statusTone(s: string): string {
    switch (s) {
      case 'succeeded': return 'good';
      case 'failed':
      case 'interrupted': return 'bad';
      case 'cancelled': return 'unknown';
      case 'running':
      case 'pending': return 'warn';
      default: return 'unknown';
    }
  }

  // Both pending and running ops can be canceled: pending ones flip
  // immediately; running ones request cooperative cancellation (the op stops
  // once its provider yields, then transitions to canceled via Watch).
  function isCancelable(s: string): boolean {
    return s === 'pending' || s === 'running';
  }

  function isReapplicable(s: string): boolean {
    return s === 'failed' || s === 'interrupted' || s === 'cancelled';
  }

  async function doCancel(id: string) {
    cancelling = { ...cancelling, [id]: true };
    cancelError = { ...cancelError, [id]: '' };
    try {
      await opsApi.cancel(id);
      // Watcher will surface the new status on the next stream tick; no
      // optimistic local mutation needed.
    } catch (err) {
      cancelError = { ...cancelError, [id]: err instanceof Error ? err.message : String(err) };
    } finally {
      cancelling = { ...cancelling, [id]: false };
    }
  }

  // Retry uses sessionStorage as a handoff between drawer and editor —
  // the editor reads the marker on mount, calls operations.get(id) to
  // fetch the original manifest, and pre-fills `text` so the user
  // re-submits exactly what failed instead of the last-successful
  // applied state.
  const RETRY_KEY = 'openctl.retryFromOp';
  function doRetry(op: OperationRow) {
    if (!op.apiVersion || !op.kind || !op.resourceName) return;
    try { sessionStorage.setItem(RETRY_KEY, op.id); } catch { /* private mode */ }
    navigate({ name: 'edit', apiVersion: op.apiVersion, kind: op.kind, resourceName: op.resourceName });
  }

  // Recent activity badge: count of ops still in flight.
  $: inflight = $ops.filter((o) => o.status === 'pending' || o.status === 'running').length;

  // Apply the U7 filter set against the in-memory ops list.
  $: visible = $ops.filter((o) => {
    if (filterStatus && o.status !== filterStatus) return false;
    if (filterSource && (o.source || '') !== filterSource) return false;
    if (filterText) {
      const t = filterText.toLowerCase();
      if (!(`${o.kind ?? ''}/${o.resourceName ?? ''}`.toLowerCase().includes(t))) return false;
    }
    return true;
  });
</script>

<aside class="drawer" class:open>
  <button class="handle" on:click={toggle} aria-expanded={open}>
    <span class="caret">{open ? '▾' : '▴'}</span>
    Operations
    {#if inflight > 0}
      <span class="inflight">{inflight} in flight</span>
    {:else if $ops.length > 0}
      <span class="muted">{$ops.length} recent</span>
    {/if}
    {#if $opsError}
      <span class="err-pill" title={$opsError}>stream error</span>
    {/if}
  </button>

  {#if open}
    <div class="body">
      <div class="filters">
        <label>
          <span class="muted small">Status</span>
          <select bind:value={filterStatus}>
            <option value="">all</option>
            <option value="pending">pending</option>
            <option value="running">running</option>
            <option value="succeeded">succeeded</option>
            <option value="failed">failed</option>
            <option value="interrupted">interrupted</option>
            <option value="cancelled">cancelled</option>
          </select>
        </label>
        <label>
          <span class="muted small">Source</span>
          <select bind:value={filterSource}>
            <option value="">all</option>
            <option value="cli">CLI</option>
            <option value="ui">UI</option>
          </select>
        </label>
        <label class="grow">
          <span class="muted small">Search</span>
          <input type="search" placeholder="kind/name" bind:value={filterText} />
        </label>
        {#if filterStatus || filterSource || filterText}
          <button class="clear-filters" on:click={() => { filterStatus=''; filterSource=''; filterText=''; }}>Clear</button>
        {/if}
      </div>

      {#if $ops.length === 0}
        <p class="muted">No operations yet. Apply a resource via CLI or UI to see one here.</p>
      {:else if visible.length === 0}
        <p class="muted">No operations match the current filters.</p>
      {:else}
        <table>
          <thead>
            <tr>
              <th></th>
              <th>When</th>
              <th>Type</th>
              <th>Resource</th>
              <th>Source</th>
              <th>Status</th>
              <th>Detail</th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {#each visible as op (op.id)}
              {@const hasChildren = (op.children?.length ?? 0) > 0}
              <tr>
                <td class="expand-cell">
                  {#if hasChildren}
                    <button class="expand-btn" on:click={() => toggleRow(op.id)} aria-expanded={!!expanded[op.id]}>
                      {expanded[op.id] ? '▾' : '▸'}
                    </button>
                  {/if}
                </td>
                <td class="when">{timeAgo(op.completedAt || op.startedAt || op.submittedAt)}</td>
                <td>
                  {op.type}{op.label ? `: ${op.label}` : ''}
                </td>
                <td>
                  {#if op.apiVersion && op.kind && op.resourceName}
                    <a href={routeHref({
                      name: 'detail',
                      apiVersion: op.apiVersion,
                      kind: op.kind,
                      resourceName: op.resourceName,
                    })}>{op.kind}/{op.resourceName}</a>
                  {:else}
                    <span class="muted">—</span>
                  {/if}
                </td>
                <td class="source">{op.source || '—'}</td>
                <td>
                  <span class="status status-{statusTone(op.status)}">{op.status}</span>
                </td>
                <td class="detail">
                  {#if op.error}
                    <span class="err" title={op.error}>{op.error}</span>
                  {:else if hasChildren}
                    <span class="muted small">{op.children!.length} step{op.children!.length === 1 ? '' : 's'}</span>
                  {:else}
                    <span class="muted small mono">{op.id.slice(0, 12)}</span>
                  {/if}
                </td>
                <td class="actions-cell">
                  {#if isCancelable(op.status)}
                    <button class="row-btn cancel" on:click={() => doCancel(op.id)} disabled={cancelling[op.id]}>
                      {cancelling[op.id] ? 'cancelling…' : 'cancel'}
                    </button>
                    {#if cancelError[op.id]}
                      <span class="err small" title={cancelError[op.id]}>!</span>
                    {/if}
                  {:else if isReapplicable(op.status) && op.apiVersion && op.kind && op.resourceName}
                    <button class="row-btn retry" on:click={() => doRetry(op)}>retry</button>
                  {/if}
                </td>
              </tr>
              {#if expanded[op.id] && hasChildren}
                <tr class="children-row">
                  <td></td>
                  <td colspan="7">
                    <ul class="children-list">
                      {#each op.children ?? [] as ch (ch.id)}
                        <li>
                          <span class="status status-{statusTone(ch.status)} child-status">{ch.status}</span>
                          <span class="mono small">{ch.type}{ch.label ? `: ${ch.label}` : ''}</span>
                          {#if ch.kind && ch.resourceName}
                            <a class="muted small" href={routeHref({
                              name: 'detail',
                              apiVersion: ch.apiVersion!,
                              kind: ch.kind,
                              resourceName: ch.resourceName,
                            })}>{ch.kind}/{ch.resourceName}</a>
                          {/if}
                          {#if ch.error}<span class="err small" title={ch.error}>error</span>{/if}
                        </li>
                      {/each}
                    </ul>
                  </td>
                </tr>
              {/if}
            {/each}
          </tbody>
        </table>
      {/if}
    </div>
  {/if}
</aside>

<style>
  .drawer {
    position: fixed;
    left: 0;
    right: 0;
    bottom: 0;
    background: #1e1e1e;
    border-top: 1px solid #2a2a2a;
    box-shadow: 0 -2px 16px rgba(0, 0, 0, 0.3);
    z-index: 20;
    display: flex;
    flex-direction: column;
    max-height: 50vh;
  }
  @media (prefers-color-scheme: light) {
    .drawer {
      background: #fff;
      border-top-color: #e6e6e6;
      box-shadow: 0 -2px 16px rgba(0, 0, 0, 0.06);
    }
  }
  .handle {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    width: 100%;
    background: transparent;
    border: none;
    border-radius: 0;
    color: inherit;
    padding: 0.5rem 1rem;
    cursor: pointer;
    font-size: 0.875rem;
    text-align: left;
  }
  .handle:hover {
    background: rgba(127, 127, 127, 0.08);
    border-color: transparent;
  }
  .caret {
    color: #777;
    font-size: 0.75rem;
  }
  .inflight {
    color: #ffce4d;
    font-weight: 500;
    font-size: 0.8rem;
  }
  .muted {
    color: #888;
  }
  .small {
    font-size: 0.8em;
  }
  .err-pill {
    margin-left: auto;
    background: rgba(248, 81, 73, 0.18);
    color: #ff8980;
    padding: 0.05em 0.6em;
    border-radius: 999px;
    font-size: 0.75rem;
  }
  .body {
    overflow-y: auto;
    padding: 0 1rem 1rem;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.85rem;
  }
  th, td {
    text-align: left;
    padding: 0.4rem 0.6rem;
    border-bottom: 1px solid rgba(127, 127, 127, 0.15);
  }
  th {
    font-size: 0.7rem;
    color: #888;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    background: rgba(0, 0, 0, 0.15);
    position: sticky;
    top: 0;
  }
  @media (prefers-color-scheme: light) {
    th { background: #f4f4f4; }
  }
  .when {
    font-variant-numeric: tabular-nums;
    color: #888;
    width: 4rem;
  }
  .detail {
    max-width: 30rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .mono {
    font-family: ui-monospace, SFMono-Regular, monospace;
  }
  td a {
    color: #6ea8ff;
    text-decoration: none;
  }
  td a:hover {
    text-decoration: underline;
  }
  .status {
    display: inline-block;
    padding: 0.05em 0.6em;
    border-radius: 999px;
    font-size: 0.75rem;
    font-weight: 500;
  }
  .status-good { background: rgba(46, 160, 67, 0.18); color: #5fdb78; }
  .status-warn { background: rgba(255, 184, 0, 0.18); color: #ffce4d; }
  .status-bad  { background: rgba(248, 81, 73, 0.18); color: #ff8980; }
  .status-unknown { background: rgba(127, 127, 127, 0.18); color: #aaa; }
  .err {
    color: #ff8980;
  }
  .filters {
    display: flex;
    gap: 0.75rem;
    align-items: end;
    padding: 0.6rem 0 0.75rem;
    border-bottom: 1px solid rgba(127, 127, 127, 0.15);
    margin-bottom: 0.5rem;
    flex-wrap: wrap;
  }
  .filters label {
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
    font-size: 0.85rem;
  }
  .filters .grow {
    flex: 1 1 12rem;
  }
  .filters select, .filters input {
    background: rgba(0, 0, 0, 0.18);
    color: inherit;
    border: 1px solid rgba(127, 127, 127, 0.3);
    border-radius: 4px;
    padding: 0.25em 0.5em;
    font-size: 0.85rem;
  }
  @media (prefers-color-scheme: light) {
    .filters select, .filters input {
      background: #fff;
    }
  }
  .clear-filters {
    align-self: end;
    padding: 0.3em 0.7em;
    font-size: 0.8rem;
    background: transparent;
    border: 1px solid rgba(127, 127, 127, 0.35);
    color: inherit;
    border-radius: 4px;
    cursor: pointer;
  }
  .expand-cell { width: 1.5rem; }
  .expand-btn {
    background: transparent;
    border: none;
    color: #888;
    cursor: pointer;
    padding: 0;
    font-size: 0.85rem;
    line-height: 1;
  }
  .expand-btn:hover { color: inherit; }
  .source {
    text-transform: uppercase;
    font-size: 0.7rem;
    color: #888;
    letter-spacing: 0.04em;
  }
  .actions-cell {
    white-space: nowrap;
  }
  .row-btn {
    padding: 0.15em 0.7em;
    font-size: 0.75rem;
    border-radius: 4px;
    cursor: pointer;
    border: 1px solid transparent;
    background: transparent;
    color: inherit;
  }
  .row-btn:hover:not(:disabled) {
    background: rgba(127, 127, 127, 0.12);
  }
  .row-btn:disabled {
    opacity: 0.6;
    cursor: wait;
  }
  .row-btn.cancel {
    border-color: rgba(255, 184, 0, 0.35);
    color: #ffce4d;
  }
  .row-btn.retry {
    border-color: rgba(110, 168, 255, 0.35);
    color: #6ea8ff;
  }
  .children-row td {
    border-bottom: none;
    padding-top: 0;
  }
  .children-list {
    list-style: none;
    padding: 0 0 0 1rem;
    margin: 0 0 0.5rem;
    border-left: 2px solid rgba(127, 127, 127, 0.2);
  }
  .children-list li {
    padding: 0.25rem 0;
    display: flex;
    align-items: center;
    gap: 0.6rem;
  }
  .child-status {
    flex-shrink: 0;
    min-width: 4.5rem;
    text-align: center;
  }
</style>
