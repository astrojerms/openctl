<script lang="ts">
  import { onDestroy } from 'svelte';
  import { resources, UnauthorizedError, type GetResourceResponse } from '../lib/api';
  import { watchResources } from '../lib/watch';
  import { statusBadge } from '../lib/format';
  import { routeHref } from '../lib/router';

  export let apiVersion: string;
  export let kind: string;
  export let resourceName: string;

  let data: GetResourceResponse | null = null;
  let loading = true;
  let error = '';

  let controller: AbortController | null = null;
  let watching = '';

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
    await load(av, k, n);
    void subscribe(av, k, n, controller.signal);
  }

  async function load(av: string, k: string, n: string) {
    try {
      data = await resources.get(av, k, n);
      error = '';
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      loading = false;
    }
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
    {#if state}
      <span class="state state-{state.tone}">{state.label}</span>
    {/if}
  </header>

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

    <article class="card">
      <h3>Owner / children</h3>
      <p class="muted">
        Owner-ref and composite children tree ship with a later U3.x
        followup — the proto surface needs extension first.
      </p>
    </article>
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
</style>
