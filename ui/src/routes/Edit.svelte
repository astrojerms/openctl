<script lang="ts">
  import { onDestroy } from 'svelte';
  import { resources, schemas, UnauthorizedError } from '../lib/api';
  import { parseYAML, resourceToYAML } from '../lib/yaml';
  import { routeHref, navigate } from '../lib/router';
  import MonacoEditor from '../components/MonacoEditor.svelte';

  export let apiVersion: string;
  export let kind: string;
  export let resourceName: string;

  let baseline = ''; // YAML the user is editing against — populated from `applied`
  let text = '';     // current editor contents
  let loading = true;
  let loadError = '';
  let parseError = '';
  let validateErrors: string[] = [];
  let validating = false;
  let lastValidatedText = '';

  // Single debounce timer for validation. Reset on every keystroke; only
  // fires after the user stops typing for 350ms. Tight enough to feel
  // live, loose enough to not hammer the server while typing.
  const VALIDATE_DEBOUNCE_MS = 350;
  let timer: number | undefined;

  $: void load(apiVersion, kind, resourceName);

  async function load(av: string, k: string, n: string) {
    loading = true;
    loadError = '';
    try {
      const r = await resources.get(av, k, n);
      const applied = r.applied;
      if (applied) {
        baseline = resourceToYAML(applied);
      } else {
        // No applied manifest on file — seed the editor with a skeleton
        // built from the observed resource so the user has a starting
        // shape instead of a blank pane.
        baseline = resourceToYAML({
          apiVersion: av,
          kind: k,
          metadata: { name: n },
          spec: r.resource.spec ?? {},
        });
      }
      text = baseline;
      // Run validation once on initial load so the user sees any
      // pre-existing issues with the applied manifest immediately.
      scheduleValidate();
    } catch (err) {
      if (err instanceof UnauthorizedError) return;
      loadError = err instanceof Error ? err.message : String(err);
    } finally {
      loading = false;
    }
  }

  function onChange(e: CustomEvent<string>) {
    text = e.detail;
    scheduleValidate();
  }

  function scheduleValidate() {
    if (timer !== undefined) clearTimeout(timer);
    timer = setTimeout(runValidate, VALIDATE_DEBOUNCE_MS) as unknown as number;
  }

  async function runValidate() {
    if (text === lastValidatedText) return;
    lastValidatedText = text;
    parseError = '';
    validateErrors = [];

    const parsed = parseYAML(text);
    if (parsed.error) {
      parseError = parsed.error;
      return;
    }
    if (!parsed.doc.apiVersion || !parsed.doc.kind) {
      parseError = 'apiVersion and kind are required';
      return;
    }

    validating = true;
    try {
      const resp = await schemas.validate({
        apiVersion: parsed.doc.apiVersion,
        kind: parsed.doc.kind,
        metadata: { name: parsed.doc.metadata?.name ?? '' },
        spec: parsed.doc.spec as Record<string, unknown> | undefined,
      });
      validateErrors = resp.errors ?? [];
    } catch (err) {
      if (err instanceof UnauthorizedError) return;
      validateErrors = [err instanceof Error ? err.message : String(err)];
    } finally {
      validating = false;
    }
  }

  onDestroy(() => {
    if (timer !== undefined) clearTimeout(timer);
  });

  function discard() {
    text = baseline;
    scheduleValidate();
  }

  function back() {
    navigate({ name: 'detail', apiVersion, kind, resourceName });
  }

  // U4.3 will fill this in with DryRunApply + destructive gates + Apply.
  // For U4.2 the button is disabled with a tooltip explaining why.
  $: applyBlocked = parseError !== '' || validateErrors.length > 0 || text === baseline;
  $: dirty = text !== baseline;

  // Turn server-side string errors into Monaco markers. CUE diagnostics
  // don't carry line info today (see api.proto ValidateResponse), so we
  // anchor every marker at line 1; the message still surfaces on hover
  // and the error pane below lists them in full.
  $: markers = (() => {
    const out: Array<{
      severity: 'error' | 'warning' | 'info';
      message: string;
      startLineNumber: number;
      startColumn: number;
      endLineNumber: number;
      endColumn: number;
    }> = [];
    if (parseError) {
      out.push({
        severity: 'error',
        message: parseError,
        startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 1,
      });
    }
    for (const e of validateErrors) {
      out.push({
        severity: 'error',
        message: e,
        startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 1,
      });
    }
    return out;
  })();
