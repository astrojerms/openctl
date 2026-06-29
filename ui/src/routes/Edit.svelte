<script lang="ts">
  import { onDestroy } from 'svelte';
  import {
    resources, schemas, UnauthorizedError,
    type DryRunApplyResponse,
  } from '../lib/api';
  import { parseYAML, resourceToYAML } from '../lib/yaml';
  import { ops as opsStore } from '../lib/ops';
  import type { OperationRow } from '../lib/watch';
  import { routeHref, navigate } from '../lib/router';
  import MonacoEditor from '../components/MonacoEditor.svelte';

  export let apiVersion: string;
  export let kind: string;
  export let resourceName: string;

  let baseline = '';
  let text = '';
  let loading = true;
  let loadError = '';

  // Debounced preview: validate + dry-run-apply fire together so the
  // panel can show both diagnostics and the planned diff in one update.
  const PREVIEW_DEBOUNCE_MS = 350;
  let timer: number | undefined;
  let lastPreviewedText = '';
  let previewing = false;
  let parseError = '';
  let validateErrors: string[] = [];
  let plan: DryRunApplyResponse | null = null;

  // Gate checkboxes — keyed by gate string (e.g. "allow_destructive").
  let checkedGates: Record<string, boolean> = {};

  // Apply lifecycle. `applying` covers the submit roundtrip; once we
  // have an op id we switch to live op-progress mode driven off the
  // existing ops store so we don't double-subscribe to WatchOperations.
  let applying = false;
  let applyError = '';
  let liveOpId = '';

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
        baseline = resourceToYAML({
          apiVersion: av,
          kind: k,
          metadata: { name: n },
          spec: r.resource.spec ?? {},
        });
      }
      text = baseline;
      schedulePreview();
    } catch (err) {
      if (err instanceof UnauthorizedError) return;
      loadError = err instanceof Error ? err.message : String(err);
    } finally {
      loading = false;
    }
  }

  function onChange(e: CustomEvent<string>) {
    text = e.detail;
    // Any edit invalidates a successful in-progress apply view — drop
    // back to preview mode. (We don't cancel an in-flight Apply RPC;
    // the user's edit can't change the server-side outcome at that
    // point.)
    if (liveOpId && !inflightOpRow(liveOpId)) {
      liveOpId = '';
    }
    schedulePreview();
  }

  function schedulePreview() {
    if (timer !== undefined) clearTimeout(timer);
    timer = setTimeout(runPreview, PREVIEW_DEBOUNCE_MS) as unknown as number;
  }

  async function runPreview() {
    if (text === lastPreviewedText) return;
    lastPreviewedText = text;
    parseError = '';
    validateErrors = [];
    plan = null;

    const parsed = parseYAML(text);
    if (parsed.error) {
      parseError = parsed.error;
      return;
    }
    if (!parsed.doc.apiVersion || !parsed.doc.kind) {
      parseError = 'apiVersion and kind are required';
      return;
    }

    const resource = {
      apiVersion: parsed.doc.apiVersion,
      kind: parsed.doc.kind,
      metadata: { name: parsed.doc.metadata?.name ?? '' },
      spec: parsed.doc.spec as Record<string, unknown> | undefined,
    };

    previewing = true;
    try {
      // Validate + DryRunApply in parallel — both round-trip the same
      // schema check internally so this isn't redundant work for the
      // server, just lets the UI render diagnostics independently of the
      // planning info.
      const [vResp, dResp] = await Promise.all([
        schemas.validate(resource),
        resources.dryRunApply(resource),
      ]);
      validateErrors = vResp.errors ?? [];
      plan = dResp;
      // Preserve existing checkbox state for gates that still apply;
      // drop gates that no longer apply.
      const next: Record<string, boolean> = {};
      for (const g of plan.requiredGates ?? []) {
        next[g] = checkedGates[g] ?? false;
      }
      checkedGates = next;
    } catch (err) {
      if (err instanceof UnauthorizedError) return;
      validateErrors = [err instanceof Error ? err.message : String(err)];
    } finally {
      previewing = false;
    }
  }

  async function doApply() {
    if (applyBlocked || !plan) return;
    const parsed = parseYAML(text);
    if (parsed.error || !parsed.doc.apiVersion || !parsed.doc.kind) return;

    applying = true;
    applyError = '';
    try {
      const resp = await resources.apply({
        resource: {
          apiVersion: parsed.doc.apiVersion,
          kind: parsed.doc.kind,
          metadata: { name: parsed.doc.metadata?.name ?? '' },
          spec: parsed.doc.spec as Record<string, unknown> | undefined,
        },
        allowDestructive: checkedGates['allow_destructive'] ?? false,
        iKnowThisBreaksTheCluster: checkedGates['i_know_this_breaks_the_cluster'] ?? false,
      });
      if (resp.operationId) {
        liveOpId = resp.operationId;
        // Treat the just-submitted text as the new baseline — Discard
        // should revert to what the user just sent, not the stale applied
        // manifest. Real applied-state catches up via the ops store +
        // Watch when the dispatcher writes back.
        baseline = text;
      }
    } catch (err) {
      if (err instanceof UnauthorizedError) return;
      applyError = err instanceof Error ? err.message : String(err);
    } finally {
      applying = false;
    }
  }

  function inflightOpRow(opId: string): OperationRow | null {
    const found = $opsStore.find((o) => o.id === opId);
    if (!found) return null;
    if (found.status === 'succeeded' || found.status === 'failed' || found.status === 'interrupted') {
      return null;
    }
    return found;
  }

  onDestroy(() => {
    if (timer !== undefined) clearTimeout(timer);
  });

  function discard() {
    text = baseline;
    schedulePreview();
  }

  function back() {
    navigate({ name: 'detail', apiVersion, kind, resourceName });
  }

  // Apply readiness: well-formed YAML + no validation errors + every
  // required gate checked + something to actually do. Also blocked
  // while a submission is in flight.
  $: gatesSatisfied = (plan?.requiredGates ?? []).every((g) => checkedGates[g]);
  $: hasChange = !!plan && (
    (plan.diff?.length ?? 0) > 0 ||
    (plan.children ?? []).some((c) => c.verb !== 'no-op')
  );
  $: applyBlocked =
    applying ||
    parseError !== '' ||
    validateErrors.length > 0 ||
    !plan ||
    !hasChange ||
    !gatesSatisfied;

  $: dirty = text !== baseline;

  // Live op row (when an Apply is in flight) drives the inline progress
  // banner. Driven off the shell-wide ops store so we don't open a
  // second WatchOperations.
  $: liveOp = liveOpId ? ($opsStore.find((o) => o.id === liveOpId) ?? null) : null;
  $: if (liveOp?.status === 'succeeded') {
    // Brief pause so the user sees the green flash, then jump to detail.
    setTimeout(() => {
      if (liveOpId && liveOp?.status === 'succeeded') {
        navigate({ name: 'detail', apiVersion, kind, resourceName });
      }
    }, 600);
  }

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
        severity: 'error', message: parseError,
        startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 1,
      });
    }
    for (const e of validateErrors) {
      out.push({
        severity: 'error', message: e,
        startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 1,
      });
    }
    return out;
  })();

  function gateLabel(g: string): string {
    switch (g) {
      case 'allow_destructive':
        return 'Allow destructive changes';
      case 'i_know_this_breaks_the_cluster':
        return 'I know this breaks the cluster';
      default: return g;
    }
  }

  function verbClass(v: string): string {
    switch (v) {
      case 'create': return 'verb-create';
      case 'destroy': return 'verb-destroy';
      case 'respec': return 'verb-respec';
      default: return 'verb-noop';
    }
  }

  function applyBlockReason(): string {
    if (applying) return 'Apply already submitted';
    if (parseError) return 'Fix YAML parse error before applying';
    if (validateErrors.length > 0) return 'Fix validation errors before applying';
    if (!hasChange) return 'No changes to apply';
    if (!gatesSatisfied) return 'Check the destructive-change confirmations above';
    return '';
  }
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
      {#if previewing}
        <span class="state state-busy">previewing…</span>
      {/if}
      <button on:click={discard} disabled={!dirty || applying}>Discard</button>
      <button on:click={back} disabled={applying}>Back</button>
      <button
        class="primary"
        disabled={applyBlocked}
        on:click={doApply}
        title={applyBlocked && plan ? applyBlockReason() : ''}
      >
        {applying ? 'Submitting…' : 'Apply'}
      </button>
    </div>
  </header>

  {#if loading}
    <p class="muted">Loading manifest…</p>
  {:else if loadError}
    <p class="err">{loadError}</p>
  {:else}
    <div class="editor-wrap">
      <MonacoEditor value={text} on:change={onChange} {markers} disabled={applying} />
    </div>

    {#if applyError}
      <article class="diag">
        <h3>Apply failed</h3>
        <p class="err mono">{applyError}</p>
      </article>
    {/if}

    {#if liveOp}
      <article class="op-card op-{liveOp.status}">
        <h3>Operation {liveOp.id.slice(0, 12)} — {liveOp.status}</h3>
        {#if liveOp.error}
          <p class="err mono">{liveOp.error}</p>
        {:else if liveOp.status === 'pending'}
          <p class="muted">Queued, waiting for the dispatcher to pick it up…</p>
        {:else if liveOp.status === 'running'}
          <p class="muted">Provider is converging the resource. Tail in the ops drawer for substeps.</p>
        {:else if liveOp.status === 'succeeded'}
          <p>Applied. Returning to detail…</p>
        {:else}
          <p class="muted">Status: {liveOp.status}</p>
        {/if}
      </article>
    {/if}

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
    {/if}

    {#if plan && validateErrors.length === 0 && !parseError}
      <article class="preview">
        <header class="preview-head">
          <h3>Preview</h3>
          <span class="muted small">{plan.summary || (hasChange ? 'changes pending' : 'no-op')}</span>
        </header>

        {#if !hasChange}
          <p class="muted">No changes. The manifest matches what's already applied.</p>
        {/if}

        {#if (plan.diff?.length ?? 0) > 0}
          <table class="diff">
            <thead>
              <tr><th>Path</th><th>Current</th><th>Will become</th></tr>
            </thead>
            <tbody>
              {#each (plan.diff ?? []) as d}
                {@const _d = /** @type {DriftEntry} */ (d)}
                <tr>
                  <td class="mono">{_d.path}</td>
                  <td class="mono">{_d.desired}</td>
                  <td class="mono">{_d.observed}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}

        {#if (plan.children?.length ?? 0) > 0}
          <ul class="children">
            {#each plan.children ?? [] as c}
              <li class={verbClass(c.verb)}>
                <span class="verb">{c.verb}</span>
                <span class="mono">{c.kind}/{c.name}</span>
                {#if c.detail}<span class="muted small">— {c.detail}</span>{/if}
              </li>
            {/each}
          </ul>
        {/if}

        {#if (plan.requiredGates?.length ?? 0) > 0}
          <div class="gates">
            <h4>Destructive change — confirm before applying:</h4>
            {#each plan.requiredGates ?? [] as g}
              <label class="gate">
                <input type="checkbox" bind:checked={checkedGates[g]} />
                <span>{gateLabel(g)}</span>
                <code class="muted small">{g}</code>
              </label>
            {/each}
          </div>
        {/if}
      </article>
    {/if}
  {/if}
</section>

<style>
  section {
    display: flex;
    flex-direction: column;
    gap: 1rem;
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
    min-height: 24rem;
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
  .preview {
    background: rgba(127, 127, 127, 0.06);
    border-radius: 6px;
    padding: 1rem 1.25rem;
  }
  .preview-head {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    margin-bottom: 0.75rem;
  }
  .preview h3 {
    margin: 0;
    font-size: 0.9rem;
    color: #aaa;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  table.diff {
    width: 100%;
    border-collapse: collapse;
    margin-bottom: 0.75rem;
  }
  .diff th, .diff td {
    text-align: left;
    padding: 0.35rem 0.6rem;
    border-bottom: 1px solid rgba(127, 127, 127, 0.15);
  }
  .diff th {
    font-size: 0.7rem;
    color: #888;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  ul.children {
    list-style: none;
    margin: 0 0 0.75rem;
    padding: 0;
  }
  ul.children li {
    padding: 0.3rem 0;
    display: flex;
    gap: 0.5rem;
    align-items: baseline;
  }
  .verb {
    display: inline-block;
    min-width: 5rem;
    padding: 0.05em 0.5em;
    border-radius: 4px;
    font-size: 0.8rem;
    font-weight: 600;
    text-align: center;
    text-transform: lowercase;
  }
  .verb-create  .verb { background: rgba(46, 160, 67, 0.18); color: #5fdb78; }
  .verb-destroy .verb { background: rgba(248, 81, 73, 0.18); color: #ff8980; }
  .verb-respec  .verb { background: rgba(255, 184, 0, 0.18); color: #ffce4d; }
  .verb-noop    .verb { background: rgba(127, 127, 127, 0.18); color: #aaa; }
  .gates {
    margin-top: 0.75rem;
    padding: 0.75rem 1rem;
    background: rgba(255, 184, 0, 0.08);
    border-left: 3px solid #ffce4d;
    border-radius: 4px;
  }
  .gates h4 {
    margin: 0 0 0.5rem;
    font-size: 0.85rem;
    color: #ffce4d;
  }
  .gate {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.25rem 0;
    cursor: pointer;
  }
  .gate input {
    margin: 0;
  }
  .op-card {
    border-left: 3px solid #6ea8ff;
    background: rgba(74, 142, 240, 0.06);
    padding: 0.75rem 1rem;
    border-radius: 4px;
  }
  .op-card h3 {
    margin: 0 0 0.5rem;
    font-size: 0.9rem;
    color: #aaa;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .op-succeeded { border-color: #5fdb78; background: rgba(46, 160, 67, 0.08); }
  .op-failed, .op-interrupted { border-color: #f57171; background: rgba(248, 81, 73, 0.06); }
  .op-card p {
    margin: 0;
  }
</style>
