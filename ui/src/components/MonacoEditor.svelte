<script lang="ts">
  import { createEventDispatcher, onDestroy, onMount } from 'svelte';
  import { loadMonaco } from '../lib/monaco';
  import type * as MonacoNs from 'monaco-editor';

  export let value: string = '';
  export let language: string = 'yaml';
  // markers: external diagnostic list rendered as Monaco markers in the
  // gutter + on hover. The parent passes them in after debouncing the
  // server-side Validate call; the component only displays.
  export let markers: Array<{
    severity: 'error' | 'warning' | 'info';
    message: string;
    startLineNumber: number;
    startColumn: number;
    endLineNumber: number;
    endColumn: number;
  }> = [];
  // disabled: makes the editor read-only. Used for the "loading…" state.
  export let disabled: boolean = false;

  const dispatch = createEventDispatcher<{ change: string }>();

  let mount: HTMLElement;
  let editor: MonacoNs.editor.IStandaloneCodeEditor | null = null;
  let model: MonacoNs.editor.ITextModel | null = null;
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

    // Prefer the OS theme; Monaco's `vs-dark` is close enough to our
    // chrome that the editor doesn't look pasted in.
    const dark = window.matchMedia?.('(prefers-color-scheme: dark)').matches;
    model = monaco.editor.createModel(value, language);
    editor = monaco.editor.create(mount, {
      model,
      theme: dark ? 'vs-dark' : 'vs',
      automaticLayout: true,
      minimap: { enabled: false },
      fontSize: 13,
      tabSize: 2,
      wordWrap: 'on',
      readOnly: disabled,
      scrollBeyondLastLine: false,
      // Stable line numbers + lighter gutter — matches the chrome.
      lineNumbers: 'on',
      renderLineHighlight: 'gutter',
    });

    editor.onDidChangeModelContent(() => {
      if (!model) return;
      const next = model.getValue();
      // Don't dispatch when the change came from a parent `value` update —
      // only when the user actually typed.
      if (next !== value) {
        value = next;
        dispatch('change', next);
      }
    });

    applyMarkers();
  });

  onDestroy(() => {
    editor?.dispose();
    model?.dispose();
    editor = null;
    model = null;
  });

  // Reactive: parent updates flow into the editor; user edits flow back
  // through dispatch above. Two-way binding without infinite loops because
  // the change handler skips dispatch when value matches.
  $: if (model && model.getValue() !== value) {
    model.setValue(value);
  }
  $: if (editor) {
    editor.updateOptions({ readOnly: disabled });
  }
  $: if (monaco && model) {
    void markers;
    applyMarkers();
  }

  function applyMarkers() {
    if (!monaco || !model) return;
    const sev = monaco.MarkerSeverity;
    monaco.editor.setModelMarkers(model, 'openctl', markers.map((m) => ({
      severity: m.severity === 'error' ? sev.Error
        : m.severity === 'warning' ? sev.Warning
        : sev.Info,
      message: m.message,
      startLineNumber: m.startLineNumber,
      startColumn: m.startColumn,
      endLineNumber: m.endLineNumber,
      endColumn: m.endColumn,
    })));
  }
</script>

<div class="wrapper">
  {#if bootError}
    <p class="err">Failed to load editor: {bootError}</p>
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
