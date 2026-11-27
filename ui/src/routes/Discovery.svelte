<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { derived, get } from 'svelte/store';
  import { discovery, ownership, UnauthorizedError, type DiscoveryContext, type DiscoveredResource, type MigrationBundle } from '../lib/api';
  import { auth, type AuthState } from '../lib/auth';
  import { canManageOwnership } from '../lib/authority';
  import { navigate, routeHref } from '../lib/router';
  import { observedDesired, parseMigrationBundle } from '../lib/discovery';
  import { resourceToYAML } from '../lib/yaml';

  type ContextState = { connected: boolean; message: string; failureCode?: string; error?: string; count?: number };
  let contexts: DiscoveryContext[] = [];
  let states: Record<string, ContextState> = {};
  let inventory: DiscoveredResource[] = [];
  let loading = true;
  let busy = false;
  let error = '';
  let reviewed: DiscoveredResource | null = null;
  let confirmed = false;
  let bundle: MigrationBundle | null = null;
  let bundleFilename = '';
  let bundleContext = '';
  let bundleConfirmed = false;
  let importing = false;
  let adoptionError = '';
  let adopting = false;
  let identityMessage = '';
  let active = true;
  let mounted = false;
  let generation = 0;
  let importRevision = 0;
  let requests = new AbortController();

  $: desired = reviewed ? observedDesired(reviewed.resource) : null;
  $: bundlePreview = bundle ? { ...bundle.resource, spec: { ...bundle.resource.spec, context: bundleContext } } : null;
  $: bundleVM = inventory.find((item) => item.context === bundleContext && item.identity === bundle?.identity);

  function message(err: unknown): string { return err instanceof Error ? err.message : String(err); }
  function current(revision: number) { return active && revision === generation; }
  function canManage() { return active && get(auth).kind === 'signed-in' && get(canManageOwnership); }
  function actorKey(state: AuthState) {
    return state.kind === 'signed-in' ? JSON.stringify([state.me.sessionId, state.me.principalId, state.me.userId, state.me.role]) : state.kind;
  }
  function observed(item: DiscoveredResource) { return inventory.includes(item) && states[item.context]?.connected === true; }
  function clearReview() { reviewed = null; confirmed = false; }
  function clearBundle() {
    importRevision++;
    importing = false;
    bundle = null;
    bundleFilename = '';
    bundleConfirmed = false;
  }
  function invalidateContext(context: string) {
    inventory = inventory.filter((item) => item.context !== context);
    if (states[context]) states = { ...states, [context]: { ...states[context], count: undefined } };
    if (reviewed?.context === context) clearReview();
    if (bundleContext === context) bundleConfirmed = false;
  }
  function resetPage() {
    generation++;
    requests.abort();
    if (active) requests = new AbortController();
    clearReview();
    clearBundle();
    contexts = [];
    states = {};
    inventory = [];
    bundleContext = '';
    busy = false;
    adopting = false;
    loading = false;
    error = '';
    adoptionError = '';
    identityMessage = '';
  }
  function handled(err: unknown, revision: number) {
    if (!current(revision)) return true;
    if (!(err instanceof UnauthorizedError)) return false;
    auth.set({ kind: 'signed-out' });
    return true;
  }

  let actor = actorKey(get(auth));
  let ownershipAllowed = get(canManageOwnership);
  const unsubscribe = derived([auth, canManageOwnership], ([session, allowed]) => ({ session, allowed })).subscribe(({ session, allowed }) => {
    const nextActor = actorKey(session);
    const changed = actor !== nextActor || ownershipAllowed !== allowed;
    actor = nextActor;
    ownershipAllowed = allowed;
    if (!changed) return;
    if (mounted && session.kind === 'signed-in') void loadContexts();
    else resetPage();
  });
  onMount(() => { mounted = true; void loadContexts(); });
  onDestroy(() => {
    active = false;
    unsubscribe();
    resetPage();
  });

  async function loadContexts() {
    if (!active) return;
    resetPage();
    const revision = generation;
    loading = true;
    try {
      const response = await discovery.contexts(undefined, { signal: requests.signal });
      if (!current(revision)) return;
      contexts = response.contexts ?? [];
      bundleContext = contexts.find((context) => context.defaultContext)?.name ?? contexts[0]?.name ?? '';
    } catch (err) {
      if (!handled(err, revision)) error = message(err);
    } finally { if (current(revision)) loading = false; }
  }

  async function test(context: string, revision: number): Promise<boolean> {
    if (!current(revision)) return false;
    try {
      const response = await discovery.test(context, undefined, { signal: requests.signal });
      if (!current(revision)) return false;
      const connected = response.connected === true;
      states = { ...states, [context]: { connected, message: response.message || (connected ? 'Connected' : 'Connection failed'), failureCode: connected ? undefined : response.failureCode } };
      return connected;
    } catch (err) {
      if (!handled(err, revision)) states = { ...states, [context]: { connected: false, message: 'Connection test failed', error: message(err) } };
      return false;
    }
  }

  async function testOne(context: string) {
    if (!active || loading || busy || adopting || !contexts.some((item) => item.name === context)) return;
    const revision = generation;
    invalidateContext(context);
    states = { ...states, [context]: { connected: false, message: 'Testing…' } };
    busy = true;
    try { await test(context, revision); } finally { if (current(revision)) busy = false; }
  }

  async function discoverContext(context: string, revision: number) {
    if (!await test(context, revision) || !current(revision)) return;
    states = { ...states, [context]: { ...states[context], message: 'Discovering VMs…' } };
    try {
      const response = await discovery.inventory(context, undefined, { signal: requests.signal });
      if (!current(revision)) return;
      const found = response.resources ?? [];
      if (found.some((item) => item.context !== context)) throw new Error('The controller returned inventory for a different context. No rows were accepted; inspect provider routing before discovering again.');
      inventory = [...inventory, ...found].sort((a, b) => a.context.localeCompare(b.context) || a.node.localeCompare(b.node) || Number(a.vmid) - Number(b.vmid));
      states = { ...states, [context]: { ...states[context], message: 'Inventory observed', count: found.length } };
    } catch (err) {
      if (!handled(err, revision)) states = { ...states, [context]: { ...states[context], message: 'Inventory unavailable', error: message(err) } };
    }
  }

  async function discoverOne(context: string) {
    if (!active || loading || busy || adopting || !contexts.some((item) => item.name === context)) return;
    const revision = generation;
    invalidateContext(context);
    states = { ...states, [context]: { connected: false, message: 'Testing…' } };
    busy = true;
    try { await discoverContext(context, revision); } finally { if (current(revision)) busy = false; }
  }

  async function discoverAll() {
    if (!active || loading || busy || adopting) return;
    const revision = generation;
    busy = true;
    inventory = [];
    states = {};
    clearReview();
    bundleConfirmed = false;
    adoptionError = '';
    try {
      await Promise.all(contexts.map((context) => discoverContext(context.name, revision)));
    } finally { if (current(revision)) busy = false; }
  }

  function review(item: DiscoveredResource) {
    if (!active || loading || busy || adopting || !observed(item)) return;
    reviewed = item;
    confirmed = false;
    adoptionError = '';
    identityMessage = '';
  }

  async function importBundle(event: Event) {
    const input = event.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    input.value = '';
    if (!canManage() || loading || adopting || busy) return;
    clearBundle();
    adoptionError = '';
    if (!file) return;
    const revision = generation;
    const fileRevision = importRevision;
    importing = true;
    try {
      const text = await file.text();
      if (!current(revision) || fileRevision !== importRevision || !canManage()) return;
      bundle = parseMigrationBundle(text);
      bundleFilename = file.name;
    } catch (err) {
      if (current(revision) && fileRevision === importRevision && canManage()) adoptionError = `Bundle validation failed: ${message(err)}`;
    } finally {
      if (current(revision) && fileRevision === importRevision) importing = false;
    }
  }

  async function adopt(fromBundle = false) {
    if (!canManage() || loading || adopting || busy) return;
    const item = fromBundle ? inventory.find((entry) => entry.context === bundleContext && entry.identity === bundle?.identity) : reviewed;
    if (!item || !observed(item) || !item.identity || !item.adoptable || item.managed || item.identityConfirmationRequired) return;
    if (fromBundle ? !bundle || !bundleConfirmed : !confirmed) return;
    const selectedBundle = bundle;
    const revision = generation;
    adopting = true;
    adoptionError = '';
    try {
      const response = fromBundle
        ? await ownership.adoptBundle(selectedBundle!, item.context, { signal: requests.signal })
        : await ownership.adopt(observedDesired(item.resource), item.context, item.identity, { signal: requests.signal });
      if (!current(revision) || !canManage()) return;
      clearReview();
      clearBundle();
      invalidateContext(item.context);
      navigate({ name: 'detail', apiVersion: response.resource.apiVersion, kind: response.resource.kind, resourceName: response.resource.metadata.name });
    } catch (err) {
      if (!handled(err, revision)) {
        invalidateContext(item.context);
        clearReview();
        bundleConfirmed = false;
        adoptionError = message(err);
      }
    } finally { if (current(revision)) adopting = false; }
  }

  async function confirmTrackedIdentity() {
    if (!canManage() || loading || adopting || busy || !confirmed || !reviewed?.identityConfirmationRequired || !reviewed.identity || !observed(reviewed)) return;
    const item = reviewed;
    const revision = generation;
    adopting = true;
    adoptionError = '';
    identityMessage = '';
    let confirmation = '';
    try {
      const response = await ownership.confirmIdentity(
        item.resource.apiVersion, item.resource.kind, item.resource.metadata.name,
        item.identity, item.context, { signal: requests.signal },
      );
      if (current(revision) && canManage()) confirmation = response.message || 'Tracked VM identity confirmed. Desired intent and ownership were not changed.';
    } catch (err) {
      if (!handled(err, revision)) adoptionError = message(err);
    } finally { if (current(revision)) adopting = false; }
    if (confirmation && current(revision) && canManage()) {
      await discoverAll();
      if (current(revision)) identityMessage = confirmation;
    }
  }
