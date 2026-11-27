<script lang="ts">
  // Saved credentials/trust are startup-bound, never proof of active connectivity.
  import { onDestroy, onMount } from 'svelte';
  import { get } from 'svelte/store';
  import { providersApi, UnauthorizedError, type ProviderEntry } from '../lib/api';
  import { auth } from '../lib/auth';
  import { routeHref } from '../lib/router';

  $: isAdmin = $auth.kind === 'signed-in' && (!$auth.me.role || $auth.me.role === 'admin');
  let entries: ProviderEntry[] = [];
  let loading = true;
  let loadError = '';
  let editing = '';
  let form = emptyForm();
  let saveError = '';
  let saving = false;
  let deleting = '';
  let restartMessage = '';
  let editRevision = 0;
  let loadRevision = 0;
  let active = true;
  const controller = new AbortController();

  function emptyForm() {
    return { name: '', endpoint: '', tokenId: '', tokenSecret: '', caFile: '', insecureSkipVerify: false };
  }
  function canAdminister() {
    const state = get(auth);
    return active && state.kind === 'signed-in' && (!state.me.role || state.me.role === 'admin');
  }
  function closeForm() {
    editRevision++;
    editing = '';
    form = emptyForm();
    saveError = '';
  }
  const unsubscribe = auth.subscribe((state) => {
    if (state.kind !== 'signed-in' || (state.me.role && state.me.role !== 'admin')) closeForm();
  });
  function unauthorized(error: unknown) {
    if (!(error instanceof UnauthorizedError)) return false;
    closeForm();
    auth.set({ kind: 'signed-out' });
    return true;
  }
  async function refresh() {
    const revision = ++loadRevision;
    loading = true;
    loadError = '';
    try {
      const response = await providersApi.list({ signal: controller.signal });
      if (!active || revision !== loadRevision) return;
      entries = (response.providers ?? []).slice().sort((a, b) => a.name.localeCompare(b.name));
    } catch (error) {
      if (!active || revision !== loadRevision || unauthorized(error)) return;
      loadError = error instanceof Error ? error.message : String(error);
    } finally {
      if (active && revision === loadRevision) loading = false;
    }
  }
  onMount(() => { void refresh(); });
  onDestroy(() => {
    active = false;
    controller.abort();
    unsubscribe();
    closeForm();
    entries = [];
  });

  function openNew() {
    if (!canAdminister() || saving || deleting) return;
    closeForm();
    editing = '__new__';
  }
  function openEdit(entry: ProviderEntry) {
    if (!canAdminister() || saving || deleting) return;
    closeForm();
    editing = entry.name;
    form = {
      name: entry.name, endpoint: entry.endpoint ?? '', tokenId: entry.tokenId ?? '',
      tokenSecret: '', caFile: entry.caFile ?? '', insecureSkipVerify: entry.insecureSkipVerify ?? false,
    };
  }
  async function save() {
    if (!canAdminister() || saving || deleting || !editing) return;
    if (!form.name) { saveError = 'Name is required.'; return; }
    if (form.caFile && form.insecureSkipVerify) {
      saveError = 'Choose a CA file or explicit insecure verification, not both.';
      return;
    }
    if (form.name === 'proxmox') {
      try {
        const endpoint = new URL(form.endpoint);
        if (endpoint.protocol !== 'https:' || !endpoint.hostname || endpoint.username || endpoint.password || endpoint.search || endpoint.hash) throw new Error();
      } catch {
        saveError = 'Proxmox requires an HTTPS endpoint without embedded credentials, query or fragment.';
        return;
      }
    }
    const request = { ...form };
    const revision = editRevision;
    saving = true;
    saveError = '';
    try {
      await providersApi.upsert(request, { signal: controller.signal });
      if (!active || revision !== editRevision || !canAdminister()) return;
      restartMessage = 'Provider configuration saved. Restart the controller before testing or discovering with these credentials and trust settings.';
      closeForm();
      await refresh();
    } catch (error) {
      if (!active || revision !== editRevision || unauthorized(error)) return;
      saveError = error instanceof Error ? error.message : String(error);
    } finally {
      if (active) saving = false;
    }
  }
  async function del(name: string) {
    if (!canAdminister() || saving || deleting) return;
    if (!confirm(`Delete provider ${name} from config.yaml?`) || !canAdminister()) return;
    deleting = name;
    try {
      await providersApi.delete(name, { signal: controller.signal });
      if (!active || !canAdminister()) return;
      restartMessage = 'Provider configuration removed. Restart the controller to update its active providers.';
      closeForm();
      await refresh();
    } catch (error) {
      if (!active || unauthorized(error)) return;
      loadError = error instanceof Error ? error.message : String(error);
    } finally {
      if (active) deleting = '';
    }
  }
