<script lang="ts">
  // Templates picker: lists the built-in parameterized starters and
  // links each into the wizard flow (TemplateWizard.svelte). Kept
  // simple on purpose — the interesting behavior lives in the wizard.

  import { onMount } from 'svelte';
  import {
    templates as templatesApi,
    UnauthorizedError,
    type TemplateSummary,
  } from '../lib/api';
  import { routeHref } from '../lib/router';

  let items: TemplateSummary[] = [];
  let loading = true;
  let error = '';

  onMount(async () => {
    try {
      const resp = await templatesApi.list();
      items = resp.templates ?? [];
    } catch (err) {
      if (err instanceof UnauthorizedError) return;
      error = err instanceof Error ? err.message : String(err);
    } finally {
      loading = false;
    }
  });
</script>

<section>
  <header>
    <h2>Templates</h2>
    <p class="lede">
      Parameterized starters. Fill in a few fields; the template
      handles the details.
    </p>
  </header>

  {#if loading}
    <p class="muted">Loading…</p>
  {:else if error}
    <p class="err">{error}</p>
  {:else if items.length === 0}
    <p class="muted">No templates registered.</p>
  {:else}
    <div class="grid">
      {#each items as t (t.name)}
        <a class="card" href={routeHref({ name: 'template', template: t.name })}>
          <h3>{t.displayName}</h3>
          <p class="desc">{t.description}</p>
          <p class="kind">
            <code>{t.kind}</code>
            <span class="muted small">· {t.apiVersion}</span>
          </p>
        </a>
      {/each}
    </div>
  {/if}
</section>

<style>
  section {
    max-width: 64rem;
  }
  header {
    margin-bottom: 1.25rem;
  }
  h2 {
    margin: 0;
    font-size: 1.25rem;
  }
  .lede {
    color: #888;
    margin: 0.3rem 0 0;
    font-size: 0.9rem;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(20rem, 1fr));
    gap: 1rem;
  }
  .card {
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
    padding: 1.1rem 1.25rem;
    background: rgba(127, 127, 127, 0.08);
    border: 1px solid rgba(127, 127, 127, 0.18);
    border-radius: 8px;
    text-decoration: none;
    color: inherit;
    transition: border-color 0.1s;
  }
  .card:hover {
    border-color: #4a8ef0;
  }
  .card h3 {
    margin: 0;
    font-size: 1rem;
  }
  .desc {
    color: #aaa;
    font-size: 0.85rem;
    margin: 0;
    flex: 1;
  }
  .kind {
    margin: 0;
    font-family: ui-monospace, SFMono-Regular, monospace;
    font-size: 0.75rem;
    color: #888;
  }
  .muted {
    color: #888;
  }
  .small {
    font-size: 0.75rem;
  }
  .err {
    color: #f57171;
  }
  code {
    background: rgba(127, 127, 127, 0.15);
    padding: 0 0.3em;
    border-radius: 3px;
  }
</style>
