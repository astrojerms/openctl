<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { stringify } from 'yaml';
  import { discovery, inventoryApi, UnauthorizedError, type DiscoveryContext, type SavedResourceMetadata } from '../lib/api';
  import { auth } from '../lib/auth';
  import { canEditDesired, desiredMutationHint } from '../lib/authority';
  import { APP_API, VM_API, REVIEW_KEYS, type DeploymentStep } from '../lib/deployment';
  import { createSavedReview, DeploymentReview, discardSavedReview, failedReviewOperation, loadReviewBinding, readSavedReview, reviewError, type ReviewState, type SavedReview } from '../lib/deploymentReview';
  import { composeDefaults, composeDeployment, composeLoopback, guestTrustCommand, shellArgument, shellFile } from '../lib/compose';
  import { deploymentArchive, deploymentManifest } from '../lib/deploymentExport';
  import { applicationCheckBadge, applicationChecks, applicationReadiness } from '../lib/detailState';
  import { routeHref } from '../lib/router';
  import DeploymentPreview from '../components/DeploymentPreview.svelte';

  let input = composeDefaults();
  let contexts: DiscoveryContext[] = [];
  let vms: SavedResourceMetadata[] = [];
  let loadingContexts = true;
  let loadingVMs = true;
  let contextError = '';
  let vmError = '';
  let recoveryLoading = true;
  let savedPresent = false;
  let saved: SavedReview | null = null;
  let recoveryError = '';
  let engine: DeploymentReview | null = null;
  let state: ReviewState | null = null;
  let drafts: DeploymentStep[] = [];
  let busy = false;
  let error = '';
  let dedicated = false;
  let trustedKey = false;
  let gates: Record<string, boolean> = {};
  let destroyed = false;
  const requests = new AbortController();
  let vmRequests = new AbortController();
  let contextRequests = new AbortController();

  onMount(() => { void recovery(); void loadVMs(); void loadContexts(); });
  onDestroy(() => { destroyed = true; requests.abort(); vmRequests.abort(); contextRequests.abort(); engine?.dispose(); });
  function failure(err: unknown): string {
    if (err instanceof UnauthorizedError) auth.set({ kind: 'signed-out' });
    return reviewError(err);
  }
  async function loadVMs() {
    vmRequests.abort(); const request = vmRequests = new AbortController();
    loadingVMs = true; vmError = ''; input.vmName = ''; clearConsent();
    try {
      const response = await inventoryApi.list(VM_API, 'VirtualMachine', { signal: request.signal });
      if (!destroyed && request === vmRequests) vms = (response.resources ?? []).slice().sort((a, b) => a.metadata.name.localeCompare(b.metadata.name));
    } catch (err) { if (!destroyed && request === vmRequests) { vms = []; vmError = failure(err); } }
    finally { if (!destroyed && request === vmRequests) loadingVMs = false; }
  }
  async function loadContexts() {
    contextRequests.abort(); const request = contextRequests = new AbortController();
    loadingContexts = true; contextError = '';
    try {
      const response = await discovery.contexts(undefined, { signal: request.signal });
      if (!destroyed && request === contextRequests) {
        contexts = response.contexts ?? [];
        if (!contexts.some((item) => item.name === input.context)) input.context = '';
      }
    } catch (err) { if (!destroyed && request === contextRequests) { contexts = []; contextError = failure(err); } }
    finally { if (!destroyed && request === contextRequests) loadingContexts = false; }
  }
  async function recovery() {
    recoveryLoading = true; recoveryError = ''; saved = null;
    try {
      savedPresent = window.sessionStorage.getItem(REVIEW_KEYS.compose) !== null;
      const binding = await loadReviewBinding({ signal: requests.signal });
      if (!destroyed) saved = readSavedReview('compose', window.sessionStorage, binding);
    } catch (err) { if (!destroyed) recoveryError = failure(err); }
    finally { if (!destroyed) recoveryLoading = false; }
  }
  function clearConsent() { dedicated = false; trustedKey = false; gates = {}; }
  function changed(value: ReviewState) {
    if (destroyed) return;
    clearConsent();
    state = value;
  }
  function activate(value: SavedReview) {
    engine?.dispose();
    engine = new DeploymentReview('compose', window.sessionStorage, value, { canApply: () => $canEditDesired, changed });
    changed(engine.state);
  }
  async function run(action: () => Promise<void>) {
    if (busy || destroyed) return;
    busy = true; error = '';
    try { await action(); } catch (err) { if (!destroyed) error = failure(err); }
    finally { if (!destroyed) busy = false; }
  }
  async function prepare() {
    await run(async () => {
      if (!$canEditDesired) throw new Error('Desired changes are not permitted by the current controller authority or role.');
      if (input.createVM) {
        if (loadingContexts || contextError || !contexts.some((context) => context.name === input.context)) throw new Error('Load and explicitly select an active Proxmox context.');
      } else if (loadingVMs || vmError || !vms.some((vm) => vm.metadata.name === input.vmName)) throw new Error('Load saved VM metadata and explicitly select a managed Docker guest. Saved names do not prove readiness.');
      const declarations = await composeDeployment({ ...input }, { signal: requests.signal });
      if (destroyed) return;
      drafts = declarations;
      const binding = await loadReviewBinding({ signal: requests.signal });
      if (destroyed) return;
      if (window.sessionStorage.getItem(REVIEW_KEYS.compose) !== null) throw new Error('Resume or discard this tab’s existing application review first.');
      activate(createSavedReview('compose', binding, declarations));
      engine!.save(); savedPresent = true;
      await engine!.review();
    });
  }
  async function resume() {
    clearConsent();
    await run(async () => {
      const binding = await loadReviewBinding({ signal: requests.signal });
      if (destroyed) return;
      const currentSaved = readSavedReview('compose', window.sessionStorage, binding);
      if (!currentSaved) throw new Error('The saved application review is no longer present.');
      activate(currentSaved); saved = null;
      await engine!.resume();
    });
  }
  async function review() { clearConsent(); await run(async () => { if (!engine) throw new Error('Restore recovery access or download these declarations before preparing a new review.'); engine.save(); await engine.review(); }); }
  async function apply() {
    await run(async () => {
      if (!engine) throw new Error('Save a recoverable review before Apply.');
      try { await engine.submit({ dedicated, trustedKey, publicExposure: false, gates: { ...gates } }); }
      finally { clearConsent(); }
      if (engine.state.operation) await engine.observe();
    });
  }
  async function next() { clearConsent(); await run(async () => { if (engine) await engine.next(); }); }
  function discard() {
    try {
      discardSavedReview('compose', window.sessionStorage); engine?.dispose(); engine = null;
      state = null; saved = null; savedPresent = false; drafts = []; recoveryError = ''; error = ''; clearConsent();
    } catch (err) { error = failure(err); }
  }
  function saveDownload(blob: Blob, filename: string) {
    const url = URL.createObjectURL(blob); const link = document.createElement('a');
    link.href = url; link.download = filename; link.click(); setTimeout(() => URL.revokeObjectURL(url), 0);
  }
  function download(steps: DeploymentStep[] = declarations) {
    if (steps.length) saveDownload(new Blob([deploymentArchive(steps)], { type: 'application/zip' }), 'application-deployment.zip');
  }
  function downloadCurrent() {
    if (!current) return; const file = deploymentManifest(current, index);
    saveDownload(new Blob([file.yaml], { type: 'application/yaml' }), file.filename);
  }
  $: declarations = state?.saved.steps ?? drafts;
  $: index = state?.saved.cursor ?? 0;
  $: current = declarations[index];
  $: preview = state?.preview ?? null;
  $: operation = state?.operation ?? null;
  $: application = declarations.find((step) => step.resource.kind === 'ComposeApplication')?.resource;
  $: loopback = composeLoopback(application);
  $: target = application?.spec?.target as { user?: string; privateKeyFile?: string; knownHostsFile?: string; host?: { $ref?: { name?: string } } } | undefined;
  $: guestName = declarations.find((step) => step.resource.kind === 'VirtualMachine')?.resource.metadata.name ?? target?.host?.$ref?.name ?? input.vmName;
  $: knownHostsFile = typeof target?.knownHostsFile === 'string' ? target.knownHostsFile : input.knownHostsFile;
  $: appStage = current?.resource.apiVersion === APP_API;
  $: reviewed = !!state?.verified && !!preview?.operation && !preview.validationErrors?.length && !preview.fieldErrors?.length;
  $: done = !!state?.verified && operation?.status === 'succeeded';
  $: ready = !appStage || applicationReadiness(state?.observed?.status) === 'ready';
  $: allDone = done && ready && index === declarations.length - 1;
  $: checks = applicationChecks(state?.observed?.status);
  $: uncertain = !!state?.saved.steps[index]?.attempt && !operation;
  $: monitoring = !!operation && ['pending', 'running'].includes(operation.status);
  $: valid = reviewed && dedicated && (!appStage || trustedKey) && (preview?.requiredGates ?? []).every((gate) => gates[gate]);
  $: if (!$canEditDesired) clearConsent();