</script>

<section>
  <header>
    <div>
      <h2>Discover infrastructure</h2>
      <p class="muted">Choose a Proxmox context to discover existing VMs without scanning unrelated endpoints, then review before adopting.</p>
    </div>
    <a href={routeHref({ name: 'providers' })}>Connect a provider</a>
  </header>
  <p class="notice">Discovery and connection tests are read-only. Adoption records desired intent and ownership; it does not Apply or change the VM. Adoption requires an admin, an idle controller, and manifest GitOps watch/pull disabled. The server verifies these safeguards and live identity.</p>
  <p class="muted">Changed provider credentials or certificate trust? Restart the controller first; hot reload is intentionally disabled for safety.</p>
  <p class="muted">Adoption binds the exact VM fingerprint you reviewed. Testing or rediscovering a context invalidates its earlier inventory and consent, but preserves other contexts. Session or authority changes clear private reviews and bundles. Stopping a request does not roll back tracking already accepted by the controller.</p>
  {#if loading}
    <p class="muted">Loading configured contexts…</p>
  {:else if error}
    <p class="err" role="alert">{error}</p>
    <button on:click={loadContexts}>Retry loading contexts</button>
  {:else if contexts.length === 0}
    <p>No active Proxmox contexts. <a href={routeHref({ name: 'providers' })}>Configure a provider</a>, then restart the controller.</p>
  {:else}
    <article class="card">
      <h3>Connections</h3>
      <div class="table-scroll">
        <table>
          <thead><tr><th>Context</th><th>Endpoint</th><th>Connection / inventory</th><th></th></tr></thead>
          <tbody>
            {#each contexts as context (context.name)}
              <tr>
                <td><code>{context.name}</code>{#if context.defaultContext} (default){/if}</td>
                <td>{context.endpoint || 'Not reported'}</td>
                <td>
                  {states[context.name]?.message ?? 'Not tested'}
                  {#if states[context.name]?.failureCode}<p class="err"><code>{states[context.name].failureCode}</code></p>{/if}
                  {#if states[context.name]?.count !== undefined} · {states[context.name].count} VMs{/if}
                  {#if states[context.name]?.error}<p class="err">{states[context.name].error}</p>{/if}
                </td>
                <td>
                  <button disabled={busy || adopting} on:click={() => testOne(context.name)}>Test connection</button>
                  <button aria-label={`Discover VMs in ${context.name}`} disabled={busy || adopting} on:click={() => discoverOne(context.name)}>Discover VMs</button>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      <button class="primary" disabled={busy || adopting} on:click={discoverAll}>{busy ? 'Testing / discovering…' : 'Test connections and discover all VMs'}</button>
    </article>

    {#if inventory.length > 0}
      <article class="card">
        <h3>Observed VMs ({inventory.length})</h3>
        <div class="table-scroll">
          <table>
            <thead><tr><th>Name</th><th>Context</th><th>Node</th><th>VMID</th><th>Management</th><th>Adoptability / blocking reason</th><th></th></tr></thead>
            <tbody>
              {#each inventory as item (`${item.context}/${item.node}/${item.vmid}`)}
                <tr>
                  <td>{item.resource.metadata.name}</td><td><code>{item.context}</code></td><td><code>{item.node}</code></td><td><code>{item.vmid}</code></td>
                  <td>{item.identityConfirmationRequired ? 'Tracked — identity unconfirmed' : item.managed ? 'Managed' : 'Unmanaged'}</td>
                  <td>{item.identityConfirmationRequired ? 'Identity confirmation required' : item.adoptable && !item.managed ? 'Adoptable' : 'Blocked'}{#if item.reason}<p>{item.reason}</p>{/if}</td>
                  <td><button disabled={busy || adopting} on:click={() => review(item)}>Review</button></td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </article>
    {/if}

    {#if reviewed && desired}
      <article class="card">
        <h3>{reviewed.identityConfirmationRequired ? 'Confirm tracked VM identity' : 'Review observed desired manifest'}</h3>
        <p>Authoritative location: context <code>{reviewed.context}</code> · node <code>{reviewed.node}</code> · VMID <code>{reviewed.vmid}</code></p>
        <p>Live identity: <code>{reviewed.identity || 'Unavailable'}</code></p>
        <p>{reviewed.identityConfirmationRequired ? 'Tracked — immutable identity not yet confirmed' : reviewed.managed ? 'Managed' : 'Unmanaged'} · {reviewed.identityConfirmationRequired ? 'Confirmation required, not adoption' : reviewed.adoptable && !reviewed.managed ? 'Adoptable' : 'Adoption blocked'}{#if reviewed.reason}: {reviewed.reason}{/if}</p>
        <p>Resource name: <code>{desired.metadata.name}</code></p>
        {#if reviewed.identityConfirmationRequired}
          <p>This VM is already tracked, but its immutable live identity has not been bound. Verify the context, node, VMID, and fingerprint above against the intended VM before confirming.</p>
          <p class="notice">Confirmation binds tracking only. It does not Apply, change the VM, alter saved desired intent, or transfer ownership. Existing composite ownership remains intact; this is not adoption.</p>
          <p class="muted">Requires an admin, an idle controller, and manifest GitOps watch/pull disabled. The server checks saved placement and the expected live fingerprint before binding identity.</p>
          {#if $canManageOwnership}
            <label class="confirmation"><input type="checkbox" bind:checked={confirmed} disabled={adopting} /> I independently verified that this context, node, VMID, and live fingerprint identify the real VM already tracked by this resource.</label>
            <button class="primary" disabled={adopting || busy || !confirmed || !reviewed.identity} on:click={confirmTrackedIdentity}>{adopting ? 'Confirming…' : 'Confirm tracked VM identity'}</button>
          {:else}<p class="muted">Identity confirmation requires admin access and controller-owned intent with GitOps disabled.</p>{/if}
        {:else}
          <p class="muted">This manifest reflects observed configuration, not unobservable original intent. Review it before taking responsibility for future reconciliation.</p>
          <pre>{resourceToYAML(desired)}</pre>
          {#if $canManageOwnership}
            <label class="confirmation"><input type="checkbox" bind:checked={confirmed} disabled={adopting} /> I reviewed this desired intent, context, node, VMID and live fingerprint, and want this controller to track this exact VM.</label>
            <button class="primary" disabled={adopting || busy || !confirmed || !reviewed.identity || !states[reviewed.context]?.connected || !reviewed.adoptable || reviewed.managed} on:click={() => adopt()}>{adopting ? 'Adopting…' : 'Adopt existing VM'}</button>
          {:else}<p class="muted">Adoption requires admin access and controller-owned intent with GitOps disabled. Discovery remains read-only.</p>{/if}
        {/if}
      </article>
    {/if}
  {/if}

  <article class="card">
    <h3>Import migration bundle</h3>
    <p class="notice">Bundles may contain literal cloud-init credentials or other secrets. Select a trusted JSON bundle locally; it is sent to the controller only when you adopt. Export and source release do not prove destination adoption.</p>
    <label>Bundle file <input type="file" accept=".json,application/json" on:change={importBundle} disabled={adopting || busy || !$canManageOwnership} /></label>
    {#if importing}<p role="status">Reading bundle locally…</p>{/if}
    {#if bundle || importing}<button disabled={adopting || busy} on:click={clearBundle}>Clear bundle</button>{/if}
    {#if bundle && bundlePreview}
      <p>Loaded {bundleFilename} · source controller <code>{bundle.controllerId}</code></p>
      <p>VM identity: <code>{bundle.identity}</code></p>
      {#if bundleVM}
        <p>Observed destination: context <code>{bundleVM.context}</code> · node <code>{bundleVM.node}</code> · VMID <code>{bundleVM.vmid}</code></p>
        <p>{bundleVM.managed ? 'Managed' : 'Unmanaged'} · {bundleVM.adoptable && !bundleVM.managed ? 'Adoptable' : 'Blocked'}{#if bundleVM.reason}: {bundleVM.reason}{/if}</p>
      {:else}
        <p class="muted">Destination VMID has not been observed for this identity. Run discovery above to review authoritative context, node, VMID, and adoptability. The server must still verify the live destination VM.</p>
      {/if}
      <label>Destination context
        <select bind:value={bundleContext} on:change={() => bundleConfirmed = false} disabled={adopting || busy}>
          {#each contexts as context}<option value={context.name}>{context.name}</option>{/each}
        </select>
      </label>
      <p class="muted">The destination context overrides the source context; the bundle itself is kept unchanged so its digest remains verifiable. Test this destination connection above before adoption. The server validates schema, digest, live identity, and ownership.</p>
      <pre>{resourceToYAML(bundlePreview)}</pre>
      <p class="muted">Test and discover the destination above to review the matching VM and its adoptability before adopting.</p>
      {#if $canManageOwnership}
        <label class="confirmation"><input type="checkbox" bind:checked={bundleConfirmed} disabled={adopting} /> I reviewed this desired manifest and destination context.</label>
        <button class="primary" disabled={adopting || busy || !bundleConfirmed || !bundleContext || !states[bundleContext]?.connected || !bundleVM?.adoptable || bundleVM.managed || bundleVM.identityConfirmationRequired} on:click={() => adopt(true)}>{adopting ? 'Adopting…' : 'Adopt bundle at destination'}</button>
      {:else}<p class="muted">Bundle adoption requires admin access and controller-owned intent with GitOps disabled.</p>{/if}
    {/if}
  </article>
  {#if adoptionError}<p class="err" role="alert">{adoptionError}</p>{/if}
  {#if identityMessage}<p role="status">{identityMessage}</p>{/if}
</section>

<style>
  section { max-width: 80rem; }
  header { display: flex; justify-content: space-between; gap: 1rem; align-items: baseline; flex-wrap: wrap; }
  h2 { margin: 0; font-size: 1.4rem; }
  h3 { margin: 0 0 0.8rem; font-size: 1rem; }
  a { color: #6ea8ff; }
  .muted { color: #888; }
  .notice { border-left: 3px solid #ffce4d; padding: 0.6rem 1rem; background: rgba(127,127,127,0.06); }
  .err { color: #f57171; overflow-wrap: anywhere; }
  .card { margin: 1rem 0; padding: 1.25rem; border: 1px solid rgba(127,127,127,0.25); border-radius: 8px; min-width: 0; }
  .table-scroll { overflow-x: auto; margin-bottom: 1rem; }
  table { border-collapse: collapse; width: 100%; }
  th, td { text-align: left; padding: 0.55rem 0.65rem; border-bottom: 1px solid rgba(127,127,127,0.2); vertical-align: top; }
  th { font-size: 0.8rem; color: #888; }
  td p { margin: 0.3rem 0; }
  button, input, select { font: inherit; border: 1px solid rgba(127,127,127,0.4); border-radius: 5px; padding: 0.4rem 0.7rem; color: inherit; background: transparent; }
  button { cursor: pointer; }
  button:disabled { opacity: 0.45; cursor: not-allowed; }
  .primary { background: #4a8ef0; color: white; }
  label { display: flex; align-items: center; flex-wrap: wrap; gap: 0.6rem; margin: 1rem 0; }
  input:not([type=checkbox]), select { max-width: 100%; min-width: 0; box-sizing: border-box; }
  pre { overflow-x: auto; background: rgba(127,127,127,0.1); padding: 1rem; border-radius: 5px; font-size: 0.8rem; }
  code { overflow-wrap: anywhere; }
  @media (max-width: 40rem) { .card { padding: 0.8rem; } th, td { padding: 0.4rem; } }
</style>
