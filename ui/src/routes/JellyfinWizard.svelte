<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { stringify } from 'yaml';
  import { canMutate } from '../lib/auth';
  import { canEditDesired, desiredMutationHint } from '../lib/authority';
  import { discovery, resources, schemas, type DiscoveryContext, type DryRunApplyResponse, type Operation, type Resource } from '../lib/api';
  import { jellyfinDeployment, APP_API, VM_API, type DeploymentStep, type JellyfinInputs } from '../lib/jellyfin';
  import { routeHref } from '../lib/router';
  import { watchOperations } from '../lib/watch';

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
  let operation: Operation | null = null;
  let stepDone = false;
  let observed: Resource | null = null;
  let watcher: AbortController | null = null;
  let destroyed = false;

  onMount(async () => {
    try {
      const [ctx, inventory] = await Promise.all([discovery.contexts(), resources.list(VM_API, 'VirtualMachine')]);
      contexts = ctx.contexts ?? [];
      input.context = contexts.find((c) => c.defaultContext)?.name ?? contexts[0]?.name ?? '';
      vms = inventory.resources ?? [];
    } catch (err) {
      error = message(err);
    }
  });
  onDestroy(() => { destroyed = true; watcher?.abort(); });

  const message = (err: unknown) => err instanceof Error ? err.message : String(err);
  $: current = steps[index];
  $: allDone = steps.length > 0 && index === steps.length - 1 && stepDone;
  $: gatesSatisfied = (preview?.requiredGates ?? []).every((gate) => checkedGates[gate]);
  $: validPreview = !!preview && (preview.validationErrors?.length ?? 0) === 0 && gatesSatisfied;
  $: monitorOnly = !!operation && ['pending', 'running'].includes(operation.status);
  $: applicationReady = current?.resource.apiVersion !== APP_API || observed?.status?.ready === true;
  $: checks = Object.entries((observed?.status?.checks ?? {}) as Record<string, { ready?: boolean; message?: string }>);

  function download() {
    const blob = new Blob([steps.map((s) => stringify(s.resource)).join('---\n')], { type: 'application/yaml' });
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url; link.download = `${input.name}-deployment.yaml`; link.click();
    URL.revokeObjectURL(url);
  }

  async function prepare() {
    error = '';
    if (!dedicated || !trustedKey || (input.expose && !publicConsent)) {
      error = 'Confirm the dedicated guest, verified SSH host key, and any public exposure before review.';
      return;
    }
    try {
      steps = jellyfinDeployment(input);
      index = 0; operation = null; stepDone = false; observed = null;
      await review();
    } catch (err) { error = message(err); }
  }

  async function review() {
    const current = steps[index];
    if (!current) return;
    busy = true; error = ''; preview = null; checkedGates = {};
    try {
      const result = await schemas.validate(current.resource);
      if (result.errors?.length) throw new Error(result.errors.join('\n'));
      const plan = await resources.dryRunApply(current.resource);
      if (current.createOnly && plan.operation !== 'create') {
        throw new Error('This resource already exists. Inspect it before making changes; this guided flow will not overwrite it.');
      }
      preview = plan;
    } catch (err) { error = message(err); }
    finally { busy = false; }
  }

  async function observeOperation(id: string) {
    watcher?.abort();
    watcher = new AbortController();
    let terminal = false;
    try {
      await watchOperations({ id, includeChildren: true }, (event) => {
        operation = event.operation;
        if (event.terminal) {
          terminal = true;
          stepDone = event.operation.status === 'succeeded';
          if (!stepDone) error = event.operation.error || `Operation ${event.operation.status}. Inspect partial infrastructure before retrying.`;
          watcher?.abort();
        }
      }, { signal: watcher.signal });
    } catch (err) {
      if (!terminal && !destroyed) throw err;
    }
    if (!terminal && !destroyed) throw new Error('Operation monitoring disconnected. Observe this operation again; do not submit a duplicate.');
    if (stepDone && current) {
      const result = await resources.get(current.resource.apiVersion, current.resource.kind, current.resource.metadata.name);
      observed = result.resource ?? null;
    }
  }

  async function apply() {
    if (!current || busy || stepDone || (!monitorOnly && (!validPreview || !$canEditDesired))) return;
    busy = true; error = '';
    try {
      if (operation && ['pending', 'running'].includes(operation.status)) {
        await observeOperation(operation.id);
        return;
      }
      const retry = operation !== null;
      const result = await resources.apply({
        resource: current.resource,
        createOnly: current.createOnly && !retry,
        allowDestructive: checkedGates.allow_destructive ?? false,
        iKnowThisBreaksTheCluster: checkedGates.i_know_this_breaks_the_cluster ?? false,
      });
      if (!result.operationId) throw new Error('The controller did not return an operation ID. Inspect operation history before trying again.');
      operation = { id: result.operationId, type: 'apply', status: 'pending' };
      await observeOperation(result.operationId);
    } catch (err) { error = message(err); }
    finally { busy = false; }
  }

  async function refreshHealth() {
    if (!current) return;
    busy = true; error = '';
    try {
      const result = await resources.get(current.resource.apiVersion, current.resource.kind, current.resource.metadata.name);
      observed = result.resource ?? null;
    } catch (err) { error = message(err); }
    finally { busy = false; }
  }

  async function next() {
    if (!stepDone || !applicationReady || index >= steps.length - 1) return;
    index += 1; operation = null; stepDone = false; observed = null;
    await review();
  }