</script>

<section>
  <h2>Deploy application</h2>
  <p>Ubuntu Docker guest → standalone Docker welcome app. Each resource has its own review, Apply and recovery. Failure never rolls back or deletes the guest.</p>
  <p class="notice">Only guarded desired declarations, controller-host file paths and durable request identities are saved in this browser tab, bound to this controller and actor. Reloading never applies anything. Downloads are not permission or request recovery; keep them private and outside watched GitOps directories.</p>
  {#if recoveryLoading}<p role="status">Checking saved application review…</p>{/if}
  {#if recoveryError}<p class="error" role="alert">{recoveryError}</p><button on:click={recovery} disabled={busy || recoveryLoading}>Check recovery access again</button>{/if}
  {#if declarations.length === 0 && savedPresent}
    <aside class="notice"><h3>Saved application review</h3><p>Resume performs read-only recovery. Preview, readiness and consent are never restored.</p>
      {#if saved}<button on:click={resume} disabled={busy || recoveryLoading}>Resume saved review</button><button on:click={() => download(saved?.steps ?? [])} disabled={busy}>Download saved manifests (.zip)</button>{/if}
      <button on:click={discard} disabled={busy}>Discard browser review (keeps controller work)</button>
    </aside>
  {:else if declarations.length === 0}
    <form on:submit|preventDefault={prepare}>
      <fieldset disabled={busy || !$canEditDesired}>
        <legend>Guest and application</legend>
        <label>Application name <input bind:value={input.name} required pattern="[a-z][a-z0-9-]*" /></label>
        <label class="check"><input type="checkbox" bind:checked={input.createVM} /> Provision a new VM</label>
        {#if input.createVM}
          <label>VM name <input bind:value={input.vmName} required pattern="[a-z][a-z0-9-]*" /></label>
          <label>Proxmox context <select bind:value={input.context} required disabled={loadingContexts || !!contextError}><option value="">Select context</option>{#each contexts as context}<option value={context.name}>{context.name}</option>{/each}</select></label>
          {#if loadingContexts}<p role="status">Loading active contexts…</p>{:else if contextError}<p class="error" role="alert">Active contexts unavailable: {contextError}</p>{:else if !contexts.length}<p>No active Proxmox contexts. Configure a provider and restart the controller.</p>{/if}
          <button type="button" on:click={loadContexts} disabled={loadingContexts}>Refresh contexts</button>
          <label>Proxmox node <input bind:value={input.node} required /></label>
          <label>Ubuntu 24.04 amd64 template VMID <input type="number" min="1" step="1" bind:value={input.templateVMID} required /></label>
          <label>VM CPU cores <input type="number" min="1" step="1" bind:value={input.cpuCores} required /></label>
          <label>VM memory (MiB) <input type="number" min="512" step="1" bind:value={input.memoryMiB} required /></label>
          <label>VM OS disk (GiB) <input type="number" min="8" step="1" bind:value={input.diskGiB} required /></label>
          <label>Storage <input bind:value={input.storage} required /></label>
          <label>Network bridge <input bind:value={input.bridge} required /></label>
          <label>Guest SSH public key <textarea bind:value={input.sshKey} required rows="2"></textarea></label>
          <p>Defaults: 2 CPU / 2048 MiB / 16 GiB. Choose host/storage capacity and a disk at least as large as the source template. Preview reserves no capacity. First boot installs supported Docker CE and Compose 5.8.1 automatically on Ubuntu 24.04 amd64. Package-network or cloud-init failures must be inspected, not bypassed.</p>
          <p>New-guest cloud-init requires snippets-enabled Proxmox storage and <code>snippetSSH.hosts</code> for this node in the controller's Proxmox context. Configure a controller-host SSH identity and independently verified hypervisor host key first. The API token alone cannot upload Proxmox snippets.</p>
        {:else}
          <label>Saved managed Docker guest <select bind:value={input.vmName} required disabled={loadingVMs || !!vmError}><option value="">Select a saved VM</option>{#each vms as vm}<option value={vm.metadata.name}>{vm.metadata.name}</option>{/each}</select></label>
          {#if loadingVMs}<p role="status">Loading saved VM metadata…</p>{:else if vmError}<p class="error" role="alert">Saved VM metadata unavailable: {vmError}</p>{:else if !vms.length}<p>No saved managed VMs. Provision a new guest or explicitly discover and adopt an existing one first.</p>{/if}
          <button type="button" on:click={loadVMs} disabled={loadingVMs}>Refresh saved VM choices</button>
          <p>Saved metadata is not Docker or SSH readiness evidence. Only the selected guest is freshly inspected before app admission. Existing guests are never silently resized, bootstrapped or upgraded. Install the supported Docker/Compose version explicitly if preflight rejects it. <a href={routeHref({ name: 'discover' })}>Discover infrastructure</a></p>
        {/if}
        <label>SSH user <input bind:value={input.sshUser} required /></label>
        <label>Controller-host SSH private key file <input bind:value={input.privateKeyFile} required /></label>
        <label>Controller-host verified known_hosts file <input bind:value={input.knownHostsFile} required /></label>
        <p>These are paths on the controller host, not your browser computer. Never enter private keys or credentials. Strict SSH and a fresh guest address gate app admission; a running VM is not proof of bootstrap completion.</p>
        <label>Guest loopback port <input type="number" min="1" max="65535" step="1" bind:value={input.port} required /></label>
        <label>Welcome text (non-sensitive) <input bind:value={input.welcomeText} required /></label>
        <p>No public exposure or publishing is created. The app listens on the guest’s loopback interface.</p>
      </fieldset>
      <button class="primary" disabled={busy || recoveryLoading || !$canEditDesired || (input.createVM ? loadingContexts || !!contextError : loadingVMs || !!vmError)}>Prepare and save deployment review</button>
    </form>
  {:else}
    <ol>{#each declarations as step, i}<li aria-current={i === index ? 'step' : undefined}>{step.title}</li>{/each}</ol>
    <div class="actions"><button on:click={() => download()} disabled={busy}>Download manifests (.zip)</button><button on:click={downloadCurrent} disabled={busy}>Download current manifest</button></div>
    {#if current}
      <h3>{index + 1}. {current.title}</h3><pre>{stringify(current.resource)}</pre>
      <aside class="notice" aria-label="Verified guest SSH trust prerequisite">
        <h4>Verified guest SSH trust before application Apply</h4>
        <p>After VM creation and guest-agent availability, obtain host keys through the trusted Proxmox agent. Run this on the controller host with a NEW known_hosts download path. The download is explicit, exclusive and never overwrites an existing file. No network keyscan, automatic acceptance or trust bypass is performed. An already verified trust file can simply be supplied instead.</p>
        <pre>{guestTrustCommand(guestName, knownHostsFile)}</pre>
        <p>If you chose a different parent directory, create it privately first. Keep the selected path <code>{knownHostsFile}</code> consistent with this reviewed app. To change it, discard only the browser review and prepare a fresh review, selecting the already-created guest.</p>
        <a href={routeHref({ name: 'detail', apiVersion: VM_API, kind: 'VirtualMachine', resourceName: guestName })} target="_blank" rel="noopener noreferrer">Inspect VM and bootstrap in a separate tab</a>
      </aside>
      {#if state?.saved.steps[index]?.attempt}<p>Saved request: <code>{state.saved.steps[index].attempt?.requestId}</code>. Keep this exact identity when acknowledgement is uncertain.</p>{/if}
      {#if uncertain}<p class="notice">Look up the original request first. Only a fresh preview and consent can explicitly retransmit the SAME uncertain request; never mint a replacement key.</p>{/if}
      {#if failedReviewOperation(operation)}<p class="notice">Inspect partial infrastructure before explicitly reviewing a retry. No rollback, upgrade, cleanup or retry is automatic.</p>{/if}
      {#if state?.blocker}
        {@const blocker = state.blocker}
        <aside class="notice" aria-label="Blocking deployment resource"><h4>Blocked by {blocker.target.kind}/{blocker.target.name}</h4>
          <p><code>{blocker.target.apiVersion}</code> <code>[{blocker.code}]</code></p><p>{blocker.message}</p><p>{blocker.detail}</p>
          <a href={routeHref({ name: 'detail', apiVersion: blocker.target.apiVersion, kind: blocker.target.kind, resourceName: blocker.target.name })} target="_blank" rel="noopener noreferrer">Inspect blocking resource</a>
          <button on:click={review} disabled={busy}>Recheck prerequisites</button>
        </aside>
      {/if}
      <DeploymentPreview {preview} {reviewed}>
        {#each preview?.requiredGates ?? [] as gate}<label class="check"><input type="checkbox" bind:checked={gates[gate]} disabled={busy} /> Explicitly allow {gate}</label>{/each}
        <label class="check"><input type="checkbox" bind:checked={dedicated} disabled={busy} /> I authorize this reviewed resource on the selected dedicated Docker guest.</label>
        {#if appStage}<label class="check"><input type="checkbox" bind:checked={trustedKey} disabled={busy} /> I verified the guest SSH host key in the selected controller-host known_hosts file.</label>{/if}
      </DeploymentPreview>
      {#if operation}<p>Operation <code>{operation.id}</code>: <strong>{operation.status}</strong>. Leaving does not cancel it.</p>{/if}
      {#if operation?.error}<p class="error" role="alert">{operation.error}</p>{/if}
      {#if checks.length}<ul class="health">{#each checks as check}{@const badge = applicationCheckBadge(check.state)}<li><strong>{check.name}: {badge.label}</strong> — {check.message || 'No readiness evidence'}</li>{/each}</ul>{/if}
      {#if !done}
        <div class="actions">
          <button on:click={() => run(async () => { if (engine) await engine.observe(); })} disabled={busy || !state?.saved.steps[index]?.attempt}>Look up / observe saved request</button>
          <button on:click={review} disabled={busy || monitoring || operation?.status === 'succeeded'}>{failedReviewOperation(operation) ? 'Review explicit retry' : 'Refresh preview'}</button>
          <button class="primary" on:click={apply} disabled={busy || !valid || !$canEditDesired} title={!$canEditDesired ? $desiredMutationHint : ''}>{uncertain ? 'Retransmit SAME reviewed request' : failedReviewOperation(operation) ? 'Retry freshly reviewed step (new request ID)' : 'Apply reviewed step'}</button>
        </div>
      {:else if !ready}
        <p class="notice">The operation succeeded, but fresh application readiness is unproven. Inspect and repair the reported checks explicitly.</p><button on:click={() => run(async () => { if (engine) await engine.refreshHealth(); })} disabled={busy}>Refresh health</button>
      {:else if !allDone}<button class="primary" on:click={next} disabled={busy}>Preview next resource</button>
      {:else}
        <p class="success">Application readiness checks passed. The declared loopback URL is guest-local, not an operator-browser address: <code>{loopback?.url ?? 'Inspect the reviewed application for its guest-local URL'}</code>.</p>
        <p>For explicit SSH forwarding, inspect the VM’s current guest address and replace GUEST_ADDRESS below. Run on the controller host; the forwarded port then belongs to the controller’s localhost, not your workstation. To browse from another computer, explicitly tunnel that port to your workstation or run the verified SSH forward there with separately installed local key/trust paths. No public listener is opened.</p>
        {#if loopback}
          <pre>ssh -N -o StrictHostKeyChecking=yes -o UserKnownHostsFile={shellFile(knownHostsFile)} -i {shellFile(target?.privateKeyFile ?? input.privateKeyFile)} -L 127.0.0.1:18080:127.0.0.1:{loopback.port} {shellArgument(`${target?.user ?? input.sshUser}@GUEST_ADDRESS`)}</pre>
          <p>Open <code>http://127.0.0.1:18080</code> only in a browser on the machine where your SSH forward is listening.</p>
        {/if}
        <a href={routeHref({ name: 'detail', apiVersion: APP_API, kind: 'ComposeApplication', resourceName: application?.metadata.name ?? input.name })}>Inspect application health, logs and lifecycle controls</a>
        <button on:click={() => run(async () => { if (engine) await engine.refreshHealth(); })} disabled={busy}>Refresh health</button>
      {/if}
    {/if}
    <p><button on:click={discard} disabled={busy}>Discard / start a new browser review (keeps controller work and infrastructure)</button></p>
  {/if}
  {#if error && error !== state?.blocker?.message}<p class="error" role="alert">{error}</p>{/if}
</section>

<style>
  section { max-width: 960px; }
  fieldset { margin: 1rem 0; border: 1px solid var(--border); border-radius: .5rem; padding: 1rem; }
  label { display: flex; flex-direction: column; gap: .3rem; margin: .7rem 0; }
  label.check { flex-direction: row; align-items: flex-start; }
  input, select, textarea { max-width: 100%; padding: .45rem; }
  .notice { background: rgba(127, 127, 127, .12); padding: 1rem; border-radius: .4rem; margin: 1rem 0; }
  .actions { display: flex; flex-wrap: wrap; gap: .6rem; margin: 1rem 0; }
  pre { overflow-x: auto; padding: 1rem; background: rgba(127, 127, 127, .12); }
  code { overflow-wrap: anywhere; }
  .error { color: var(--danger, #f87171); }
  .success { color: var(--success, #4ade80); }
  @media (prefers-color-scheme: light) {
    .error { color: #b91c1c; }
    .success { color: #166534; }
  }
</style>
