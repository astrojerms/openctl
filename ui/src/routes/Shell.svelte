<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import type { PingResponse, WhoAmIResponse } from '../lib/api';
  import { ping } from '../lib/api';
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
  import Templates from './Templates.svelte';
  import TemplateWizard from './TemplateWizard.svelte';
  import Providers from './Providers.svelte';
  import Settings from './Settings.svelte';

  export let me: WhoAmIResponse;

  let busy = false;

  // On mobile the sidebar is an off-canvas drawer (see the styles below).
  // It's hidden by default and toggled by the hamburger in the header.
  let sidebarOpen = false;

  // Close the drawer whenever the route changes, so tapping a nav link on a
  // phone navigates AND dismisses the menu. No-op on desktop where the
  // sidebar is always visible regardless of this flag.
  $: if ($route) sidebarOpen = false;

  // Version pill in the header. Fetched once on mount so the user can
  // see at a glance which controller build (git SHA + build time) is
  // running — sidesteps "is my UI change even deployed?" without
  // digging into the Home tab.
  let pong: PingResponse | null = null;

  onMount(() => {
    void refreshCatalogue();
    startOpsWatcher();
    void ping.ping().then((p) => (pong = p)).catch(() => { /* ignore */ });
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

<svelte:window on:keydown={(e) => { if (e.key === 'Escape') sidebarOpen = false; }} />

<header>
  <button
    class="menu-toggle"
    aria-label="Toggle navigation menu"
    aria-expanded={sidebarOpen}
    on:click={() => (sidebarOpen = !sidebarOpen)}
  >
    <svg width="20" height="20" viewBox="0 0 20 20" aria-hidden="true">
      <path d="M2 5h16M2 10h16M2 15h16" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
    </svg>
  </button>
  <a class="brand" href={routeHref({ name: 'home' })}>openctl</a>
  {#if pong?.gitCommit}
    <span class="build-pill" title={pong.buildTime && pong.buildTime !== 'dev' ? `built ${pong.buildTime}` : 'commit / build info'}>
      {pong.gitCommit}
    </span>
  {/if}
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
  <!-- Backdrop dims the content behind the off-canvas drawer on mobile and
       closes it on tap. A button (not a div) so it's keyboard-focusable.
       Hidden on desktop via CSS regardless of sidebarOpen. -->
  {#if sidebarOpen}
    <button
      class="backdrop"
      aria-label="Close navigation menu"
      on:click={() => (sidebarOpen = false)}
    ></button>
  {/if}
  <aside class:open={sidebarOpen}>
    <a
      class="sidebar-link"
      class:active={$route.name === 'templates' || $route.name === 'template'}
      href={routeHref({ name: 'templates' })}
    >Templates</a>
    <a
      class="sidebar-link"
      class:active={$route.name === 'providers'}
      href={routeHref({ name: 'providers' })}
    >Providers</a>
    <a
      class="sidebar-link"
      class:active={$route.name === 'settings'}
      href={routeHref({ name: 'settings' })}
    >Settings</a>
    {#if $catalogueError}
      <p class="err">{$catalogueError}</p>
    {/if}
    <Nav catalogue={$catalogue} activeKindKey={activeKindKey} />
  </aside>

  <main>
    {#if $route.name === 'home'}
      <HomePane />
    {:else if $route.name === 'templates'}
      <Templates />
    {:else if $route.name === 'template'}
      <TemplateWizard template={$route.template} />
    {:else if $route.name === 'providers'}
      <Providers />
    {:else if $route.name === 'settings'}
      <Settings />
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
    gap: 0.6rem;
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
  /* Hamburger is desktop-hidden; the media query below reveals it. */
  .menu-toggle {
    display: none;
    padding: 0.3rem;
    line-height: 0;
    background: transparent;
    border: 1px solid transparent;
    border-radius: 6px;
    color: inherit;
  }
  .backdrop {
    display: none;
  }
  .build-pill {
    margin-left: 0.6rem;
    padding: 0.05em 0.55em;
    border-radius: 999px;
    background: rgba(127, 127, 127, 0.12);
    color: #888;
    font-family: ui-monospace, SFMono-Regular, monospace;
    font-size: 0.72rem;
    letter-spacing: 0.02em;
  }
  .meta {
    display: flex;
    align-items: center;
    gap: 1rem;
    /* Pushes the meta group to the far right of the gap-based header row. */
    margin-left: auto;
  }
  .who {
    color: #888;
    font-size: 0.875rem;
  }
  .shell {
    display: grid;
    grid-template-columns: var(--sidebar-width) 1fr;
    min-height: calc(100vh - 3rem);
  }
  aside {
    border-right: 1px solid #2a2a2a;
    padding: 1rem 0.75rem;
    overflow-y: auto;
  }
  .sidebar-link {
    display: block;
    padding: 0.4rem 0.75rem;
    margin-bottom: 0.75rem;
    color: #ccc;
    text-decoration: none;
    font-size: 0.9rem;
    font-weight: 500;
    border-radius: 4px;
    border: 1px solid rgba(127, 127, 127, 0.2);
  }
  .sidebar-link:hover {
    background: rgba(127, 127, 127, 0.08);
    color: #fff;
  }
  .sidebar-link.active {
    background: rgba(74, 142, 240, 0.15);
    color: #6ea8ff;
    border-color: #4a8ef0;
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

  /* --- Mobile: collapse the sidebar into an off-canvas drawer.
     Keep this in sync with --bp-mobile (48rem) in app.css. --- */
  @media (max-width: 48rem) {
    .menu-toggle {
      display: inline-flex;
      align-items: center;
    }
    header {
      padding: 0.5rem 0.9rem;
    }
    /* Drop the session line on narrow screens; the Sign out button and
       the Home tab still convey identity. */
    .who {
      display: none;
    }
    .meta {
      gap: 0.6rem;
    }

    .shell {
      grid-template-columns: 1fr;
    }
    aside {
      position: fixed;
      top: 3rem;
      bottom: 0;
      left: 0;
      width: min(var(--sidebar-width), 82vw);
      box-sizing: border-box;
      background: #1a1a1a;
      z-index: 30;
      transform: translateX(-100%);
      transition: transform 180ms ease;
      will-change: transform;
    }
    aside.open {
      transform: translateX(0);
      box-shadow: 0 0 2rem rgba(0, 0, 0, 0.5);
    }
    .backdrop {
      display: block;
      position: fixed;
      inset: 3rem 0 0 0;
      /* Reset the global button chrome — this is an invisible overlay. */
      padding: 0;
      margin: 0;
      border: none;
      border-radius: 0;
      background: rgba(0, 0, 0, 0.45);
      z-index: 20;
      cursor: default;
    }
    main {
      padding: 1rem 1rem 4rem;
    }
  }
  @media (max-width: 48rem) and (prefers-color-scheme: light) {
    aside {
      background: #fafafa;
    }
  }
</style>
