<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { loadMonaco } from '../lib/monaco';
  import type * as MonacoNs from 'monaco-editor';

  // Left = the baseline (applied manifest); right = current text. Both
  // are read-only by design — this view is for review, not editing. The
  // user toggles back to the editor view to make changes.
  export let original: string = '';
  export let modified: string = '';
  export let language: string = 'yaml';

  let mount: HTMLElement;
  let diffEditor: MonacoNs.editor.IStandaloneDiffEditor | null = null;
  let originalModel: MonacoNs.editor.ITextModel | null = null;
  let modifiedModel: MonacoNs.editor.ITextModel | null = null;
  let monaco: typeof MonacoNs | null = null;
  let bootError = '';

  onMount(async () => {
    try {
      monaco = await loadMonaco();
    } catch (err) {
      bootError = err instanceof Error ? err.message : String(err);
      return;
    }
    if (!mount) return;

    const dark = window.matchMedia?.('(prefers-color-scheme: dark)').matches;
    originalModel = monaco.editor.createModel(original, language);
    modifiedModel = monaco.editor.createModel(modified, language);
    diffEditor = monaco.editor.createDiffEditor(mount, {
      theme: dark ? 'vs-dark' : 'vs',
      automaticLayout: true,
      readOnly: true,
      renderSideBySide: true,
      enableSplitViewResizing: false,
      minimap: { enabled: false },
      fontSize: 13,
      wordWrap: 'on',
      scrollBeyondLastLine: false,
      originalEditable: false,
    });
    diffEditor.setModel({ original: originalModel, modified: modifiedModel });
  });

  onDestroy(() => {
    diffEditor?.dispose();
    originalModel?.dispose();
    modifiedModel?.dispose();
    diffEditor = null;
    originalModel = null;
    modifiedModel = null;
  });

  $: if (originalModel && originalModel.getValue() !== original) {
    originalModel.setValue(original);
  }
  $: if (modifiedModel && modifiedModel.getValue() !== modified) {
    modifiedModel.setValue(modified);
  }
</script>

<div class="wrapper">
  {#if bootError}
    <p class="err">Failed to load diff editor: {bootError}</p>
  {/if}
  <div class="editor" bind:this={mount}></div>
</div>

<style>
  .wrapper {
    height: 100%;
    display: flex;
    flex-direction: column;
    border-radius: 6px;
    overflow: hidden;
    border: 1px solid rgba(127, 127, 127, 0.25);
  }
  .editor {
    flex: 1;
    min-height: 24rem;
  }
  .err {
    margin: 0;
    padding: 0.75rem 1rem;
    color: #f57171;
    background: rgba(248, 81, 73, 0.08);
  }
</style>
