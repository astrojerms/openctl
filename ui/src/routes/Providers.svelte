<script lang="ts">
  // Providers: list + add + edit + delete for provider credentials in
  // ~/.openctl/config.yaml. Scope covers the single-context/single-
  // credential-per-provider common case; multi-context configs are
  // still editable by hand-editing the file (this UI would clobber
  // them on save — worth flagging in the readme when we add one).

  import { onMount } from 'svelte';
  import {
    providersApi,
    UnauthorizedError,
    type ProviderEntry,
  } from '../lib/api';

  let entries: ProviderEntry[] = [];
  let loading = true;
  let loadError = '';

  // Edit form state — one form at a time, either "add new" or
  // "editing existing by name". Empty editing = closed.
  let editing = '';
  let form = { name: '', endpoint: '', tokenId: '', tokenSecret: '' };
  let saveError = '';
  let saving = false;

  async function refresh() {
    loading = true;
    loadError = '';
    try {
      const resp = await providersApi.list();
      entries = (resp.providers ?? []).slice().sort((a, b) => a.name.localeCompare(b.name));
    } catch (err) {
      if (err instanceof UnauthorizedError) return;
      loadError = err instanceof Error ? err.message : String(err);
    } finally {
      loading = false;
    }
  }

  onMount(refresh);

  function openNew() {
    editing = '__new__';
    form = { name: '', endpoint: '', tokenId: '', tokenSecret: '' };
    saveError = '';
  }

  function openEdit(e: ProviderEntry) {
    editing = e.name;
    form = {
      name: e.name,
      endpoint: e.endpoint ?? '',
      tokenId: e.tokenId ?? '',
      tokenSecret: '',
    };
    saveError = '';
  }

  function closeForm() {
    editing = '';
    saveError = '';
  }

  async function save() {
    if (!form.name) {
      saveError = 'Name is required.';
      return;
    }
    saving = true;
    saveError = '';
    try {
      await providersApi.upsert({
        name: form.name,
        endpoint: form.endpoint,
        tokenId: form.tokenId,
        tokenSecret: form.tokenSecret,
      });
      await refresh();
      editing = '';
    } catch (err) {
      if (err instanceof UnauthorizedError) return;
      saveError = err instanceof Error ? err.message : String(err);
    } finally {
      saving = false;
    }
  }

  async function del(name: string) {
    if (!confirm(`Delete provider ${name} from config.yaml?`)) return;
    try {
      await providersApi.delete(name);
      await refresh();
    } catch (err) {
      if (err instanceof UnauthorizedError) return;
      loadError = err instanceof Error ? err.message : String(err);
    }
  }
</script>

<section>
  <header>
    <div>
      <h2>Provider Credentials</h2>
      <p class="lede">Edit provider endpoints and API tokens in <code>~/.openctl/config.yaml</code>.</p>
    </div>
    <button class="primary" on:click={openNew}>+ Add provider</button>
  </header>

  {#if loading}
    <p class="muted">Loading…</p>
  {:else if loadError}
    <p class="err">{loadError}</p>
  {:else}
    {#if entries.length === 0}
      <p class="muted">No providers configured yet. Click <strong>Add provider</strong> to configure one.</p>
    {:else}
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Endpoint</th>
            <th>Token ID</th>
            <th>Secret</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {#each entries as e (e.name)}
            <tr>
              <td class="mono">{e.name}</td>
              <td class="mono truncate">{e.endpoint ?? ''}</td>
              <td class="mono truncate">{e.tokenId ?? ''}</td>
              <td>
                {#if e.hasSecret}
                  {#if e.usesSecretFile}
                    <span class="pill pill-file">file</span>
                  {:else}
                    <span class="pill pill-inline">on file</span>
                  {/if}
                {:else}
                  <span class="pill pill-missing">missing</span>
                {/if}
              </td>
              <td class="right">
                <button class="link" on:click={() => openEdit(e)}>Edit</button>
                <button class="link danger" on:click={() => del(e.name)}>Delete</button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}

    {#if editing}
      <article class="form-card">
        <h3>{editing === '__new__' ? 'Add provider' : `Edit ${editing}`}</h3>
        <div class="row">
          <label for="prov-name">Name</label>
          <input
            id="prov-name"
            type="text"
            placeholder="proxmox"
            bind:value={form.name}
            disabled={editing !== '__new__'}
          />
          <p class="hint">Matches the provider prefix in resource apiVersions (e.g. <code>proxmox</code> for <code>proxmox.openctl.io/v1</code>).</p>
        </div>
        <div class="row">
          <label for="prov-endpoint">Endpoint</label>
          <input
            id="prov-endpoint"
            type="text"
            placeholder="https://proxmox.lan:8006"
            bind:value={form.endpoint}
          />
        </div>
        <div class="row">
          <label for="prov-token-id">Token ID</label>
          <input
            id="prov-token-id"
            type="text"
            placeholder="user@pam!token-name"
            bind:value={form.tokenId}
          />
        </div>
        <div class="row">
          <label for="prov-token-secret">Token Secret</label>
          <input
            id="prov-token-secret"
            type="password"
            placeholder={editing === '__new__' ? 'Required' : 'Leave blank to keep existing'}
            bind:value={form.tokenSecret}
          />
          <p class="hint">
            {#if editing !== '__new__'}Leave blank to preserve the current secret.{/if}
            Stored inline in <code>config.yaml</code>. For file-backed secrets, edit the config manually with <code>tokenSecretFile:</code>.
          </p>
        </div>
        {#if saveError}
          <p class="err">{saveError}</p>
        {/if}
        <div class="actions">
          <button class="primary" disabled={saving} on:click={save}>
            {saving ? 'Saving…' : 'Save'}
          </button>
          <button on:click={closeForm} disabled={saving}>Cancel</button>
        </div>
      </article>
    {/if}
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
  table {
    width: 100%;
    border-collapse: collapse;
    margin-bottom: 1.25rem;
  }
  th, td {
    text-align: left;
    padding: 0.55rem 0.75rem;
    border-bottom: 1px solid rgba(127, 127, 127, 0.18);
    font-size: 0.9rem;
  }
  th {
    font-weight: 600;
    color: #888;
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .mono {
    font-family: ui-monospace, SFMono-Regular, monospace;
  }
  .truncate {
    max-width: 20rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .right { text-align: right; }
  .link {
    background: transparent;
    border: none;
    color: #6ea8ff;
    cursor: pointer;
    font-size: 0.85rem;
    padding: 0 0.4em;
  }
  .link:hover { text-decoration: underline; }
  .link.danger { color: #ff8980; }
  .pill {
    display: inline-block;
    padding: 0.05em 0.55em;
    border-radius: 999px;
    font-size: 0.75rem;
  }
  .pill-inline { background: rgba(46, 160, 67, 0.18); color: #5fdb78; }
  .pill-file { background: rgba(255, 184, 0, 0.18); color: #ffce4d; }
  .pill-missing { background: rgba(248, 81, 73, 0.18); color: #ff8980; }
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
  input {
    padding: 0.4rem 0.6rem;
    background: rgba(0, 0, 0, 0.2);
    color: inherit;
    border: 1px solid rgba(127, 127, 127, 0.25);
    border-radius: 4px;
    font-size: 0.9rem;
    font-family: inherit;
  }
  input:disabled { opacity: 0.6; }
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