</script>

<section>
  <header>
    <h2>Deploy Jellyfin</h2>
    <p>Dedicated GPU VM → protected media mount → application → optional public hostname.</p>
  </header>
  <p class="notice">OpenCtl manages each resource independently. Failure does not roll back the VM or delete media. Updates and recovery remain available from resource details.</p>

  {#if steps.length === 0}
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
      <button class="primary" disabled={busy || !$canMutate}>Review deployment</button>
    </form>
  {:else}
    <ol class="steps">{#each steps as step, i}<li class:active={i === index} class:done={i < index || (i === index && stepDone)}>{step.title}</li>{/each}</ol>
    <button on:click={download}>Download reproducible manifests</button>
    {#if current}
      <h3>{index + 1}. {current.title}</h3>
      <pre>{stringify(current.resource)}</pre>
      {#if preview}
        <p><strong>{preview.operation}</strong> — {preview.summary}</p>
        {#if preview.validationErrors?.length}<ul class="error">{#each preview.validationErrors as problem}<li>{problem}</li>{/each}</ul>{/if}
        {#if preview.diff?.length}<ul>{#each preview.diff as change}<li><code>{change.path}</code>: {change.observed} → {change.desired}</li>{/each}</ul>{/if}
        {#each preview.requiredGates ?? [] as gate}<label class="check"><input type="checkbox" bind:checked={checkedGates[gate]} disabled={busy} /> Explicitly allow {gate}</label>{/each}
      {/if}
      {#if operation}<p>Operation <code>{operation.id}</code>: <strong>{operation.status}</strong>. Leaving this page does not cancel it.</p>{/if}
      {#if checks.length}<ul class="health">{#each checks as [name, check]}<li class:healthy={check.ready === true}><strong>{name}: {check.ready === true ? 'ready' : 'not ready'}</strong> — {check.message ?? 'No readiness evidence'}</li>{/each}</ul>{/if}
      {#if !stepDone}
        <div class="actions">
          <button on:click={review} disabled={busy || !!operation}>Refresh preview</button>
          <button class="primary" on:click={apply} disabled={busy || (!monitorOnly && (!validPreview || !$canEditDesired))} title={!monitorOnly && !$canEditDesired ? $desiredMutationHint : ''}>{busy ? 'Working…' : monitorOnly ? 'Observe existing operation' : operation ? 'Retry reviewed step' : 'Apply reviewed step'}</button>
        </div>
      {:else if !applicationReady}
        <p class="notice">The configuration operation succeeded, but application readiness has not been proven. Resolve the reported checks before publishing or proceeding.</p>
        <div class="actions">
          <button on:click={refreshHealth} disabled={busy}>Refresh health</button>
          <button on:click={() => { stepDone = false; void apply(); }} disabled={busy || !$canEditDesired} title={!$canEditDesired ? $desiredMutationHint : ''}>Reapply reviewed configuration</button>
        </div>
      {:else if !allDone}
        <button class="primary" on:click={next} disabled={busy}>Preview next resource</button>
      {:else}
        <p class="success">Application readiness checks passed. GPU transcoding still needs a real playback test.</p>
        <a href={routeHref({ name: 'detail', apiVersion: APP_API, kind: 'Jellyfin', resourceName: input.name })}>Open application health, logs and lifecycle controls</a>
      {/if}
    {/if}
    {#if !busy}<p><button on:click={() => { steps = []; preview = null; operation = null; error = ''; }}>Start a new review (keeps existing infrastructure)</button></p>{/if}
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
