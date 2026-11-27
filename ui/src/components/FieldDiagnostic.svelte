<script lang="ts">
  import type { FieldError, ResourceRef } from '../lib/api';
  import { routeHref } from '../lib/router';

  export let field: FieldError;

  function inspectionTarget(value: unknown): ResourceRef | null {
    if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
    const ref = value as Partial<ResourceRef>;
    if (![ref.apiVersion, ref.kind, ref.name].every((part) =>
      typeof part === 'string' && part.length > 0 && part.trim() === part)) return null;
    // The detail router requires a group/version and reserves "new" for Create.
    if (!/^[^/\s]+\/[^/\s]+$/.test(ref.apiVersion!) || ref.name === 'new') return null;
    return ref as ResourceRef;
  }

  $: reference = field.referenceField === undefined || typeof field.referenceField === 'string'
    ? inspectionTarget(field.reference) : null;
  $: requestedField = typeof field.referenceField === 'string' ? field.referenceField : '';
</script>

{#if field.code}<code>[{field.code}]</code>{' '}{/if}{#if field.path}<code>{field.path}</code>{': '}{/if}{field.message ?? 'Invalid value'}
{#if reference}
  <span class="reference">
    <a href={routeHref({ name: 'detail', apiVersion: reference.apiVersion, kind: reference.kind, resourceName: reference.name })} target="_blank" rel="noopener noreferrer" title="Inspect in a new tab; keep this draft and preview">Inspect referenced resource</a>
    {' '}<code>{reference.apiVersion} {reference.kind}/{reference.name}</code>
    {#if requestedField} — requested field: <code>{requestedField}</code>{:else} — whole resource{/if}
  </span>
{/if}

<style>
  .reference { display: block; margin-top: 0.25rem; }
  a { color: #6ea8ff; text-decoration: none; }
  a:hover { text-decoration: underline; }
</style>
