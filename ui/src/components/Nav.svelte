<script lang="ts">
  import type { Catalogue } from '../lib/catalogue';
  import { routeHref } from '../lib/router';

  export let catalogue: Catalogue;
  export let activeKindKey: string;

  // Sort providers alphabetically for stable display order. The catalogue
  // store already sorts kinds within each provider.
  $: providers = [...catalogue.byProvider.entries()].sort((a, b) =>
    a[0].localeCompare(b[0]),
  );
</script>

<nav>
  {#if providers.length === 0}
    <p class="empty">No kinds loaded.</p>
  {/if}
  {#each providers as [provider, kinds]}
    <section>
      <h3>{provider}</h3>
      <ul>
        {#each kinds as kind}
          <li class:active={kind.key === activeKindKey}>
            <a href={routeHref({ name: 'list', apiVersion: kind.apiVersion, kind: kind.kind })}>
              <span class="kind">{kind.kind}</span>
              <span class="count" title={kind.count === null ? 'count unavailable' : ''}>
                {kind.count === null ? '—' : kind.count}
              </span>
            </a>
          </li>
        {/each}
      </ul>
    </section>
  {/each}
</nav>

<style>
  nav {
    font-size: 0.9rem;
  }
  section {
    margin-bottom: 1.25rem;
  }
  h3 {
    text-transform: uppercase;
    font-size: 0.7rem;
    letter-spacing: 0.06em;
    color: #777;
    margin: 0 0 0.5rem;
    padding: 0 0.5rem;
  }
  ul {
    list-style: none;
    padding: 0;
    margin: 0;
  }
  li a {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 0.35rem 0.5rem;
    border-radius: 4px;
    color: inherit;
    text-decoration: none;
  }
  li a:hover {
    background: rgba(127, 127, 127, 0.1);
  }
  li.active a {
    background: rgba(74, 142, 240, 0.18);
    color: #6ea8ff;
  }
  .count {
    color: #888;
    font-variant-numeric: tabular-nums;
    font-size: 0.85em;
    background: rgba(127, 127, 127, 0.12);
    padding: 0.05em 0.5em;
    border-radius: 999px;
    min-width: 1.5em;
    text-align: center;
  }
  .empty {
    color: #777;
    padding: 0 0.5rem;
  }
</style>