</script>

<section>
  <header>
    <div>
      <p class="crumbs">
        <a href={routeHref({ name: 'list', apiVersion, kind })}>{kind}</a>
        <span> · </span>
        <a href={routeHref({ name: 'detail', apiVersion, kind, resourceName })}>{resourceName}</a>
      </p>
      <h2>Edit {resourceName}</h2>
    </div>
    <div class="actions">
      <span class="state" class:dirty>{dirty ? 'unsaved changes' : 'no changes'}</span>
      {#if validating}
        <span class="state state-busy">validating…</span>
      {/if}
      <button on:click={discard} disabled={!dirty}>Discard</button>
      <button on:click={back}>Back</button>
      <button class="primary" disabled={applyBlocked} title={applyBlocked ? 'Apply panel ships in U4.3' : ''}>
        Apply
      </button>
    </div>
  </header>

  {#if loading}
    <p class="muted">Loading manifest…</p>
  {:else if loadError}
    <p class="err">{loadError}</p>
  {:else}
    <div class="editor-wrap">
      <MonacoEditor value={text} on:change={onChange} {markers} />
    </div>

    {#if parseError || validateErrors.length > 0}
      <article class="diag">
        <h3>Validation</h3>
        {#if parseError}
          <p class="err mono">YAML: {parseError}</p>
        {/if}
        {#each validateErrors as e}
          <p class="err mono">{e}</p>
        {/each}
      </article>
    {:else if !validating}
      <p class="muted small">No validation errors.</p>
    {/if}
  {/if}
</section>

<style>
  section {
    display: flex;
    flex-direction: column;
    gap: 1rem;
    height: calc(100vh - 7rem);
    max-width: 80rem;
  }
  header {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 1rem;
  }
  .crumbs {
    margin: 0 0 0.25rem;
    color: #888;
    font-size: 0.85rem;
  }
  .crumbs a {
    color: #6ea8ff;
    text-decoration: none;
  }
  .crumbs a:hover {
    text-decoration: underline;
  }
  h2 {
    margin: 0;
    font-size: 1.25rem;
  }
  .actions {
    display: flex;
    align-items: center;
    gap: 0.6rem;
  }
  .state {
    font-size: 0.8rem;
    color: #888;
    padding: 0.1em 0.6em;
    border-radius: 999px;
    background: rgba(127, 127, 127, 0.12);
  }
  .state.dirty {
    color: #ffce4d;
    background: rgba(255, 184, 0, 0.15);
  }
  .state-busy {
    color: #6ea8ff;
    background: rgba(74, 142, 240, 0.12);
  }
  .primary {
    background: #4a8ef0;
    color: white;
    border-color: #4a8ef0;
  }
  .primary:disabled {
    opacity: 0.5;
  }
  .editor-wrap {
    flex: 1;
    min-height: 20rem;
    display: flex;
  }
  .editor-wrap :global(.wrapper) {
    flex: 1;
  }
  .diag {
    background: rgba(248, 81, 73, 0.06);
    border-left: 3px solid #f57171;
    padding: 0.75rem 1rem;
    border-radius: 4px;
  }
  .diag h3 {
    margin: 0 0 0.5rem;
    font-size: 0.85rem;
    color: #aaa;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .err {
    color: #f57171;
    margin: 0.25rem 0;
  }
  .mono {
    font-family: ui-monospace, SFMono-Regular, monospace;
    font-size: 0.85rem;
  }
  .muted {
    color: #888;
  }
  .small {
    font-size: 0.85rem;
  }
</style>
