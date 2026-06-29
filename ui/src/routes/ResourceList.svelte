<script lang="ts">
  import { resources, type Resource } from '../lib/api';
  import { statusBadge } from '../lib/format';
  import { routeHref } from '../lib/router';

  export let apiVersion: string;
  export let kind: string;
  export let provider: string;

  let rows: Resource[] = [];
  let loading = true;
  let error = '';

  // Re-fetch whenever the user navigates to a different kind. Reactive
  // re-run: when apiVersion/kind change, run load().
  $: void load(apiVersion, kind);

  async function load(av: string, k: string) {
    loading = true;
    error = '';
    try {
      const resp = await resources.list(av, k);
      rows = resp.resources ?? [];
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
      rows = [];
    } finally {
      loading = false;
    }
  }
</script>

<section>
  <header>
    <div>
      <h2>{kind}</h2>
      <p class="path">{provider} · {apiVersion}</p>
    </div>
    <span class="badge-count">{rows.length}</span>
  </header>

  {#if loading}
    <p class="muted">Loading…</p>
  {:else if error}
    <p class="err">{error}</p>
  {:else if rows.length === 0}
    <p class="muted">No resources of this kind. Apply one via CLI:
      <code>openctl ctl apply -f manifest.yaml</code></p>
  {:else}
    <table>
      <thead>
        <tr>
          <th>Name</th>
          <th>State</th>
          <th>Drift</th>
        </tr>
      </thead>
      <tbody>
        {#each rows as r (r.metadata.name)}
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
