<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import type { WhoAmIResponse } from '../lib/api';
  import { logout } from '../lib/auth';
  import { route } from '../lib/router';
  import {
    catalogue, catalogueError, refreshCatalogue, stopCatalogue,
    type KindEntry,
  } from '../lib/catalogue';
  import { routeHref } from '../lib/router';
  import { startOpsWatcher, stopOpsWatcher } from '../lib/ops';
  import GitStatus from '../components/GitStatus.svelte';
  import Nav from '../components/Nav.svelte';
  import OpsDrawer from '../components/OpsDrawer.svelte';
  import HomePane from './HomePane.svelte';
  import ResourceList from './ResourceList.svelte';
  import Detail from './Detail.svelte';
  import Edit from './Edit.svelte';

  export let me: WhoAmIResponse;

  let busy = false;

  onMount(() => {
    void refreshCatalogue();
    startOpsWatcher();
  });

  onDestroy(() => {
    stopOpsWatcher();
    stopCatalogue();
  });

  async function doLogout() {
    busy = true;
    try {
      await logout();
    } finally {
      busy = false;
    }
  }

  // Pre-flatten catalogue lookups so the main pane can find the active
  // kind without re-grouping. The store value is reactive; this just
  // turns it into a Map for O(1) access.
  $: byKey = new Map<string, KindEntry>($catalogue.flat.map((e) => [e.key, e]));

  $: activeKindKey =
    $route.name === 'list' || $route.name === 'detail'
      || $route.name === 'create' || $route.name === 'edit'
      ? `${$route.apiVersion}/${$route.kind}`
      : '';
</script>

<header>
  <a class="brand" href={routeHref({ name: 'home' })}>openctl</a>
  <div class="meta">
    <GitStatus />
    <span class="who" title="Session: {me.sessionId}">
      signed in {me.userId ? `as ${me.userId}` : ''}
    </span>
    <button on:click={doLogout} disabled={busy}>
      {busy ? 'Signing out…' : 'Sign out'}
    </button>
  </div>
</header>

<div class="shell">
  <aside>
    {#if $catalogueError}
      <p class="err">{$catalogueError}</p>
    {/if}
    <Nav catalogue={$catalogue} activeKindKey={activeKindKey} />
  </aside>

  <main>
    {#if $route.name === 'home'}
      <HomePane />
    {:else if $route.name === 'list'}
      {@const entry = byKey.get(`${$route.apiVersion}/${$route.kind}`)}
      <ResourceList
        apiVersion={$route.apiVersion}
        kind={$route.kind}
        provider={entry?.provider ?? ''}
      />
    {:else if $route.name === 'detail'}
      <Detail
        apiVersion={$route.apiVersion}
        kind={$route.kind}
        resourceName={$route.resourceName}
      />
    {:else if $route.name === 'create'}
      <Edit
        apiVersion={$route.apiVersion}
        kind={$route.kind}
      />
    {:else}
      <Edit
        apiVersion={$route.apiVersion}
        kind={$route.kind}
        resourceName={$route.resourceName}
      />
    {/if}
  </main>
</div>

<OpsDrawer />

<style>
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.5rem 1.25rem;
    border-bottom: 1px solid #2a2a2a;
    height: 3rem;
    box-sizing: border-box;
  }
  .brand {
    font-weight: 600;
    color: inherit;
    text-decoration: none;
    font-size: 1rem;
  }
  .meta {
    display: flex;
    align-items: center;
    gap: 1rem;
  }
  .who {
    color: #888;
    font-size: 0.875rem;
  }
  .shell {
    display: grid;
    grid-template-columns: 18rem 1fr;
    min-height: calc(100vh - 3rem);
  }
  aside {
    border-right: 1px solid #2a2a2a;
    padding: 1rem 0.75rem;
    overflow-y: auto;
  }
  main {
    padding: 1.5rem 2rem;
    /* Reserve room for the fixed-bottom OpsDrawer handle (collapsed
       height) so it doesn't cover the last row of content. */
    padding-bottom: 4rem;
    overflow-x: auto;
  }
  .err {
    color: #f57171;
    font-size: 0.875rem;
    margin: 0 0 1rem;
  }
  @media (prefers-color-scheme: light) {
    header,
    aside {
      border-color: #e6e6e6;
    }
  }
</style>
