<script lang="ts">
  import { onMount } from 'svelte';
  import { ApiError } from '../lib/api';
  import { login } from '../lib/auth';

  let token = '';
  let displayName = defaultDeviceLabel();
  let busy = false;
  let error = '';
  let tokenInput: HTMLInputElement;

  onMount(() => {
    // Programmatic focus instead of HTML autofocus — single input, clear
    // intent, and avoids Svelte's a11y_autofocus warning.
    tokenInput?.focus();
  });

  function defaultDeviceLabel(): string {
    const ua = navigator.userAgent;
    if (ua.includes('Macintosh')) return 'macOS browser';
    if (ua.includes('Windows')) return 'Windows browser';
    if (ua.includes('Linux')) return 'Linux browser';
    return 'browser';
  }

  async function submit() {
    if (!token.trim()) {
      error = 'Token is required.';
      return;
    }
    busy = true;
    error = '';
    try {
      await login(token.trim(), displayName.trim() || 'browser');
      token = '';
    } catch (err) {
      if (err instanceof ApiError) {
        error =
          err.status === 401
            ? 'Invalid token. Check ~/.openctl/controller/token on the controller host.'
            : `Login failed: ${err.message}`;
      } else {
        error = `Login failed: ${err instanceof Error ? err.message : String(err)}`;
      }
    } finally {
      busy = false;
    }
  }
</script>

<main class="login">
  <div class="card">
    <h1>openctl</h1>
    <p class="hint">
      Paste the controller's install-time bearer token to start a session.
      Find it on the controller host at
      <code>~/.openctl/controller/token</code>.
    </p>
    <form on:submit|preventDefault={submit}>
      <label>
        Bearer token
        <input
          type="password"
          bind:value={token}
          bind:this={tokenInput}
          autocomplete="off"
          spellcheck="false"
          placeholder="Paste token"
        />
      </label>
      <label>
        Device label <span class="opt">(optional)</span>
        <input type="text" bind:value={displayName} spellcheck="false" />
      </label>
      {#if error}
        <p class="err" role="alert">{error}</p>
      {/if}
      <button type="submit" disabled={busy}>
        {busy ? 'Signing in…' : 'Sign in'}
      </button>
    </form>
  </div>
</main>

<style>
  .login {
    display: grid;
    place-items: center;
    min-height: 100vh;
    padding: 1rem;
  }
  .card {
    width: 100%;
    max-width: 28rem;
    padding: 2rem;
    border-radius: 12px;
    background: #232323;
    box-shadow: 0 4px 24px rgba(0, 0, 0, 0.3);
  }
  @media (prefers-color-scheme: light) {
    .card {
      background: #fff;
      box-shadow: 0 4px 24px rgba(0, 0, 0, 0.08);
    }
  }
  h1 {
    margin: 0 0 0.25rem;
    font-size: 1.5rem;
  }
  .hint {
    color: #888;
    margin: 0 0 1.5rem;
    font-size: 0.9rem;
  }
  code {
    background: rgba(127, 127, 127, 0.15);
    padding: 0 0.25em;
    border-radius: 3px;
  }
  form {
    display: grid;
    gap: 1rem;
  }
  label {
    display: grid;
    gap: 0.35rem;
    font-size: 0.9rem;
    color: #aaa;
  }
  .opt {
    color: #666;
    font-size: 0.85em;
  }
  .err {
    margin: 0;
    color: #f57171;
    font-size: 0.9rem;
  }
  button {
    margin-top: 0.5rem;
    background: #4a8ef0;
    color: #fff;
    font-weight: 600;
    border-color: #4a8ef0;
  }
  button:hover {
    background: #3a7ee0;
  }
</style>
