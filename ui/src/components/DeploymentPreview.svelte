<script lang="ts">
  import type { DryRunApplyResponse } from '../lib/api';
  import FieldDiagnostic from './FieldDiagnostic.svelte';

  export let preview: DryRunApplyResponse | null;
  export let reviewed = false;
  $: errors = (preview?.validationErrors ?? []).filter((problem) =>
    !(preview?.fieldErrors ?? []).some((field) => problem === field.message || problem === `${field.path}: ${field.message}`));
</script>

{#if preview}
  <p><strong>{reviewed ? preview.operation : 'Preview blocked'}</strong> — {preview.summary}</p>
  {#if preview.fieldErrors?.length || errors.length}
    <ul class="error" aria-label="Preview validation errors">
      {#each preview.fieldErrors ?? [] as field}<li><FieldDiagnostic {field} /></li>{/each}
      {#each errors as problem}<li>{problem}</li>{/each}
    </ul>
  {/if}
  {#if preview.diff?.length}<ul>{#each preview.diff as change}<li><code>{change.path}</code>: stored {change.desired} → submitted {change.observed}</li>{/each}</ul>{/if}
  {#if preview.metadataDiff?.length}
    <h4>Desired metadata changes</h4>
    <ul>{#each preview.metadataDiff as change}<li><code>{change.path}</code>: stored {change.desired} → submitted {change.observed}</li>{/each}</ul>
  {/if}
  {#if reviewed}<slot />{/if}
{/if}

<style>
  .error { color: var(--danger, #f87171); }
  code { overflow-wrap: anywhere; }
</style>
