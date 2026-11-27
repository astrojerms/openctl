<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { stringify } from 'yaml';
  import { canMutate } from '../lib/auth';
  import { canEditDesired, desiredMutationHint } from '../lib/authority';
  import { discovery, resources, type DiscoveryContext, type DryRunApplyResponse, type Operation, type Resource } from '../lib/api';
  import { createSavedReview, DeploymentReview, discardSavedReview, failedReviewOperation, loadReviewBinding, readSavedReview, REVIEW_KEY, reviewError, type ReviewBinding, type ReviewState, type SavedReview } from '../lib/deploymentReview';
  import { jellyfinDeployment, APP_API, VM_API, type DeploymentStep, type JellyfinInputs } from '../lib/jellyfin';
  import { routeHref } from '../lib/router';

  let input: JellyfinInputs = {
    name: 'jellyfin', vmName: '', createVM: false, context: '', node: '', templateVMID: 0,
    storage: '', bridge: '', pciMapping: '', sshPublicKey: '', ip: 'dhcp', gateway: '',
    sshUser: 'ubuntu', privateKeyFile: '', knownHostsFile: '~/.ssh/known_hosts', image: '',
    nasServer: '', nasShare: '', nasMediaPath: '', secretProvider: 'file', nasSecret: '',
    adminUser: '', adminSecret: '', viewerUser: '', viewerSecret: '',
    expose: false, hostname: '', tunnel: '', accountId: '', zoneId: '',
  };
  let contexts: DiscoveryContext[] = [];
  let vms: Resource[] = [];
  let dedicated = false;
  let trustedKey = false;
  let publicConsent = false;
  let steps: DeploymentStep[] = [];
  let index = 0;
  let preview: DryRunApplyResponse | null = null;
  let checkedGates: Record<string, boolean> = {};
  let error = '';
  let busy = false;
  let loadingRecovery = true;
  let binding: ReviewBinding | null = null;
  let savedCandidate: SavedReview | null = null;
  let savedPresent = false;
  let savedError = '';
  let recoveryError = '';
  let engine: DeploymentReview | null = null;
  let state: ReviewState | null = null;
  let operation: Operation | null = null;
  let observed: Resource | null = null;
  let verified = false;
  let destroyed = false;

  onMount(() => {
    void refreshRecovery();
    void loadInventory();
  });
  onDestroy(() => { destroyed = true; engine?.dispose(); });

  async function loadInventory() {
    try {
      const [ctx, inventory] = await Promise.all([discovery.contexts(), resources.list(VM_API, 'VirtualMachine')]);
      if (destroyed) return;
      contexts = ctx.contexts ?? [];
      input.context = contexts.find((c) => c.defaultContext)?.name ?? contexts[0]?.name ?? '';
      vms = inventory.resources ?? [];
    } catch (err) { if (!destroyed) error = reviewError(err); }
  }
  async function refreshRecovery() {
    loadingRecovery = true; recoveryError = ''; savedError = ''; binding = null; savedCandidate = null;
    try {
      savedPresent = window.sessionStorage.getItem(REVIEW_KEY) !== null;
      binding = await loadReviewBinding();
      if (!destroyed) savedCandidate = readSavedReview(window.sessionStorage, binding);
    } catch (err) {
      if (savedPresent && binding) savedError = reviewError(err);
      else recoveryError = reviewError(err);
    } finally { loadingRecovery = false; }
  }
  function changed(value: ReviewState) {
    if (destroyed) return;
    if (preview !== value.preview) checkedGates = {};
    state = value;
    steps = value.saved.steps; index = value.saved.cursor;
    preview = value.preview; operation = value.operation;
    observed = value.observed; verified = value.verified;
  }
  function activate(saved: SavedReview) {
    engine?.dispose();
    engine = new DeploymentReview(window.sessionStorage, saved, { canApply: () => $canEditDesired, changed });
    changed(engine.state);
  }
  async function run(action: () => Promise<void>) {
    if (busy) return;
    busy = true; error = '';
    try { await action(); } catch (err) { if (!destroyed) error = reviewError(err); }
    finally { busy = false; }
  }
  $: current = steps[index];
  $: stepDone = verified && operation?.status === 'succeeded';
  $: allDone = steps.length > 0 && index === steps.length - 1 && stepDone;
  $: gatesSatisfied = (preview?.requiredGates ?? []).every((gate) => checkedGates[gate]);
  $: publicReview = steps.some((step) => step.resource.kind === 'TunnelRoute');
  $: consentSatisfied = dedicated && trustedKey && (!publicReview || publicConsent);
  $: validPreview = verified && !!preview && (preview.validationErrors?.length ?? 0) === 0 && gatesSatisfied && consentSatisfied;
  $: monitorOnly = !!operation && ['pending', 'running'].includes(operation.status);
  $: uncertain = !!state?.saved.steps[index]?.attempt && !operation;
  $: applicationReady = current?.resource.apiVersion !== APP_API || observed?.status?.ready === true;
  $: checks = Object.entries((observed?.status?.checks ?? {}) as Record<string, { ready?: boolean; message?: string }>);
  $: applicationName = steps.find((step) => step.resource.kind === 'Jellyfin')?.resource.metadata.name ?? input.name;

  function download(declarations: DeploymentStep[] = steps) {
    if (!declarations.length) return;
    const name = declarations.find((step) => step.resource.kind === 'Jellyfin')?.resource.metadata.name ?? input.name;
    const blob = new Blob([declarations.map((step) => stringify(step.resource)).join('---\n')], { type: 'application/yaml' });
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url; link.download = `${name}-deployment.yaml`; link.click();
    URL.revokeObjectURL(url);
  }
  async function prepare() {
    await run(async () => {
      if (!dedicated || !trustedKey || (input.expose && !publicConsent)) throw new Error('Confirm the dedicated guest, verified SSH host key, and any public exposure before review.');
      steps = jellyfinDeployment(input);
      index = 0; preview = null; operation = null; observed = null; verified = false;
      // Keep declarations downloadable even when capability/storage preflight fails.
      binding = await loadReviewBinding();
      if (window.sessionStorage.getItem(REVIEW_KEY) !== null) throw new Error('A saved review already exists in this tab. Resume or discard it before preparing another deployment.');
      activate(createSavedReview(binding, steps));
      engine!.save();
      savedPresent = true;
      await engine!.review();
    });
  }
  async function resumeSaved() {
    dedicated = false; trustedKey = false; publicConsent = false; checkedGates = {};
    await run(async () => {
      const currentBinding = await loadReviewBinding();
      const saved = readSavedReview(window.sessionStorage, currentBinding);
      if (!saved) throw new Error('The saved review is no longer present.');
      binding = currentBinding;
      activate(saved);
      savedCandidate = null;
      await engine!.resume();
    });
  }
  async function review() {
    dedicated = false; trustedKey = false; publicConsent = false; checkedGates = {};
    await run(async () => {
      if (!engine) throw new Error('Recovery is unavailable. Download these declarations, restore controller capability/session storage and start a fresh review.');
      engine.save();
      await engine.review();
    });
  }
  async function apply() {
    await run(async () => {
      if (!engine) throw new Error('Save a recoverable review before submitting.');
      try {
        await engine.submit({ dedicated, trustedKey, publicExposure: publicConsent, gates: { ...checkedGates } });
      } finally {
        dedicated = false; trustedKey = false; publicConsent = false; checkedGates = {};
      }
      if (engine.state.operation && ['pending', 'running'].includes(engine.state.operation.status)) await engine.observe();
    });
  }
  async function observe() { await run(async () => { if (engine) await engine.observe(); }); }
  async function refreshHealth() { await run(async () => { if (engine) await engine.refreshHealth(); }); }
  async function next() {
    dedicated = false; trustedKey = false; publicConsent = false; checkedGates = {};
    await run(async () => { if (engine) await engine.next(); });
  }
  function discard() {
    error = '';
    try {
      discardSavedReview(window.sessionStorage);
      engine?.dispose(); engine = null; state = null; savedCandidate = null;
      savedPresent = false; savedError = ''; steps = []; index = 0;
      preview = null; operation = null; observed = null; verified = false;
      dedicated = false; trustedKey = false; publicConsent = false; checkedGates = {};
    } catch {
      error = 'Cannot clear this tab’s saved review. Enable session storage or clear it in browser settings. Existing controller work and infrastructure are unchanged.';
    }
  }
