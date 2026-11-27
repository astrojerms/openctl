<script lang="ts">
  import { createEventDispatcher } from 'svelte';

  export let value = '';
  export let disabled = false;
  export let original: string | undefined = undefined;
  export let modified = '';
  const dispatch = createEventDispatcher<{ change: string }>();
</script>

{#if original !== undefined}
  <pre aria-label="Applied baseline">{original}</pre>
  <pre aria-label="Reviewed diff">{modified}</pre>
{:else}
  <textarea aria-label="Manifest editor" {value} {disabled} on:input={(event) => dispatch('change', event.currentTarget.value)}></textarea>
{/if}
