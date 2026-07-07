<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { ApiError, repo, type RepoStatus } from '../lib/api';

  // Poll repo status — there's no Watch RPC for repo state, and the
  // signal-to-noise of a stream would be low (status changes on apply
  // commits or operator push, not continuously). 10s is fast enough to
  // feel live, slow enough to be cheap.
  const POLL_MS = 10_000;

  let status: RepoStatus | null = null;
  let error = '';
  let pushing = false;
  let pushMsg = '';
  let pulling = false;
  let pullMsg = '';
  let timer: number | undefined;

  onMount(async () => {
    await refresh();
    timer = setInterval(refresh, POLL_MS) as unknown as number;
  });

  onDestroy(() => {
    if (timer !== undefined) clearInterval(timer);
  });

  async function refresh() {
    try {
      status = await repo.status();
      error = '';
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        // Older controller without RepoService — hide quietly.
        status = { enabled: false };
        error = '';
        return;
      }
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function doPush() {
    if (pushing) return;
    pushing = true;
    pushMsg = '';
    try {
      const r = await repo.push();
      pushMsg = r.message ?? 'pushed';
      await refresh();
    } catch (err) {
      pushMsg = err instanceof Error ? err.message : String(err);
    } finally {
      pushing = false;
    }
  }

  async function doPull() {
    if (pulling) return;
    pulling = true;
    pullMsg = '';
    try {
      const r = await repo.pull();
      pullMsg = r.message ?? 'pulled';
      await refresh();
    } catch (err) {
      pullMsg = err instanceof Error ? err.message : String(err);
    } finally {
      pulling = false;
    }
  }

  // Pill tone: clean+up-to-date is good, dirty is warn, behind is warn,
  // ahead is info (no colour change), error is bad.
  function tone(s: RepoStatus | null, err: string): 'good' | 'warn' | 'bad' | 'muted' {
    if (err) return 'bad';
    if (!s || !s.enabled) return 'muted';
    if (!s.clean) return 'warn';
    if ((s.behind ?? 0) > 0) return 'warn';
    return 'good';
  }

  function label(s: RepoStatus | null): string {
    if (!s) return 'git: …';
    if (!s.enabled) return 'git: off';
    if (!s.clean) return `git: dirty (${s.dirtyPaths?.length ?? 0})`;
    const ahead = s.ahead ?? 0;
    const behind = s.behind ?? 0;
    if (ahead > 0 && behind > 0) return `git: ↑${ahead} ↓${behind}`;
    if (ahead > 0) return `git: ↑${ahead}`;
    if (behind > 0) return `git: ↓${behind}`;
    return 'git: clean';
  }

  // "Push now" only shows when there's a remote and something local to
  // push. When push_mode is 'manual', the button always shows (point of
  // the mode), even if there's nothing to send — the server will return
  // a friendly "nothing to push" message.
  $: showPush =
    !!status?.enabled &&
    !!status?.remote &&
    ((status?.ahead ?? 0) > 0 || status?.pushMode === 'manual');

  // "Pull" shows whenever a remote is configured — it's how an operator picks
  // up out-of-band commits (another controller, a manual git push to the
  // remote). Highlighted when the local is behind.
  $: showPull = !!status?.enabled && !!status?.remote;
</script>

<span
  class="pill tone-{tone(status, error)}"
  title={error || (status?.dirtyPaths?.length
    ? status.dirtyPaths.join('\n')
    : `${status?.branch ?? ''} @ ${status?.headSha ?? ''}\n${status?.dir ?? ''}`)}
>
  {error ? 'git: error' : label(status)}
</span>

{#if showPull}
  <button class="git-action" class:behind={(status?.behind ?? 0) > 0} on:click={doPull} disabled={pulling} title={pullMsg}>
    {pulling ? 'Pulling…' : 'Pull'}
  </button>
{/if}

{#if showPush}
  <button class="git-action" on:click={doPush} disabled={pushing} title={pushMsg}>
    {pushing ? 'Pushing…' : 'Push now'}
  </button>
{/if}

<style>
  .pill {
    display: inline-block;
    font-size: 0.75rem;
    font-family: ui-monospace, SFMono-Regular, monospace;
    padding: 0.15em 0.7em;
    border-radius: 999px;
    border: 1px solid transparent;
    cursor: help;
  }
  .tone-good { background: rgba(46, 160, 67, 0.15); color: #5fdb78; }
  .tone-warn { background: rgba(255, 184, 0, 0.15); color: #ffce4d; }
  .tone-bad  { background: rgba(248, 81, 73, 0.15); color: #ff8980; }
  .tone-muted { background: rgba(127, 127, 127, 0.12); color: #888; }
  .git-action {
    font-size: 0.8rem;
    padding: 0.25em 0.75em;
  }
  .git-action.behind {
    border-color: rgba(255, 184, 0, 0.5);
    color: #ffce4d;
  }
</style>
