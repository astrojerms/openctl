<script lang="ts">
  import { onMount } from 'svelte';
  import { ping, type PingResponse } from '../lib/api';
  import { catalogue } from '../lib/catalogue';

  let pong: PingResponse | null = null;
  let error = '';

  onMount(async () => {
    try {
      pong = await ping.ping();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  });

  $: totalKinds = $catalogue.flat.length;
  $: totalResources = $catalogue.flat.reduce(
    (sum, e) => sum + (e.count ?? 0),
    0,
  );
</script>

<section>
  <h2>Welcome</h2>
  <p class="lede">
    Pick a kind in the sidebar to drill in. Detail and ops views ship in
    U3.3 and U3.4.
  </p>

  <div class="grid">
    <div class="card">
      <h3>Controller</h3>
      {#if error}
        <p class="err">{error}</p>
      {:else if !pong}
        <p>Pinging…</p>
      {:else}
        <dl>
          <dt>Version</dt><dd>{pong.serverVersion}</dd>
          <dt>Echo</dt><dd>{pong.echo}</dd>
        </dl>
      {/if}
    </div>

    <div class="card">
      <h3>Catalogue</h3>
      <dl>
        <dt>Kinds</dt><dd>{totalKinds}</dd>
        <dt>Resources</dt><dd>{totalResources}</dd>
      </dl>
    </div>
  </div>
</section>

<style>
  section {
    max-width: 60rem;
  }
  h2 {
    margin: 0 0 0.25rem;
    font-size: 1.25rem;
  }
  .lede {
    color: #888;
    margin: 0 0 2rem;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(16rem, 1fr));
    gap: 1rem;
  }
  .card {
    background: #232323;
    border-radius: 8px;
    padding: 1.25rem 1.5rem;
  }
  @media (prefers-color-scheme: light) {
    .card {
      background: #fff;
      box-shadow: 0 1px 4px rgba(0, 0, 0, 0.04);
    }
  }
  h3 {
    margin: 0 0 0.75rem;
    font-size: 0.95rem;
    color: #aaa;
  }
  dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.4rem 1.5rem;
    margin: 0;
  }
  dt {
    color: #888;
  }
  dd {
    margin: 0;
    font-family: ui-monospace, SFMono-Regular, monospace;
    font-size: 0.9rem;
  }
  .err {
    color: #f57171;
  }
</style>
