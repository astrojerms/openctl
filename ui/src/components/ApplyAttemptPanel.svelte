<script lang="ts">
  import type { ApplyAttemptState } from '../lib/applyAttempt';
  import { routeHref } from '../lib/router';
  import { focusOpsDrawer } from '../lib/opsDrawer';

  export let state: ApplyAttemptState;
  export let onRecover: () => void | Promise<void>;
  export let onVerify: () => void | Promise<void>;
  export let onReviewNew: () => void | Promise<void>;
  export let onInspect: ((id: string) => void) | undefined = undefined;
  $: terminal = !!state.operation && ['succeeded', 'failed', 'interrupted', 'canceled'].includes(state.operation.status);
  function inspect() {
    const attempt = state.attempt;
    if (!attempt?.operationId) return;
    if (onInspect) onInspect(attempt.operationId);
    else focusOpsDrawer({ apiVersion: attempt.target.apiVersion, kind: attempt.target.kind, resourceName: attempt.target.name, opId: attempt.operationId });
  }
</script>

{#if state.attempt || state.error || state.busy === 'binding'}
  <article class="apply-attempt" aria-label="Apply request recovery">
    <header>
      <h3>Apply request</h3>
      <div class="controls">
        {#if state.attempt}
          <button type="button" on:click={onRecover} disabled={!!state.busy || !state.ready}>Recover Apply request</button>
          {#if state.attempt.operationId}
            <button type="button" on:click={inspect} disabled={!!state.busy}>Inspect Apply operation</button>
          {/if}
          {#if terminal && !state.reviewingNew}
            <button type="button" on:click={onReviewNew} disabled={!!state.busy}>Review new Apply</button>
          {/if}
        {:else if state.error}
          <button type="button" on:click={onVerify} disabled={!!state.busy}>Verify Apply recovery</button>
        {/if}
      </div>
    </header>
    {#if state.busy}<p role="status">{state.busy === 'submitting' ? 'Submitting reviewed Apply…' : state.busy === 'recovering' ? 'Reading the original request…' : 'Verifying controller and principal…'}</p>{/if}
    {#if state.error}<p class="error" role="alert">{state.error}</p>{/if}
    {#if state.attempt}
      <p>Request ID: <code>{state.attempt.requestId}</code></p>
      <p>Submitted target:
        <a href={routeHref({ name: 'detail', apiVersion: state.attempt.target.apiVersion, kind: state.attempt.target.kind, resourceName: state.attempt.target.name })}>{state.attempt.target.kind}/{state.attempt.target.name}</a>
        <span class="muted"> · {state.attempt.target.apiVersion}</span>
      </p>
      {#if state.attempt.operationId}<p>Operation ID: <code>{state.attempt.operationId}</code></p>{/if}
      {#if state.operation}
        <p>Apply operation: <strong>{state.operation.status}</strong></p>
        {#if state.operation.error}<p class="error">{state.operation.error}</p>{/if}
        {#if state.operation.status === 'succeeded'}<p class="muted">The operation succeeded. Inspect the resource for current readiness; operation history is not live health.</p>{/if}
      {:else}
        <p class="muted">{state.attempt.operationId ? 'Apply acknowledged; its outcome is not yet observed.' : 'Admission is uncertain or not yet acknowledged. Keep this request identity.'}</p>
      {/if}
      {#if state.reviewingNew}<p>New review opened. Refresh the preview and explicitly confirm any required gates before submitting a new request.</p>{/if}
      <p class="muted">Recovery only reads accepted work. Reload never resubmits or restores consent. This tab saves request metadata, not drafts, parameters or credentials; the submitted target may differ from the current draft. Leaving does not cancel controller work.</p>
    {/if}
  </article>
{/if}

<style>
  .apply-attempt { border: 1px solid var(--border, currentColor); border-radius: 6px; padding: 1rem; margin: 1rem 0; overflow-wrap: anywhere; }
  header { display: flex; gap: 1rem; align-items: center; justify-content: space-between; flex-wrap: wrap; }
  h3 { margin: 0; font-size: 1rem; }
  .controls { display: flex; flex-wrap: wrap; gap: .5rem; }
  button { cursor: pointer; }
  button:disabled { cursor: default; opacity: .55; }
  code { user-select: all; }
  .error { color: var(--error, currentColor); }
  .muted { opacity: .75; font-size: .9rem; }
</style>
