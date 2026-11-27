<script lang="ts">
  import type { Writable } from 'svelte/store';
  import Edit from './Edit.svelte';

  export let route: Writable<{ apiVersion: string; kind: string; resourceName: string }>;
</script>

<Edit apiVersion={$route.apiVersion} kind={$route.kind} resourceName={$route.resourceName} />
