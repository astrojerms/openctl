<script lang="ts">
  import { onDestroy } from 'svelte';
  import { repo, UnauthorizedError, type CommitInfo } from '../lib/api';
  import MonacoDiffEditor from './MonacoDiffEditor.svelte';

  // The resource whose committed manifest history we diff, plus the current
  // desired YAML to diff a selected commit against (right-hand side).
  export let apiVersion: string;
  export let kind: string;
  export let resourceName: string;
  export let appliedYaml = '';
  export let signal: AbortSignal | undefined = undefined;
  export let onUnauthorized: (() => void) | undefined = undefined;

  let commits: CommitInfo[] = [];
  let selectedSha = '';
  let commitYaml = '';
  let loading = false;
  let error = '';
  let historyController: AbortController | null = null;
  let commitController: AbortController | null = null;

  async function loadHistory(av: string, k: string, n: string, routeSignal: AbortSignal | undefined): Promise<void> {
    historyController?.abort();
    commitController?.abort();
    commitController = null;
    const current = new AbortController();
    historyController = current;
    const requestSignal = routeSignal ? AbortSignal.any([routeSignal, current.signal]) : current.signal;
    commits = [];
    selectedSha = '';
    commitYaml = '';
    error = '';
    loading = true;
    try {
      if (!av || !k || !n || requestSignal.aborted) return;
      const resp = await repo.history({ apiVersion: av, kind: k, name: n }, { signal: requestSignal });
      if (historyController !== current || requestSignal.aborted) return;
      commits = resp.commits ?? [];
    } catch (err) {
      if (historyController !== current || requestSignal.aborted) return;
      if (err instanceof UnauthorizedError) {
        commits = [];
        commitYaml = '';
        onUnauthorized?.();
      }
      error = err instanceof Error ? err.message : String(err);
    } finally {
      if (historyController === current) loading = false;
    }
  }

  async function loadCommit(av: string, k: string, n: string, sha: string, routeSignal: AbortSignal | undefined): Promise<void> {
    commitController?.abort();
    const current = new AbortController();
    commitController = current;
    const requestSignal = routeSignal ? AbortSignal.any([routeSignal, current.signal]) : current.signal;
    commitYaml = '';
    if (!sha || requestSignal.aborted) return;
    error = '';
    try {
      const resp = await repo.atCommit({ apiVersion: av, kind: k, name: n }, sha, { signal: requestSignal });
      if (commitController !== current || requestSignal.aborted) return;
      commitYaml = resp.existed
        ? (resp.yaml ?? '')
        : '# (this resource did not exist at the selected commit)\n';
    } catch (err) {
      if (commitController !== current || requestSignal.aborted) return;
      if (err instanceof UnauthorizedError) {
        commits = [];
        commitYaml = '';
        onUnauthorized?.();
      }
      error = err instanceof Error ? err.message : String(err);
    }
  }

  $: void loadHistory(apiVersion, kind, resourceName, signal);
  $: void loadCommit(apiVersion, kind, resourceName, selectedSha, signal);

  onDestroy(() => {
    historyController?.abort();
    commitController?.abort();
    historyController = null;
    commitController = null;
  });

  function commitLabel(c: CommitInfo): string {
    const short = (c.sha ?? '').slice(0, 8);
    const when = c.committedAt ? new Date(c.committedAt).toLocaleString() : '';
    const subject = c.subject ?? '';
    return `${short} · ${subject}${when ? ` · ${when}` : ''}`;
  }
</script>

<article class="card">
  <h3>History</h3>
  {#if loading}
    <p class="muted small">Loading history…</p>
  {:else if error && commits.length === 0}
    <p class="error small" role="alert">{error}</p>
  {:else if commits.length === 0}
    <p class="muted small">No committed history for this resource yet.</p>
  {:else}
    <label class="history-picker">
      Compare current against commit:
      <select bind:value={selectedSha}>
        <option value="">— select a commit —</option>
        {#each commits as c (c.sha)}
          <option value={c.sha}>{commitLabel(c)}</option>
        {/each}
      </select>
    </label>
    {#if error}<p class="error small" role="alert">{error}</p>{/if}
    {#if selectedSha && commitYaml}
      <p class="muted small">Left: selected commit · Right: current desired manifest</p>
      <div class="diff-wrap">
        <MonacoDiffEditor original={commitYaml} modified={appliedYaml} language="yaml" />
      </div>
    {/if}
  {/if}
</article>

<style>
  .history-picker {
    display: block;
    margin-bottom: 0.5rem;
  }
  .history-picker select {
    margin-left: 0.5rem;
    min-width: 24rem;
    max-width: 100%;
  }
  .diff-wrap {
    height: 420px;
    border: 1px solid #ddd;
    border-radius: 4px;
    overflow: hidden;
  }
  .error {
    color: #c0392b;
  }
</style>
