<script lang="ts">
  import type { PingResponse } from '../lib/api';
  import { catalogue } from '../lib/catalogue';
  import { manifestAuthority } from '../lib/authority';
  import { overview } from '../lib/overview';
  import { focusOpsDrawer } from '../lib/opsDrawer';
  import { routeHref } from '../lib/router';

  export let pong: PingResponse | null = null;
  export let buildError = '';
  export let viewer = false;

  $: snapshot = $overview.snapshot;
  $: operationGroups = snapshot ? [
    {
      title: 'Active root requests',
      total: snapshot.pendingOperations + snapshot.runningOperations,
      rows: snapshot.activeOperations,
      description: 'Pending and running root requests, not child steps or live readiness checks.',
    },
    {
      title: 'Latest unsuccessful requests needing review',
      total: snapshot.reviewResources,
      rows: snapshot.reviewOperations,
      description: 'Resources whose latest retained root Apply/Delete request failed, was interrupted, or was canceled. A newer request supersedes an older failure; this is history, not current resource health.',
    },
  ] : [];
  $: inventory = snapshot?.kinds ?? [];
  $: byKey = new Map($catalogue.flat.map((entry) => [entry.key, entry]));
</script>

<section>
  <h2>Control-plane overview</h2>
  <p class="lede">
    Saved managed inventory and retained request history. Read-only; not live health, readiness, or a provider scan.
  </p>

  {#if $overview.error}
    <p class="snapshot-warning" role="alert">
      {snapshot ? 'Stale snapshot — refresh failed. All values below are from the last successful snapshot.' : 'Overview unavailable — counts are unknown.'}
      {$overview.error}
    </p>
  {:else if !snapshot}
    <p role="status">Loading persisted overview… Counts are unknown until the controller responds.</p>
  {/if}

  {#if snapshot}
    <p class="snapshot-time">
      { $overview.error ? 'Last successful snapshot' : 'Snapshot' }:
      <time datetime={snapshot.asOf}>{snapshot.asOf}</time>
    </p>
    <div class="grid metrics">
      <div class="card">
        <h3>Saved managed resources</h3>
        <p class="metric">{snapshot.managedResources}</p>
        <p class="muted">Desired records, including owned children. Discovery-only observations are excluded.</p>
      </div>
      <div class="card">
        <h3>Pending root requests</h3>
        <p class="metric">{snapshot.pendingOperations}</p>
      </div>
      <div class="card">
        <h3>Running root requests</h3>
        <p class="metric">{snapshot.runningOperations}</p>
      </div>
      <div class="card">
        <h3>Resources needing request review</h3>
        <p class="metric">{snapshot.reviewResources}</p>
        <p class="muted">Latest retained unsuccessful Apply/Delete admission per resource.</p>
      </div>
    </div>

    <div class="card inventory">
      <h3>Saved inventory by kind</h3>
      {#if inventory.length === 0}
        <p>No saved managed resources in this snapshot.</p>
      {:else}
        <ul>
          {#each inventory as kind (`${kind.apiVersion}/${kind.kind}`)}
            {@const entry = byKey.get(`${kind.apiVersion}/${kind.kind}`)}
            <li>
              <a href={routeHref({ name: 'list', apiVersion: kind.apiVersion, kind: kind.kind })}>{kind.kind}</a>
              <span class="muted">{kind.apiVersion}</span>
              {#if !entry?.schemaAvailable}<span class="muted">Schema unavailable; provider status not checked</span>{/if}
              <strong>{kind.resources}</strong>
            </li>
          {/each}
        </ul>
      {/if}
    </div>

    {#each operationGroups as group}
      <div class="card requests">
        <h3>{group.title}</h3>
        <p class="muted">{group.description}</p>
        <p class="displayed">Showing {group.rows.length} of {group.total}{group.rows.length < group.total ? ' — list capped; totals are exact for this snapshot.' : '.'}</p>
        {#if group.rows.length === 0}
          <p>No matching requests in this snapshot.</p>
        {:else}
          <div class="table-scroll">
            <table>
              <thead><tr><th>Resource</th><th>Request</th><th>Status</th><th>Submitted</th><th></th></tr></thead>
              <tbody>
                {#each group.rows as op (op.id)}
                  <tr>
                    <td>
                      <span>{op.kind && op.resourceName ? `${op.kind}/${op.resourceName}` : 'Unknown resource'}</span>
                      {#if op.apiVersion}<small>{op.apiVersion}</small>{/if}
                    </td>
                    <td>{op.type}<small title={op.id}>{op.id.slice(0, 12)}</small></td>
                    <td>{op.status}</td>
                    <td><time datetime={op.submittedAt}>{op.submittedAt || 'Unknown'}</time></td>
                    <td><button type="button" on:click={() => focusOpsDrawer({ opId: op.id })} aria-label={`Inspect operation ${op.id}`}>Inspect</button></td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </div>
    {/each}
  {/if}

  <div class="grid onboarding">
    <div class="card">
      <h3>Get started</h3>
      {#if viewer}<p class="muted">Viewer session — read-only access. Deployment and adoption require an editor or admin.</p>{/if}
      {#if $manifestAuthority.mode === 'gitops'}
        <p class="muted">Running authority: GitOps owns desired intent; edit the upstream repository to apply or delete.</p>
      {:else if $manifestAuthority.mode === 'mirror'}
        <p class="muted">Running authority: the controller owns desired intent; changes remain subject to your role.</p>
      {:else}
        <p class="muted">Running manifest authority is unknown. Desired changes stay blocked until it is verified.</p>
      {/if}
      <p><a href={routeHref({ name: 'providers' })}>Connect a provider</a></p>
      <p><a href={routeHref({ name: 'discover' })}>Discover and adopt existing VMs</a></p>
      <p><a href={routeHref({ name: 'jellyfin' })}>Deploy Jellyfin</a></p>
    </div>
    <div class="card">
      <h3>Controller</h3>
      {#if buildError}
        <p class="err">Build information unavailable: {buildError}</p>
      {:else if !pong}
        <p>Loading build information…</p>
      {:else}
        <dl>
          <dt>Version</dt><dd>{pong.serverVersion}</dd>
          {#if pong.gitCommit}<dt>Commit</dt><dd>{pong.gitCommit}</dd>{/if}
          {#if pong.buildTime && pong.buildTime !== 'dev'}<dt>Built</dt><dd>{pong.buildTime}</dd>{/if}
          {#if pong.controllerId}<dt>Controller</dt><dd>{pong.controllerId}</dd>{/if}
        </dl>
      {/if}
    </div>
  </div>
</section>

<style>
  section { max-width: 76rem; }
  h2 { margin: 0 0 0.25rem; font-size: 1.25rem; }
  h3 { margin: 0 0 0.75rem; font-size: 0.95rem; }
  p { line-height: 1.5; }
  .lede, .muted, .snapshot-time, small { color: #888; }
  .lede { margin: 0 0 1rem; }
  .snapshot-time { font-size: 0.85rem; overflow-wrap: anywhere; }
  .snapshot-warning { padding: 0.8rem 1rem; background: rgba(200, 162, 74, 0.15); border-left: 3px solid #c8a24a; }
  .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr)); gap: 1rem; }
  .card { background: #232323; border-radius: 8px; padding: 1.25rem 1.5rem; }
  .metric { font-size: 2rem; font-variant-numeric: tabular-nums; margin: 0; }
  .inventory, .requests, .onboarding { margin-top: 1rem; }
  .inventory ul { list-style: none; padding: 0; margin: 0; }
  .inventory li { display: flex; align-items: baseline; flex-wrap: wrap; gap: 0.5rem 1rem; padding: 0.5rem 0; }
  .inventory strong { margin-left: auto; font-variant-numeric: tabular-nums; }
  .displayed { font-size: 0.85rem; }
  .table-scroll { overflow-x: auto; }
  table { width: 100%; border-collapse: collapse; font-size: 0.85rem; text-align: left; }
  th, td { padding: 0.6rem; border-bottom: 1px solid rgba(127, 127, 127, 0.2); }
  th { color: #888; font-weight: 500; }
  small { display: block; margin-top: 0.25rem; }
  button { cursor: pointer; border: 1px solid #666; background: transparent; color: inherit; padding: 0.3rem 0.6rem; border-radius: 4px; }
  dl { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 0.4rem 1rem; margin: 0; }
  dt { color: #888; }
  dd { margin: 0; font-family: ui-monospace, SFMono-Regular, monospace; font-size: 0.9rem; overflow-wrap: anywhere; }
  .err { color: #f57171; }
  a { color: #6ea8ff; }
  @media (prefers-color-scheme: light) {
    .card { background: #fff; box-shadow: 0 1px 4px rgba(0, 0, 0, 0.04); }
  }
</style>