</script>

<section>
  <header>
    <div>
      <h2>Provider Credentials</h2>
      <p class="lede">Edit provider endpoints and API tokens in <code>~/.openctl/config.yaml</code>.</p>
    </div>
    {#if isAdmin}<button class="primary" on:click={openNew} disabled={saving || !!deleting}>+ Add provider</button>{/if}
  </header>

  <p class="muted">Configuration changes require a controller restart. Credential hot reload is intentionally disabled because changing live provider identities is unsafe. Discovery uses the currently running configuration.</p>
  <p class="muted">Native Proxmox requires HTTPS and verifies the server certificate and hostname by default. For a private CA, install its PEM bundle on the controller host and set its path below. Changing trust does not test the endpoint or hot-reload existing connections.</p>
  <p><a class="link" href={routeHref({ name: 'discover' })}>Test connections and discover Proxmox VMs →</a></p>
  {#if restartMessage}<p role="status">{restartMessage}</p>{/if}
  {#if !isAdmin}<p class="muted">Provider configuration requires an admin role.</p>{/if}

  {#if loading}
    <p class="muted">Loading…</p>
  {:else if loadError}
    <p class="err">{loadError}</p>
  {:else}
    {#if entries.length === 0}
      <p class="muted">No providers configured yet. Click <strong>Add provider</strong> to configure one.</p>
    {:else}
      <div class="table-scroll">
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Endpoint</th>
            <th>Token ID</th>
            <th>Secret</th>
            <th>Certificate policy (saved)</th>
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
              <td>
                {#if e.name === 'proxmox'}
                  {#if e.insecureSkipVerify}<strong class="err">Verification disabled — insecure</strong>
                  {:else if e.caFile}<span title={e.caFile}>System roots + CA file: <code>{e.caFile}</code></span>
                  {:else}System roots + hostname verification{/if}
                {:else}Provider-specific{/if}
              </td>
              <td class="right">
                {#if isAdmin}
                  <button class="link" on:click={() => openEdit(e)} disabled={saving || !!deleting}>Edit</button>
                  <button class="link danger" on:click={() => del(e.name)} disabled={saving || !!deleting}>{deleting === e.name ? 'Removing…' : 'Delete'}</button>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
      </div>
    {/if}

    {#if editing && isAdmin}
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
        {#if form.name === 'proxmox'}
          <div class="row">
            <label for="prov-ca-file">CA bundle file (controller host)</label>
            <input id="prov-ca-file" type="text" bind:value={form.caFile} placeholder="/etc/openctl/proxmox-ca.pem" />
            <p class="hint">PEM trust bundle, read by the controller/plugin at startup. Obtain and verify it out of band; this is not a browser file upload. Empty uses system trust.</p>
          </div>
          <label>
            <input id="prov-insecure" type="checkbox" bind:checked={form.insecureSkipVerify} />
            Disable certificate verification (insecure HTTPS only)
          </label>
          {#if form.insecureSkipVerify}
            <p class="err" role="alert">Certificate and hostname verification are disabled. An impersonated Proxmox server can steal API credentials and control infrastructure. Prefer a verified CA bundle. Plain HTTP is never allowed.</p>
          {/if}
        {/if}
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
