<script lang="ts">
  // TemplateWizard: fetches a template's parameter set, renders a
  // form using the parameters (a lightweight cousin of FormField for
  // template shape, since template params are flat name+type pairs
  // instead of a nested schema tree), and on submit:
  //   1. calls RenderTemplate to get a full Resource
  //   2. submits it via the normal Apply RPC
  //   3. navigates to the new resource's detail page on success
  //
  // Same DryRun preview + Apply pipeline as the Create flow, just
  // fronted by a template-specific parameter form.

  import { onDestroy, onMount, setContext } from 'svelte';
  import { writable } from 'svelte/store';
  import {
    templates as templatesApi,
    resources,
    UnauthorizedError,
    type GetTemplateResponse,
    type TemplateParameter,
    type DryRunApplyResponse,
  } from '../lib/api';
  import { ensureOptions, optionsStore } from '../lib/options';
  import { routeHref, navigate } from '../lib/router';
  import { resourceToYAML } from '../lib/yaml';

  export let template: string;

  let info: GetTemplateResponse | null = null;
  let loading = true;
  let loadError = '';

  // params: reactive dict of user-entered values. Seeded from each
  // parameter's default (parsed from defaultJson) on load.
  let params: Record<string, unknown> = {};

  // Live preview of the rendered manifest. Debounced against param
  // edits so we don't render on every keystroke.
  const PREVIEW_DEBOUNCE_MS = 350;
  let previewTimer: number | undefined;
  let renderedYAML = '';
  let renderError = '';
  let dryRun: DryRunApplyResponse | null = null;
  let dryRunError = '';
  let previewing = false;

  // Apply state.
  let submitting = false;
  let submitError = '';

  // options context so the OptionsKind dropdowns can resolve against
  // the containing apiVersion (which we know from the template summary).
  const apiVersionCtx = writable('');
  setContext('resourceAPIVersionStore', apiVersionCtx);

  onMount(async () => {
    try {
      info = await templatesApi.get(template);
      apiVersionCtx.set(info.summary.apiVersion);
      for (const p of info.parameters ?? []) {
        if (p.defaultJson) {
          try {
            params[p.name] = JSON.parse(p.defaultJson);
          } catch {
            /* ignore malformed defaults; user fills in */
          }
        }
      }
      params = { ...params };
      schedulePreview();
    } catch (err) {
      if (err instanceof UnauthorizedError) return;
      loadError = err instanceof Error ? err.message : String(err);
    } finally {
      loading = false;
    }
  });

  onDestroy(() => {
    if (previewTimer !== undefined) clearTimeout(previewTimer);
  });

  function setParam(name: string, value: unknown) {
    params = { ...params, [name]: value };
    schedulePreview();
  }

  function schedulePreview() {
    if (previewTimer !== undefined) clearTimeout(previewTimer);
    previewTimer = setTimeout(runPreview, PREVIEW_DEBOUNCE_MS) as unknown as number;
  }

  async function runPreview() {
    if (!info) return;
    renderError = '';
    dryRunError = '';
    dryRun = null;
    previewing = true;
    try {
      const rendered = await templatesApi.render(template, params);
      renderedYAML = resourceToYAML(rendered.resource);
      try {
        dryRun = await resources.dryRunApply(rendered.resource);
      } catch (err) {
        if (err instanceof UnauthorizedError) return;
        dryRunError = err instanceof Error ? err.message : String(err);
      }
    } catch (err) {
      if (err instanceof UnauthorizedError) return;
      renderError = err instanceof Error ? err.message : String(err);
      renderedYAML = '';
    } finally {
      previewing = false;
    }
  }

  async function submit() {
    if (!info || submitting) return;
    submitError = '';
    submitting = true;
    try {
      // Render one more time so the applied manifest matches the last
      // preview even if params changed mid-preview.
      const rendered = await templatesApi.render(template, params);
      const resp = await resources.apply({ resource: rendered.resource });
      if (resp.operationId) {
        // Navigate to the new resource's detail page — the op will tail
        // in the drawer + the detail's inline op banner.
        const name = rendered.resource.metadata?.name ?? '';
        if (name && info.summary.apiVersion && info.summary.kind) {
          navigate({
            name: 'detail',
            apiVersion: info.summary.apiVersion,
            kind: info.summary.kind,
            resourceName: name,
          });
        }
      }
    } catch (err) {
      if (err instanceof UnauthorizedError) return;
      submitError = err instanceof Error ? err.message : String(err);
    } finally {
      submitting = false;
    }
  }

  // OptionsKind dropdown support: for each param with optionsKind set,
  // trigger the fetch on load so the select is populated by the time
  // the user reaches it.
  $: if (info) {
    for (const p of info.parameters ?? []) {
      if (p.optionsKind) {
        void ensureOptions(info.summary.apiVersion, p.optionsKind);
      }
    }
  }

  function optionsForParam(p: TemplateParameter): string[] | null {
    if (!p.optionsKind || !info) return null;
    return $optionsStore.data[`${info.summary.apiVersion}/${p.optionsKind}`] ?? null;
  }

  $: hasChange = !!dryRun && (
    (dryRun.diff?.length ?? 0) > 0 ||
    (dryRun.children ?? []).some((c) => c.verb !== 'no-op')
  );
  $: applyBlocked = submitting || !!renderError || !!dryRunError || !dryRun || !hasChange
    || (dryRun.validationErrors?.length ?? 0) > 0;
