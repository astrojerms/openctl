<script lang="ts">
  import { ops, opsError } from '../lib/ops';
  import { routeHref } from '../lib/router';

  // Persisted across re-renders inside the shell. Collapsed by default
  // so it doesn't eat screen real estate on first visit.
  let open = false;

  function toggle() {
    open = !open;
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
      case 'running':
      case 'pending': return 'warn';
      default: return 'unknown';
    }
  }

  // Recent activity badge: count of ops still in flight.
  $: inflight = $ops.filter((o) => o.status === 'pending' || o.status === 'running').length;
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
      {#if $ops.length === 0}
        <p class="muted">No operations yet. Apply a resource via CLI or UI to see one here.</p>
      {:else}
        <table>
          <thead>
            <tr>
              <th>When</th>
              <th>Type</th>
              <th>Resource</th>
              <th>Status</th>
              <th>Detail</th>
            </tr>
          </thead>
          <tbody>
            {#each $ops as op (op.id)}
              <tr>
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
                <td>
                  <span class="status status-{statusTone(op.status)}">{op.status}</span>
                </td>
                <td class="detail">
                  {#if op.error}
                    <span class="err" title={op.error}>{op.error}</span>
                  {:else}
                    <span class="muted small mono">{op.id.slice(0, 12)}</span>
                  {/if}
                </td>
              </tr>
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
</style>
