<script lang="ts">
  import { onMount } from 'svelte';
  import { auth, refresh } from './lib/auth';
  import Login from './routes/Login.svelte';
  import Home from './routes/Home.svelte';

  onMount(() => {
    void refresh();
  });
</script>

{#if $auth.kind === 'unknown'}
  <main class="boot">
    <p>Loading…</p>
  </main>
{:else if $auth.kind === 'signed-out'}
  <Login />
{:else}
  <Home me={$auth.me} />
{/if}

<style>
  .boot {
    display: grid;
    place-items: center;
    min-height: 100vh;
    color: #888;
  }
</style>