</script>

<section>
  <header>
    <p class="crumbs">
      <a href={routeHref({ name: 'templates' })}>Templates</a>
    </p>
    {#if info}
      <h2>{info.summary.displayName}</h2>
      <p class="desc">{info.summary.description}</p>
    {:else if loading}
      <h2>Loading…</h2>
    {/if}
  </header>

  {#if loadError}
    <p class="err">{loadError}</p>
  {:else if info}
    <div class="wizard">
      <div class="params">
        <h3>Parameters</h3>
        {#each info.parameters ?? [] as p (p.name)}
          {@const opts = optionsForParam(p)}
          <div class="row">
            <label for={`param-${p.name}`}>
              {p.name}{#if p.required}<span class="req">*</span>{/if}
            </label>
            {#if p.description}
              <p class="desc small">{p.description}</p>
            {/if}
            {#if opts !== null}
              <select
                id={`param-${p.name}`}
                value={(params[p.name] as string) ?? ''}
                on:change={(e) => setParam(p.name, (e.currentTarget as HTMLSelectElement).value)}
              >
                <option value="">— pick a {p.optionsKind} —</option>
                {#each opts as name}
                  <option value={name}>{name}</option>
                {/each}
              </select>
            {:else if p.optionsKind}
              <input
                id={`param-${p.name}`}
                type="text"
                placeholder={`Loading ${p.optionsKind}…`}
                value={(params[p.name] as string) ?? ''}
                on:input={(e) => setParam(p.name, (e.currentTarget as HTMLInputElement).value)}
              />
            {:else if p.enum && p.enum.length > 0}
              <select
                id={`param-${p.name}`}
                value={(params[p.name] as string) ?? ''}
                on:change={(e) => setParam(p.name, (e.currentTarget as HTMLSelectElement).value)}
              >
                {#each p.enum as v}
                  <option value={v}>{v}</option>
                {/each}
              </select>
            {:else if p.type === 'int'}
              <input
                id={`param-${p.name}`}
                type="number"
                value={(params[p.name] as number | undefined) ?? ''}
                on:input={(e) => {
                  const raw = (e.currentTarget as HTMLInputElement).value;
                  setParam(p.name, raw === '' ? null : Number(raw));
                }}
              />
            {:else if p.type === 'bool'}
              <label class="bool">
                <input
                  id={`param-${p.name}`}
                  type="checkbox"
                  checked={!!params[p.name]}
                  on:change={(e) => setParam(p.name, (e.currentTarget as HTMLInputElement).checked)}
                />
                <span>{params[p.name] ? 'true' : 'false'}</span>
              </label>
            {:else}
              <input
                id={`param-${p.name}`}
                type="text"
                value={(params[p.name] as string) ?? ''}
                on:input={(e) => setParam(p.name, (e.currentTarget as HTMLInputElement).value)}
              />
            {/if}
          </div>
        {/each}

        <div class="actions">
          <button class="primary" disabled={applyBlocked} on:click={submit}>
            {submitting ? 'Creating…' : 'Create'}
          </button>
          {#if previewing}
            <span class="muted small">previewing…</span>
          {/if}
        </div>
        {#if submitError}
          <p class="err small">{submitError}</p>
        {/if}
        {#if renderError}
          <p class="err small">Render error: {renderError}</p>
        {/if}
        {#if dryRunError}
          <p class="err small">Dry-run error: {dryRunError}</p>
        {/if}
        {#if dryRun && (dryRun.validationErrors?.length ?? 0) > 0}
          <article class="diag">
            <h4>Validation</h4>
            {#each dryRun.validationErrors ?? [] as e}
              <p class="err small mono">{e}</p>
            {/each}
          </article>
        {/if}
      </div>

      <div class="preview">
        <h3>Rendered manifest</h3>
        {#if renderedYAML}
          <pre>{renderedYAML}</pre>
        {:else}
          <p class="muted">Fill in the required parameters to preview.</p>
        {/if}
        {#if dryRun?.summary}
          <p class="muted small preview-summary">{dryRun.summary}</p>
        {/if}
      </div>
    </div>
  {/if}
</section>

<style>
  section {
    max-width: 80rem;
  }
  header {
    margin-bottom: 1.25rem;
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
  h3 {
    margin: 0 0 0.5rem;
    font-size: 0.95rem;
  }
  .desc {
    color: #aaa;
    font-size: 0.9rem;
    margin: 0.3rem 0 0;
  }
  .small {
    font-size: 0.8rem;
  }
  .wizard {
    display: grid;
    grid-template-columns: minmax(0, 2fr) minmax(0, 1fr);
    gap: 1.25rem;
    align-items: start;
  }
  .params {
    display: flex;
    flex-direction: column;
    gap: 0.85rem;
  }
  .row {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }
  label {
    font-size: 0.85rem;
    color: #aaa;
    font-family: ui-monospace, SFMono-Regular, monospace;
  }
  .req {
    color: #ff8980;
    margin-left: 0.2em;
  }
  input[type='text'],
  input[type='number'],
  select {
    padding: 0.4rem 0.6rem;
    background: rgba(0, 0, 0, 0.2);
    color: inherit;
    border: 1px solid rgba(127, 127, 127, 0.25);
    border-radius: 4px;
    font-size: 0.9rem;
    font-family: inherit;
    box-sizing: border-box;
  }
  @media (prefers-color-scheme: light) {
    input[type='text'],
    input[type='number'],
    select {
      background: #fff;
      border-color: #ccc;
    }
  }
  .bool {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.9rem;
    cursor: pointer;
    font-family: inherit;
    color: inherit;
  }
  .actions {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    margin-top: 0.5rem;
  }
  .primary {
    background: #4a8ef0;
    color: white;
    border-color: #4a8ef0;
    padding: 0.5em 1.2em;
    border-radius: 6px;
    font-size: 0.9rem;
    font-weight: 500;
    cursor: pointer;
  }
  .primary:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
  .preview {
    border: 1px solid rgba(127, 127, 127, 0.18);
    border-radius: 6px;
    padding: 1rem;
    position: sticky;
    top: 1rem;
    max-height: 80vh;
    overflow: auto;
  }
  pre {
    background: rgba(127, 127, 127, 0.08);
    padding: 0.75rem 1rem;
    margin: 0.5rem 0 0;
    border-radius: 4px;
    font-size: 0.8rem;
    font-family: ui-monospace, SFMono-Regular, monospace;
    white-space: pre-wrap;
    word-break: break-word;
  }
  .preview-summary {
    margin-top: 0.5rem;
    color: #6ea8ff;
  }
  .muted {
    color: #888;
  }
  .err {
    color: #f57171;
  }
  .mono {
    font-family: ui-monospace, SFMono-Regular, monospace;
  }
  .diag {
    margin-top: 0.5rem;
    padding: 0.5rem 0.75rem;
    background: rgba(248, 81, 73, 0.08);
    border-left: 3px solid #ff8980;
    border-radius: 6px;
  }
  .diag h4 {
    margin: 0 0 0.3rem;
    font-size: 0.8rem;
    color: #ff8980;
  }

  /* Mobile: stack the params form above its preview instead of side-by-side,
     and let the preview flow instead of sticking. Keep in sync with
     --bp-mobile (48rem) in app.css. */
  @media (max-width: 48rem) {
    .wizard {
      grid-template-columns: minmax(0, 1fr);
    }
    .preview {
      position: static;
      max-height: none;
    }
  }
</style>