</script>

<section>
  <header>
    <h2>Deploy Jellyfin</h2>
    <p>Dedicated GPU VM → protected media mount → application → optional public hostname.</p>
  </header>
  <p class="notice">OpenCtl manages each resource independently. Failure does not roll back the VM or delete media. Updates and recovery remain available from resource details.</p>
  <p class="notice">Prepared reviews and request identities are saved only in this browser tab (session storage), bound to this controller and signed-in user. Reloading or navigating back offers recovery, never an automatic Apply. Download declarations before closing the tab. Discarding a review never cancels controller work or removes infrastructure.</p>
  {#if loadingRecovery}<p>Checking controller recovery capability and this tab’s saved review…</p>{/if}
  {#if recoveryError}<p class="error" role="alert">{recoveryError}</p><button on:click={refreshRecovery} disabled={busy || loadingRecovery}>Check recovery access again</button>{/if}
  {#if savedError}<p class="error" role="alert">{savedError}</p>{/if}

  {#if steps.length === 0 && savedPresent}
    <aside class="notice">
      <h3>Saved deployment review</h3>
      <p>No infrastructure will be changed by resuming. Accepted work is looked up on the controller; readiness, preview and all permissions must be checked again.</p>
      {#if savedCandidate}
        <button class="primary" on:click={resumeSaved} disabled={busy || loadingRecovery}>Resume saved review</button>
        <button on:click={() => download(savedCandidate?.steps ?? [])} disabled={busy}>Download saved declarations</button>
      {/if}
      <button on:click={discard} disabled={busy}>Discard browser review (keeps controller work)</button>
    </aside>
  {:else if steps.length === 0}
    <form on:submit|preventDefault={prepare}>
      <fieldset disabled={busy || !$canMutate}>
        <legend>1. Dedicated Ubuntu 24.04 GPU guest</legend>
        <label>Application name <input bind:value={input.name} required pattern="[a-z][a-z0-9-]*" /></label>
        <label class="check"><input type="checkbox" bind:checked={input.createVM} /> Provision a new VM</label>
        {#if input.createVM}
          <label>VM name <input bind:value={input.vmName} required /></label>
          <label>Proxmox context <select bind:value={input.context} required><option value="">Select context</option>{#each contexts as ctx}<option value={ctx.name}>{ctx.name}</option>{/each}</select></label>
          <label>Proxmox node <input bind:value={input.node} required /></label>
          <label>Ubuntu 24.04 template VMID <input type="number" min="1" bind:value={input.templateVMID} required /></label>
          <label>VM disk and EFI storage <input bind:value={input.storage} required /></label>
          <label>Network bridge <input bind:value={input.bridge} required /></label>
          <label>Verified PCI/GPU resource mapping <input bind:value={input.pciMapping} required /></label>
          <label>Guest IP (dhcp or reserved CIDR) <input bind:value={input.ip} required /></label>
          {#if input.ip !== 'dhcp'}<label>Gateway <input bind:value={input.gateway} required /></label>{/if}
          <label>Guest SSH public key <textarea bind:value={input.sshPublicKey} required rows="2"></textarea></label>
          <p class="hint">4 vCPUs, 4 GiB RAM, 32 GiB SSD, UEFI/q35. Host IOMMU/VFIO and the PCI mapping must already be prepared; OpenCtl does not disconnect host hardware automatically.</p>
        {:else}
          <label>Managed GPU VM <select bind:value={input.vmName} required><option value="">Select a VM</option>{#each vms as vm}<option value={vm.metadata.name}>{vm.metadata.name}</option>{/each}</select></label>
          <p class="hint">Existing VM not listed? <a href={routeHref({ name: 'discover' })}>Discover and adopt it first.</a></p>
        {/if}
        <label>SSH user <input bind:value={input.sshUser} required /></label>
        <label>Controller-side SSH private key file <input bind:value={input.privateKeyFile} required /></label>
        <label>Controller-side verified known_hosts file <input bind:value={input.knownHostsFile} required /></label>
        <label class="check"><input type="checkbox" bind:checked={dedicated} required /> This is a dedicated Ubuntu 24.04 NVIDIA guest. I authorize Docker, driver, mount and service configuration inside it; no other applications share it.</label>
        <label class="check"><input type="checkbox" bind:checked={trustedKey} required /> I will verify the guest SSH host key and install it in this controller's known_hosts before application Apply. No trust-on-first-use bypass.</label>
        <p class="hint">A newly installed GPU driver may require an operator-controlled guest reboot. Reapply the same reviewed application afterwards; OpenCtl never reboots or destroys the VM automatically.</p>
      </fieldset>

      <fieldset disabled={busy || !$canMutate}>
        <legend>2. Application, media and secrets</legend>
        <label>Official pinned image <input bind:value={input.image} placeholder="jellyfin/jellyfin@sha256:…" required /></label>
        <p class="hint">Use the published digest for your chosen official image version. No moving latest tag is selected for you.</p>
        <label>NAS hostname/address <input bind:value={input.nasServer} required /></label>
        <label>SMB share <input bind:value={input.nasShare} required /></label>
        <label>Media subdirectory (empty = share root) <input bind:value={input.nasMediaPath} placeholder="e.g. video" /></label>
        <label>Secret provider <input bind:value={input.secretProvider} required /></label>
        <label>SMB credentials secret key <input bind:value={input.nasSecret} required /></label>
        <p class="hint">The SMB secret contains a credentials file (username=… and password=…). Media is mounted read-only; configuration and cache stay on the guest disk.</p>
        <label>Admin username <input bind:value={input.adminUser} required autocomplete="off" /></label>
        <label>Admin password secret key <input bind:value={input.adminSecret} required /></label>
        <label>Viewer username <input bind:value={input.viewerUser} required autocomplete="off" /></label>
        <label>Viewer password secret key <input bind:value={input.viewerSecret} required /></label>
        <p class="hint">Enter secret references, never passwords. Values are resolved transiently on the controller and delivered only to the dedicated guest.</p>
      </fieldset>

      <fieldset disabled={busy || !$canMutate}>
        <legend>3. Optional domain exposure</legend>
        <label class="check"><input type="checkbox" bind:checked={input.expose} /> Publish through an existing Cloudflare tunnel</label>
        {#if input.expose}
          <label>Public hostname <input bind:value={input.hostname} required /></label>
          <label>Existing connected Tunnel resource name <input bind:value={input.tunnel} required /></label>
          <label>Cloudflare account ID (unless provider default) <input bind:value={input.accountId} /></label>
          <label>Cloudflare zone ID (unless provider default) <input bind:value={input.zoneId} /></label>
          <p class="hint">The tunnel's connector must already reach the guest's HTTP origin. Route and DNS are previewed separately after the application exists. A DNS record is not proof of public reachability.</p>
          <label class="check"><input type="checkbox" bind:checked={publicConsent} required /> I authorize publishing this application at this hostname, after reviewing its access policy.</label>
        {/if}
      </fieldset>
      <button class="primary" disabled={busy || loadingRecovery || !$canMutate}>Prepare and save deployment review</button>
    </form>
  {:else}
    <ol class="steps">{#each steps as step, i}<li class:active={i === index} class:done={verified && (i < index || (i === index && stepDone))}>{step.title}</li>{/each}</ol>
    <button on:click={() => download()}>Download reproducible manifests</button>
    {#if current}
      <h3>{index + 1}. {current.title}</h3>
      <pre>{stringify(current.resource)}</pre>
      {#if state?.saved.steps[index]?.attempt}<p>Saved request: <code>{state.saved.steps[index].attempt?.requestId}</code>. Keep this identity when recovering an uncertain submission.</p>{/if}
      {#if uncertain}<p class="notice">This request has no confirmed operation yet. Look it up first. If the controller still reports NotFound, a fresh review can explicitly retransmit the exact same request ID; a new ID must not be used.</p>{/if}
      {#if failedReviewOperation(operation)}<p class="notice">This Apply ended without success. Inspect partial infrastructure first. Refresh the preview and confirm permissions to explicitly retry with a new request ID. No rollback or cleanup is automatic.</p>{/if}
      {#if preview}
        <p><strong>{preview.operation}</strong> — {preview.summary}</p>
        {#if preview.validationErrors?.length}<ul class="error">{#each preview.validationErrors as problem}<li>{problem}</li>{/each}</ul>{/if}
        {#if preview.diff?.length}<ul>{#each preview.diff as change}<li><code>{change.path}</code>: {change.observed} → {change.desired}</li>{/each}</ul>{/if}
        {#each preview.requiredGates ?? [] as gate}<label class="check"><input type="checkbox" bind:checked={checkedGates[gate]} disabled={busy} /> Explicitly allow {gate}</label>{/each}
        <label class="check"><input type="checkbox" bind:checked={dedicated} disabled={busy} /> I confirm this dedicated guest and authorize the reviewed guest configuration.</label>
        <label class="check"><input type="checkbox" bind:checked={trustedKey} disabled={busy} /> I verified this guest’s SSH host key in the controller’s known_hosts.</label>
        {#if publicReview}<label class="check"><input type="checkbox" bind:checked={publicConsent} disabled={busy} /> I freshly authorize the declared public hostname and access policy.</label>{/if}
      {/if}
      {#if operation}<p>Operation <code>{operation.id}</code>: <strong>{operation.status}</strong>. Leaving this page does not cancel it.</p>{/if}
      {#if operation?.error}<p class="error" role="alert">{operation.error}</p>{/if}
      {#if checks.length}<ul class="health">{#each checks as [name, check]}<li class:healthy={check.ready === true}><strong>{name}: {check.ready === true ? 'ready' : 'not ready'}</strong> — {check.message ?? 'No readiness evidence'}</li>{/each}</ul>{/if}
      {#if !stepDone}
        <div class="actions">
          <button on:click={observe} disabled={busy || !state?.saved.steps[index]?.attempt}>Look up / observe saved request</button>
          <button on:click={review} disabled={busy || monitorOnly || operation?.status === 'succeeded'}>{failedReviewOperation(operation) ? 'Review explicit retry' : 'Refresh preview'}</button>
          <button class="primary" on:click={apply} disabled={busy || !validPreview || !$canEditDesired} title={!$canEditDesired ? $desiredMutationHint : ''}>{busy ? 'Working…' : uncertain ? 'Retransmit SAME reviewed request' : failedReviewOperation(operation) ? 'Retry freshly reviewed step (new request ID)' : 'Apply reviewed step'}</button>
        </div>
      {:else if !applicationReady}
        <p class="notice">The configuration operation succeeded, but application readiness has not been proven. Resolve the reported checks before publishing or proceeding.</p>
        <div class="actions">
          <button on:click={refreshHealth} disabled={busy}>Refresh health</button>
          <a href={routeHref({ name: 'detail', apiVersion: current.resource.apiVersion, kind: current.resource.kind, resourceName: current.resource.metadata.name })}>Inspect and repair resource configuration</a>
        </div>
      {:else if !allDone}
        <button class="primary" on:click={next} disabled={busy}>Preview next resource</button>
      {:else}
        <p class="success">Application readiness checks passed. GPU transcoding still needs a real playback test.</p>
        <a href={routeHref({ name: 'detail', apiVersion: APP_API, kind: 'Jellyfin', resourceName: applicationName })}>Open application health, logs and lifecycle controls</a>
      {/if}
    {/if}
    {#if !busy}<p><button on:click={discard}>Discard / start a new browser review (keeps controller work and infrastructure)</button></p>{/if}
  {/if}
  {#if error}<p class="error" role="alert">{error}</p>{/if}
</section>

<style>
  section { max-width: 70rem; }
  header h2 { margin: 0; }
  header p, .hint { color: #888; }
  .notice { border-left: 3px solid #c8a24a; padding: 0.8rem 1rem; background: rgba(200,162,74,.08); }
  form { display: grid; gap: 1rem; }
  fieldset { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 1rem; border: 1px solid #555; border-radius: 6px; padding: 1rem; }
  legend { padding: 0 .5rem; font-weight: 600; }
  label { display: flex; flex-direction: column; gap: .35rem; font-size: .9rem; }
  input, select, textarea { box-sizing: border-box; width: 100%; padding: .55rem; border: 1px solid #666; border-radius: 4px; background: transparent; color: inherit; font: inherit; }
  .check { grid-column: 1 / -1; flex-direction: row; align-items: flex-start; }
  .check input { width: auto; margin-top: .2rem; }
  .hint { grid-column: 1 / -1; margin: 0; font-size: .85rem; }
  button { width: fit-content; padding: .55rem .9rem; cursor: pointer; }
  button:disabled { cursor: default; opacity: .5; }
  .primary { background: #336fc2; color: white; border: 1px solid #336fc2; border-radius: 4px; }
  pre { overflow: auto; max-height: 28rem; background: rgba(127,127,127,.08); padding: 1rem; border-radius: 4px; }
  .steps { display: flex; flex-wrap: wrap; gap: .7rem 2rem; padding-left: 1.2rem; color: #888; }
  .steps .active { color: inherit; font-weight: 600; }
  .steps .done, .success, .healthy { color: #41a77a; }
  .actions { display: flex; gap: .6rem; margin: 1rem 0; }
  .error { color: #e76a6a; white-space: pre-wrap; }
  .health { padding: 1rem 1.5rem; border: 1px solid #555; border-radius: 4px; }
  .health li { margin: .4rem 0; }
  @media (max-width: 650px) { fieldset { grid-template-columns: 1fr; } }
</style>
