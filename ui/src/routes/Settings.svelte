<script lang="ts">
  // Controller settings: edit the controller-behavior tunables (drift
  // reconciler + operation retention) in ~/.openctl/config.yaml. Every value
  // here is read once at controller startup, so a change only takes effect
  // after the controller restarts — hence the persistent banner.

  import { onMount } from 'svelte';
  import {
    controllerConfigApi,
    UnauthorizedError,
    type ControllerConfig,
  } from '../lib/api';

  let loading = true;
  let loadError = '';
  let saving = false;
  let saveError = '';
  let saved = false;
  let restartRequired = true;

  // Form model. Retention is edited as a string so an empty field maps to
  // "use the default" rather than a literal 0.
  let form = { reconcilerEnabled: true, reconcilerInterval: '', retain: '' };

  function applyConfig(c: ControllerConfig | undefined) {
    form = {
      reconcilerEnabled: c?.reconcilerEnabled ?? true,
      reconcilerInterval: c?.reconcilerInterval ?? '',
      retain: c?.opRetainPerResource != null ? String(c.opRetainPerResource) : '',
    };
  }

  async function refresh() {
    loading = true;
    loadError = '';
    try {
      const resp = await controllerConfigApi.get();
      applyConfig(resp.config);
      restartRequired = resp.restartRequired ?? true;
    } catch (err) {
      if (err instanceof UnauthorizedError) return;
      loadError = err instanceof Error ? err.message : String(err);
    } finally {
      loading = false;
    }
  }

  onMount(refresh);

  // Clear the "saved" flash whenever the user edits a field again.
  function touched() {
    saved = false;
  }

  async function save() {
    saving = true;
    saveError = '';
    saved = false;
    const retainNum = form.retain.trim() === '' ? 0 : Number(form.retain);
    if (!Number.isInteger(retainNum) || retainNum < 0) {
      saveError = 'Retention must be a non-negative whole number (blank = default).';
      saving = false;
      return;
    }
    try {
      const resp = await controllerConfigApi.update({
        reconcilerEnabled: form.reconcilerEnabled,
        reconcilerInterval: form.reconcilerInterval.trim(),
        opRetainPerResource: retainNum,
      });
      applyConfig(resp.config); // reflect server-side defaulting
      restartRequired = resp.restartRequired ?? true;
      saved = true;
    } catch (err) {
      if (err instanceof UnauthorizedError) return;
      saveError = err instanceof Error ? err.message : String(err);
    } finally {
      saving = false;
    }
  }
</script>

<section>
  <header>
    <div>
      <h2>Controller Settings</h2>
      <p class="lede">Tune the controller's background behavior in <code>~/.openctl/config.yaml</code>.</p>
    </div>
  </header>

  {#if loading}
    <p class="muted">Loading…</p>
  {:else if loadError}
    <p class="err">{loadError}</p>
  {:else}
    {#if restartRequired}
      <p class="banner">
        These settings are read when the controller starts. Saving updates
        <code>config.yaml</code>, but the change takes effect only after the
        controller is restarted.
      </p>
    {/if}

    <article class="form-card">
      <h3>Drift reconciler</h3>
      <div class="row check">
        <label class="inline" for="rec-enabled">
          <input id="rec-enabled" type="checkbox" bind:checked={form.reconcilerEnabled} on:change={touched} />
          Enabled
        </label>
        <p class="hint">The periodic pass that recomputes drift for every managed resource.</p>
      </div>
      <div class="row">
        <label for="rec-interval">Interval</label>
        <input
          id="rec-interval"
          type="text"
          placeholder="5m"
          bind:value={form.reconcilerInterval}
          on:input={touched}
          disabled={!form.reconcilerEnabled}
        />
        <p class="hint">Go duration between passes (e.g. <code>30s</code>, <code>5m</code>, <code>1h</code>). Blank uses the default <code>5m</code>.</p>
      </div>

      <h3 class="section">Operations</h3>
      <div class="row">
        <label for="op-retain">Retain per resource</label>
        <input
          id="op-retain"
          type="number"
          min="0"
          placeholder="50"
          bind:value={form.retain}
          on:input={touched}
        />
        <p class="hint">Completed operation rows kept per resource before older ones are pruned. Blank uses the default <code>50</code>.</p>
      </div>

      {#if saveError}
        <p class="err">{saveError}</p>
      {/if}
      {#if saved}
        <p class="ok">Saved to config.yaml. Restart the controller to apply.</p>
      {/if}
      <div class="actions">
        <button class="primary" disabled={saving} on:click={save}>
          {saving ? 'Saving…' : 'Save'}
        </button>
        <button on:click={refresh} disabled={saving}>Reset</button>
      </div>
    </article>
  {/if}
</section>

<style>
  section {
    max-width: 64rem;
  }
  header {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 1rem;
    margin-bottom: 1.25rem;
  }
  h2 {
    margin: 0;
    font-size: 1.25rem;
  }
  h3 {
    margin: 0 0 0.75rem;
    font-size: 0.95rem;
  }
  h3.section {
    margin-top: 0.5rem;
    padding-top: 0.85rem;
    border-top: 1px solid rgba(127, 127, 127, 0.18);
  }
  .lede {
    color: #888;
    margin: 0.3rem 0 0;
    font-size: 0.9rem;
  }
  .muted {
    color: #888;
  }
  .err {
    color: #f57171;
  }
  .ok {
    color: #5fdb78;
    font-size: 0.85rem;
    margin: 0;
  }
  .banner {
    background: rgba(255, 184, 0, 0.1);
    border: 1px solid rgba(255, 184, 0, 0.35);
    color: #d9b552;
    border-radius: 6px;
    padding: 0.7rem 0.9rem;
    font-size: 0.85rem;
    margin: 0 0 1.25rem;
    max-width: 40rem;
  }
  .form-card {
    background: rgba(127, 127, 127, 0.06);
    border: 1px solid rgba(127, 127, 127, 0.2);
    border-radius: 6px;
    padding: 1.25rem 1.5rem;
    display: flex;
    flex-direction: column;
    gap: 0.85rem;
    max-width: 40rem;
  }
  .row {
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
  }
  label {
    font-size: 0.85rem;
    color: #aaa;
    font-family: ui-monospace, SFMono-Regular, monospace;
  }
  label.inline {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    cursor: pointer;
  }
  .row.check input {
    width: auto;
  }
  input {
    padding: 0.4rem 0.6rem;
    background: rgba(0, 0, 0, 0.2);
    color: inherit;
    border: 1px solid rgba(127, 127, 127, 0.25);
    border-radius: 4px;
    font-size: 0.9rem;
    font-family: inherit;
  }
  input:disabled { opacity: 0.5; }
  @media (prefers-color-scheme: light) {
    input {
      background: #fff;
      border-color: #ccc;
    }
  }
  .hint {
    color: #888;
    font-size: 0.75rem;
    margin: 0;
  }
  .actions {
    display: flex;
    gap: 0.5rem;
    margin-top: 0.25rem;
  }
  .primary {
    background: #4a8ef0;
    color: white;
    border-color: #4a8ef0;
    padding: 0.4em 1em;
    border-radius: 6px;
    font-size: 0.9rem;
    font-weight: 500;
    cursor: pointer;
  }
  .primary:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
  code {
    background: rgba(127, 127, 127, 0.15);
    padding: 0 0.3em;
    border-radius: 3px;
    font-family: ui-monospace, SFMono-Regular, monospace;
  }
</style>
