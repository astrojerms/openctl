<script lang="ts">
  import { repo, type CommitInfo } from '../lib/api';
  import MonacoDiffEditor from './MonacoDiffEditor.svelte';

  // The resource whose committed manifest history we diff, plus the current
  // desired YAML to diff a selected commit against (right-hand side).
  export let apiVersion: string;
  export let kind: string;
  export let resourceName: string;
  export let appliedYaml = '';

  let commits: CommitInfo[] = [];
  let selectedSha = '';
  let commitYaml = '';
  let loading = false;
  let error = '';
  let unavailable = false; // git tracking off in the controller

  async function loadHistory(av: string, k: string, n: string): Promise<void> {
    commits = [];
    selectedSha = '';
    commitYaml = '';
    error = '';
    unavailable = false;
    if (!av || !k || !n) return;
    loading = true;
    try {
      const resp = await repo.history({ apiVersion: av, kind: k, name: n });
      commits = resp.commits ?? [];
    } catch {
      // FailedPrecondition (git disabled) or any error → show the unavailable
      // note rather than a scary error; history is a nice-to-have panel.
      unavailable = true;
    } finally {
      loading = false;
    }
  }

  async function loadCommit(sha: string): Promise<void> {
    commitYaml = '';
    error = '';
    if (!sha) return;
    try {
      const resp = await repo.atCommit({ apiVersion, kind, name: resourceName }, sha);
      commitYaml = resp.existed
        ? (resp.yaml ?? '')
        : '# (this resource did not exist at the selected commit)\n';
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  // Reload history when the resource identity changes.
  $: void loadHistory(apiVersion, kind, resourceName);
  // Fetch the selected commit's manifest.
  $: void loadCommit(selectedSha);

  function commitLabel(c: CommitInfo): string {
    const short = (c.sha ?? '').slice(0, 8);
    const when = c.committedAt ? new Date(c.committedAt).toLocaleString() : '';
    const subject = c.subject ?? '';
    return `${short} · ${subject}${when ? ` · ${when}` : ''}`;
  }
</script>

<article class="card">
  <h3>History</h3>
  {#if unavailable}
    <p class="muted small">
      Git history is unavailable. Enable <code>manifests.git</code> in the
      controller config to track and diff manifest revisions.
    </p>
  {:else if loading}
    <p class="muted small">Loading history…</p>
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
    {#if error}
      <p class="error small">{error}</p>
    {/if}
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
