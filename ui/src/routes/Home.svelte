<script lang="ts">
  import { onMount } from 'svelte';
  import { ping, type PingResponse, type WhoAmIResponse } from '../lib/api';
  import { logout } from '../lib/auth';

  export let me: WhoAmIResponse;

  let pong: PingResponse | null = null;
  let busy = false;
  let error = '';

  onMount(async () => {
    try {
      pong = await ping.ping();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  });

  async function doLogout() {
    busy = true;
    try {
      await logout();
    } finally {
      busy = false;
    }
  }
</script>

<header>
  <h1>openctl</h1>
  <div class="meta">
    <span class="who" title="Session id: {me.sessionId}">
      signed in {me.userId ? `as ${me.userId}` : ''}
    </span>
    <button on:click={doLogout} disabled={busy}>
      {busy ? 'Signing out…' : 'Sign out'}
    </button>
  </div>
</header>

<main>
  <p class="placeholder">
    UI Phase U3.1 is up. List, detail, and ops views land in U3.2–U3.4.
  </p>
  <section>
    <h2>Controller</h2>
    {#if error}
      <p class="err">Ping failed: {error}</p>
    {:else if !pong}
      <p>Pinging controller…</p>
    {:else}
      <dl>
        <dt>Version</dt><dd>{pong.serverVersion}</dd>
        <dt>Echo</dt><dd>{pong.echo}</dd>
      </dl>
    {/if}
  </section>
</main>

<style>
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.75rem 1.5rem;
    border-bottom: 1px solid #2a2a2a;
  }
  h1 {
    font-size: 1.1rem;
    margin: 0;
  }
  .meta {
    display: flex;
    align-items: center;
    gap: 1rem;
  }
  .who {
    color: #888;
    font-size: 0.9rem;
  }
  main {
    padding: 2rem;
    max-width: 60rem;
    margin: 0 auto;
  }
  .placeholder {
    color: #888;
    margin-bottom: 2rem;
  }
  section {
    background: #232323;
    border-radius: 8px;
    padding: 1.25rem 1.5rem;
  }
  @media (prefers-color-scheme: light) {
    header {
      border-bottom-color: #e6e6e6;
    }
    section {
      background: #fff;
      box-shadow: 0 1px 4px rgba(0, 0, 0, 0.04);
    }
  }
  h2 {
    margin: 0 0 0.75rem;
    font-size: 1rem;
    color: #aaa;
  }
  dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 0.5rem 1.5rem;
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
