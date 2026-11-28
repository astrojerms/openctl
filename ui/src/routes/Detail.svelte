<script lang="ts">
  import { onDestroy } from 'svelte';
  import { get } from 'svelte/store';
  import { resources, ownership, operations, UnauthorizedError } from '../lib/api';
  import type { Operation, Resource, ResourceRef, ActionSpec, MigrationBundle } from '../lib/api';
  import { auth, canMutate } from '../lib/auth';
  import { canEditDesired, canManageOwnership, desiredMutationHint } from '../lib/authority';
  import { applicationCheckBadge, applicationChecks, childHealth } from '../lib/detailState';
  import type { ChildState } from '../lib/detailState';
  import { guestAddressObservation } from '../lib/guestAddress';
  import { createDetailInspection, detailManifestYAML } from '../lib/detailInspection';
  import type { DetailLease } from '../lib/detailInspection';
  import { createDeleteAttempt } from '../lib/deletionAttempt';
  import { focusOpsDrawer } from '../lib/opsDrawer';
  // Shown on disabled mutation controls for a read-only (viewer) session.
  const readOnlyTitle = 'Read-only session — requires editor or admin role';
  import { watchResources } from '../lib/watch';
  import { statusBadge } from '../lib/format';
  import { routeHref, navigate } from '../lib/router';
  import { ops as opsStore } from '../lib/ops';
  import HistoryDiff from '../components/HistoryDiff.svelte';
  import DagView from '../components/DagView.svelte';

  export let apiVersion: string;
  export let kind: string;
  export let resourceName: string;

  const inspection = createDetailInspection(() => {
    childController?.abort();
    childStates = {};
    operationHistory = null;
    historyError = '';
    actionSpecs = [];
    actionMsg = '';
    actionErr = '';
    migrationMessage = '';
    migrationError = '';
    copyMsg = '';
    exportedBundle = null;
    cancelRuntimeActionReview();
    auth.set({ kind: 'signed-out' });
  });
  // Uncertain Delete admission stays recovery-only. A missing resource or
  // declaration never establishes success or authorizes a new request identity.
  const deletion = createDeleteAttempt({
    storage: () => sessionStorage,
    canDelete: () => inspection.canAct() && get(canEditDesired),
    canReviewNew: (operation) => inspection.canAct() && get(canEditDesired)
      && (operation.status !== 'succeeded' || !!get(inspection).data?.applied),
    refreshAfterTerminal: () => inspection.refresh(),
    confirm: (target, newAttempt) => prompt(newAttempt
      ? `Review a NEW deletion of ${target.kind} ${target.name}?\n\nThis deletes the currently observed resource with a fresh request ID. It is NOT a replay of the previous Delete.\n\nType the name to confirm this NEW deletion:`
      : `Delete ${target.kind} ${target.name}?\n\nType the name to confirm:`),
    onUnauthorized: () => inspection.unauthorized(),
  });
  $: data = $inspection.data;
  $: saved = $inspection.saved;
  $: loading = $inspection.loading;
  $: error = $inspection.error;
  $: fresh = $inspection.observation === 'fresh' && !!data;
  $: inspectionSignal = data ? inspection.capture()?.signal : undefined;
  let graphVersion = 0;
  let watching = '';
  let childController: AbortController | null = null;
  let childStates: Record<string, ChildState | null> = {};
  const unsubscribeDeleteAuth = auth.subscribe((state) => {
    if (watching && state.kind === 'signed-out') {
      inspection.invalidate(new Error('Sign in and refresh to inspect the resource again'));
    }
    if (watching) void deletion.open(
      { apiVersion, kind, name: resourceName },
      state.kind === 'signed-in' ? state.me : null,
    );
  });

  function childKey(ref: ResourceRef): string {
    return `${ref.apiVersion}/${ref.kind}/${ref.name}`;
  }

  $: for (const operation of $opsStore) deletion.observe(operation);

  $: switchTo(apiVersion, kind, resourceName);

  function switchTo(av: string, k: string, n: string) {
    const target = `${av}/${k}/${n}`;
    if (target === watching) return;
    watching = target;
    childController?.abort();
    childStates = {};
    graphVersion = 0;
    exportedBundle = null;
    secretAcknowledged = false;
    migrationMessage = '';
    migrationError = '';
    migrating = false;
    actionSpecs = [];
    actionInflight = '';
    actionMsg = '';
    actionErr = '';
    copyMsg = '';
    operationHistory = null;
    historyLoading = false;
    historyError = '';
    lastSeenOpId = '';
    cancelRuntimeActionReview();
    released = false;
    inspection.open({ apiVersion: av, kind: k, name: n });
    const session = get(auth);
    void deletion.open({ apiVersion: av, kind: k, name: n }, session.kind === 'signed-in' ? session.me : null);
    const lease = inspection.capture();
    if (lease) {
      void subscribe(av, k, n, lease);
    }
  }

  $: if (data) {
    graphVersion += 1;
    const lease = inspection.capture();
    if (lease) {
      void loadChildStates(data.resource, lease);
      void loadActions(lease.target.apiVersion, lease.target.kind, lease);
    }
  } else {
    childController?.abort();
    childStates = {};
    cancelRuntimeActionReview();
    exportedBundle = null;
    secretAcknowledged = false;
  }

  async function loadChildStates(parent: Resource, lease: DetailLease) {
    childController?.abort();
    const childRequest = new AbortController();
    childController = childRequest;
    const abort = () => childRequest.abort();
    lease.signal.addEventListener('abort', abort, { once: true });
    const refs = parent.children ?? [];
    const seed: Record<string, ChildState | null> = {};
    for (const ref of refs) seed[childKey(ref)] = null;
    childStates = seed;
    try {
      await Promise.all(refs.map(async (ref) => {
        try {
          const r = await resources.get(ref.apiVersion, ref.kind, ref.name, { signal: childRequest.signal });
          if (!inspection.current(lease, true) || childRequest.signal.aborted) return;
          childStates = { ...childStates, [childKey(ref)]: {
            badge: statusBadge(r.resource),
            driftCount: r.resource.drift?.length ?? 0,
          } };
        } catch (err) {
          if (!inspection.current(lease) || childRequest.signal.aborted) return;
          if (err instanceof UnauthorizedError) { inspection.unauthorized(); return; }
          if (!inspection.current(lease, true)) return;
          childStates = { ...childStates, [childKey(ref)]: {
            badge: { label: '—', tone: 'unknown' },
            driftCount: 0,
            error: err instanceof Error ? err.message : String(err),
          } };
        }
      }));
    } finally {
      lease.signal.removeEventListener('abort', abort);
    }
  }

  async function subscribe(av: string, k: string, n: string, lease: DetailLease) {
    let backoffMs = 1000;
    while (inspection.current(lease)) {
      try {
        await watchResources(av, k, n, () => {
          if (inspection.current(lease)) void inspection.refresh();
        }, { signal: lease.signal });
        if (!inspection.current(lease)) return;
        inspection.invalidate(new Error('Resource watch disconnected'));
        backoffMs = 1000;
      } catch (err) {
        if (!inspection.current(lease)) return;
        inspection.invalidate(err);
        if (!inspection.current(lease)) return;
        backoffMs = Math.min(backoffMs * 2, 15_000);
      }
      const delay = Promise.withResolvers<void>();
      const timer = setTimeout(delay.resolve, backoffMs);
      const abort = () => { clearTimeout(timer); delay.resolve(); };
      lease.signal.addEventListener('abort', abort, { once: true });
      await delay.promise;
      lease.signal.removeEventListener('abort', abort);
      if (inspection.current(lease)) void inspection.refresh();
    }
  }

  onDestroy(() => {
    childController?.abort();
    inspection.stop();
    unsubscribeDeleteAuth();
    unsubscribeRuntimeActionAuth();
    unsubscribeRuntimeActionInspection();
    deletion.close();
  });

  // Pretty-print JSON for the desired/observed panes. The proto wire
  // format is JSON-on-the-fence anyway (grpc-gateway), and the editor
  // in U4 will switch to Monaco-YAML; for read-only display, JSON is the
  // honest representation.
  function pretty(v: unknown): string {
    if (v === undefined || v === null) return '—';
    try {
      return JSON.stringify(v, null, 2);
    } catch {
      return String(v);
    }
  }

  function timeAgo(rfc3339: string | undefined): string {
    if (!rfc3339) return '';
    const t = new Date(rfc3339);
    if (Number.isNaN(t.getTime())) return rfc3339;
    const seconds = Math.floor((Date.now() - t.getTime()) / 1000);
    if (seconds < 60) return `${seconds}s ago`;
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
    if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
    return `${Math.floor(seconds / 86400)}d ago`;
  }

  $: displayState = data ? statusBadge(data.resource) : null;
  $: appChecks = apiVersion === 'apps.openctl.io/v1' ? applicationChecks(data?.resource.status) : [];

  $: guestAddress = fresh && $auth.kind === 'signed-in'
    ? guestAddressObservation(data?.resource.status) : null;
  // Address evidence has one conservative presentation, not a conflicting raw
  // status.ip fallback from older controllers.
  $: observedStatus = apiVersion === 'proxmox.openctl.io/v1' && kind === 'VirtualMachine'
    ? Object.fromEntries(Object.entries(data?.resource.status ?? {})
      .filter(([key]) => key !== 'ip' && key !== 'ipObservation'))
    : data?.resource.status;
  $: standaloneVM = apiVersion === 'proxmox.openctl.io/v1' && kind === 'VirtualMachine'
    && !!data?.applied && !data.resource.metadata.ownerRefs?.length && !data.resource.children?.length;

  let exportedBundle: MigrationBundle | null = null;
  let secretAcknowledged = false;
  let migrating = false;
  let migrationMessage = '';
  let migrationError = '';
  let released = false;

  async function exportBundle() {
    if (!inspection.canAct() || !$canManageOwnership || !standaloneVM || !secretAcknowledged || migrating || activeOp) return;
    const lease = inspection.capture();
    if (!lease) return;
    migrating = true;
    migrationError = '';
    migrationMessage = '';
    exportedBundle = null;
    try {
      const response = await ownership.export(apiVersion, kind, resourceName, { signal: lease.signal });
      if (!inspection.current(lease, true)) return;
      const url = URL.createObjectURL(new Blob([JSON.stringify(response.bundle, null, 2)], { type: 'application/json' }));
      const link = document.createElement('a');
      link.href = url;
      link.download = `${resourceName}-migration.json`;
      document.body.appendChild(link);
      link.click();
      link.remove();
      URL.revokeObjectURL(url);
      exportedBundle = response.bundle;
      migrationMessage = 'Bundle download requested. Verify it is safely saved before releasing tracking. Export did not change ownership or prove destination adoption.';
    } catch (err) {
      if (!inspection.current(lease)) return;
      if (err instanceof UnauthorizedError) { inspection.unauthorized(); return; }
      if (inspection.current(lease, true)) migrationError = err instanceof Error ? err.message : String(err);
    } finally {
      if (inspection.current(lease)) migrating = false;
    }
  }

  async function forgetVM() {
    if (!inspection.canAct() || !$canManageOwnership || !standaloneVM || !exportedBundle || !secretAcknowledged || migrating || activeOp) return;
    const lease = inspection.capture();
    if (!lease) return;
    const typed = prompt(`Release controller tracking for ${resourceName}?\n\nThis is NOT Delete. The VM stays intact and its power state is unchanged. This controller stops managing it; destination adoption has not been proven. Keep the exported bundle safely saved.\n\nType the resource name to confirm:`);
    if (typed === null) return;
    if (typed !== resourceName) {
      migrationError = 'Name did not match; release cancelled.';
      return;
    }
    if (!inspection.current(lease, true) || !$canManageOwnership || !exportedBundle) return;
    migrating = true;
    migrationError = '';
    try {
      const response = await ownership.forget(exportedBundle, { signal: lease.signal });
      if (!inspection.current(lease, true)) return;
      inspection.stop();
      childController?.abort();
      released = true;
      migrationMessage = response.message || 'Controller tracking released. The provider VM remains intact.';
    } catch (err) {
      if (!inspection.current(lease)) return;
      if (err instanceof UnauthorizedError) { inspection.unauthorized(); return; }
      if (inspection.current(lease, true)) migrationError = err instanceof Error ? err.message : String(err);
    } finally {
      if (inspection.current(lease)) migrating = false;
    }
  }

  // Reconciliation uses the editor's fresh preview and durable Apply protocol.
  // Navigating here grants no destructive consent and submits no operation.
  function reviewReconcile() {
    if (!inspection.canAct() || !data?.applied || !$canEditDesired) return;
    navigate({ name: 'edit', apiVersion, kind, resourceName, reconcile: true });
  }

  // Every runtime action needs transient consent tied to this observation.
  let actionSpecs: ActionSpec[] = [];
  let actionInflight = '';
  let actionMsg = '';
  let actionErr = '';

  interface RuntimeActionReview {
    spec: ActionSpec;
    lease: DetailLease;
    values: Record<string, string>;
    error: string;
  }
  let runtimeActionReview: RuntimeActionReview | null = null;
  const unsubscribeRuntimeActionAuth = auth.subscribe(() => cancelRuntimeActionReview());
  const unsubscribeRuntimeActionInspection = inspection.subscribe(() => {
    if (runtimeActionReview && !inspection.current(runtimeActionReview.lease, true)) cancelRuntimeActionReview();
  });

  function runtimeActionAllowed(lease: DetailLease): boolean {
    return inspection.current(lease, true) && get(canMutate) && get(auth).kind !== 'signed-out'
      && !actionInflight && lease.target.apiVersion === apiVersion
      && lease.target.kind === kind && lease.target.name === resourceName;
  }

  function openRuntimeActionReview(spec: ActionSpec) {
    const lease = inspection.capture();
    if (!lease || !runtimeActionAllowed(lease)) return;
    const values: Record<string, string> = {};
    for (const p of spec.parameters ?? []) values[p.name] = p.defaultValue ?? '';
    runtimeActionReview = { spec, lease, values, error: '' };
  }

  function cancelRuntimeActionReview() {
    runtimeActionReview = null;
  }

  async function loadActions(av: string, k: string, lease: DetailLease) {
    try {
      const resp = await resources.listActions(av, k, { signal: lease.signal });
      if (!inspection.current(lease, true)) return;
      // Prefer the richer specs; fall back to bare names for older servers.
      actionSpecs = resp.actionSpecs ?? (resp.actions ?? []).map((name) => ({ name }));
    } catch (err) {
      if (!inspection.current(lease)) return;
      if (err instanceof UnauthorizedError) { inspection.unauthorized(); return; }
      if (inspection.current(lease, true)) actionSpecs = [];
    }
  }

  async function submitRuntimeActionReview(review: RuntimeActionReview) {
    if (runtimeActionReview !== review || !runtimeActionAllowed(review.lease)) return;
    for (const p of review.spec.parameters ?? []) {
      if (p.required && !(review.values[p.name] ?? '').trim()) {
        review.error = `${p.name} is required`;
        runtimeActionReview = review;
        return;
      }
    }
    const { lease, spec } = review;
    const parameters = spec.parameters?.length ? { ...review.values } : undefined;
    const action = spec.name;
    const actor = get(auth);
    cancelRuntimeActionReview();
    actionInflight = action;
    actionMsg = '';
    actionErr = '';
    try {
      const resp = await resources.invokeAction(lease.target.apiVersion, lease.target.kind, lease.target.name, action, parameters, { signal: lease.signal });
      if (!inspection.current(lease, true) || get(auth) !== actor) return;
      // Dispatch on result shape: URL → open in new tab, download →
      // stream a file, otherwise show the message inline.
      if (resp.downloadFilename) {
        const blob = new Blob([resp.downloadContent ?? ''], { type: 'application/octet-stream' });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = resp.downloadFilename;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
        actionMsg = `Downloaded ${resp.downloadFilename}`;
      } else if (resp.url) {
        window.open(resp.url, '_blank', 'noopener,noreferrer');
        actionMsg = resp.message || `Opened ${action}`;
      } else {
        actionMsg = resp.message
          ? `${action} → ${resp.message}`
          : `${action} submitted`;
      }
      // The refresh is tied to this route; no delayed callback can refresh a
      // different resource after navigation.
      if (!resp.url && !resp.downloadContent && !resp.downloadFilename) void inspection.refresh();
    } catch (err) {
      if (!inspection.current(lease) || get(auth) !== actor) return;
      if (err instanceof UnauthorizedError) { inspection.unauthorized(); return; }
      if (inspection.current(lease, true)) actionErr = err instanceof Error ? err.message : String(err);
    } finally {
      if (inspection.current(lease)) actionInflight = '';
    }
  }

  function inspectDeleteOperation() {
    const attempt = $deletion.attempt;
    if (!attempt?.operationId) return;
    focusOpsDrawer({ apiVersion, kind, resourceName, opId: attempt.operationId });
  }

  // U8.17: live progress on the detail page. Subscribe to the global
  // ops store and surface any in-flight op matching this resource as
  // an inline banner. When the op completes, we auto-refetch to catch
  // up before the reconciler tick and then drop the banner. Composite
  // resources also match children whose ops are still running.
  $: activeOp = $auth.kind === 'signed-in' && $inspection.observation !== 'unauthorized' ? $opsStore.find((o) => {
    if (o.apiVersion !== apiVersion || o.kind !== kind) return false;
    if (o.resourceName !== resourceName) return false;
    return o.status === 'pending' || o.status === 'running';
  }) ?? null : null;

  // When the active op transitions to terminal (out of pending/running),
  // refetch once so status catches up promptly.
  let lastSeenOpId = '';
  $: {
    const id = activeOp?.id ?? '';
    if (id) {
      lastSeenOpId = id;
    } else if (lastSeenOpId) {
      // Just cleared — refetch to see the result.
      void inspection.refresh();
      lastSeenOpId = '';
    }
  }

  let operationHistory: Operation[] | null = null;
  let historyLoading = false;
  let historyError = '';
  async function inspectHistory() {
    const lease = inspection.capture();
    if (!lease || historyLoading) return;
    historyLoading = true;
    historyError = '';
    try {
      const result = await operations.list({
        apiVersion: lease.target.apiVersion, kind: lease.target.kind,
        resourceName: lease.target.name, limit: 100,
      }, { signal: lease.signal });
      if (inspection.current(lease)) operationHistory = result.operations ?? [];
    } catch (err) {
      if (!inspection.current(lease)) return;
      if (err instanceof UnauthorizedError) { inspection.unauthorized(); return; }
      historyError = err instanceof Error ? err.message : String(err);
    } finally {
      if (inspection.current(lease)) historyLoading = false;
    }
  }

  // Read-only exports prefer raw persisted intent, including unresolved markers.
  let copyMsg = '';
  async function copyYAML() {
    const lease = inspection.capture();
    const y = detailManifestYAML(saved, data);
    if (!lease || !y) return;
    try {
      await navigator.clipboard.writeText(y);
      if (!inspection.current(lease)) return;
      copyMsg = 'Copied.';
    } catch (err) {
      if (!inspection.current(lease)) return;
      copyMsg = `Copy failed: ${err instanceof Error ? err.message : String(err)}`;
    }
  }
  function downloadYAML() {
    const y = detailManifestYAML(saved, data);
    if (!y) return;
    const blob = new Blob([y], { type: 'application/x-yaml' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${kind.toLowerCase()}-${resourceName}.yaml`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
  }

  // Current desired manifest YAML — the right-hand side of the History diff.
  $: appliedYamlStr = detailManifestYAML(saved, data);

  $: childTotal = data?.resource.children?.length ?? 0;
  $: childSummary = childHealth(data?.resource.children ?? [], childStates);
</script>

<section>
  <header>
    <div>
      <p class="crumbs">
        <a href={routeHref({ name: 'list', apiVersion, kind })}>{kind}</a>
        <span> · </span>
        <span class="path">{apiVersion}</span>
      </p>
      <h2>{resourceName}</h2>
    </div>
    <div class="header-right">
      {#if !released}
      {#if displayState}
        <span class="state state-{displayState.tone}">{displayState.label}</span>
      {:else}
        <span class="state state-unknown">{loading ? 'Observing / unknown' : 'Observation unknown'}</span>
      {/if}
      {#if childTotal > 0}
        <span class="state state-{childSummary.tone}" title="Aggregated child status; unknown or loading children are not healthy">
          {childTotal} {childTotal === 1 ? 'child' : 'children'}
          {#if childSummary.drifted > 0} · {childSummary.drifted} drifted{/if}
          {#if childSummary.unhealthy > 0} · {childSummary.unhealthy} unhealthy{/if}
          {#if childSummary.unknown > 0} · {childSummary.unknown} unknown / loading{/if}
        </span>
      {/if}
      {#each actionSpecs as a}
        <button
          type="button"
          class="action-btn"
          disabled={!fresh || !!actionInflight || !$canMutate}
          title={!fresh ? 'Requires fresh provider observation' : $canMutate ? (a.description || `Invoke '${a.name}' on ${resourceName}`) : readOnlyTitle}
          on:click={() => openRuntimeActionReview(a)}
        >
          {actionInflight === a.name ? '…' : (a.label || a.name)}
        </button>
      {/each}
      <button
        type="button"
        class="reconcile-btn"
        disabled={!fresh || !data?.applied || !$canEditDesired}
        title={!fresh ? 'Requires fresh provider observation' : !$canEditDesired
          ? $desiredMutationHint
          : data?.applied
            ? 'Review stored intent and live drift before submitting a recoverable Apply'
            : 'No applied manifest on file — nothing to reconcile from'}
        on:click={reviewReconcile}
      >
        Reconcile
      </button>
      {#if $canEditDesired}
        {#if fresh}
          <a class="edit-btn" href={routeHref({ name: 'edit', apiVersion, kind, resourceName })} on:click={(event) => { if (!inspection.canAct() || !$canEditDesired) event.preventDefault(); }}>Edit</a>
        {:else}
          <button type="button" class="edit-btn" disabled title="Requires fresh provider observation">Edit</button>
        {/if}
        <button
          type="button"
          class="delete-btn"
          disabled={!fresh || !$deletion.ready || !!$deletion.busy || !!$deletion.attempt}
          title={!fresh ? 'Requires fresh provider observation' : $deletion.attempt ? 'Recover the existing request; another Delete is blocked' : 'Delete this resource with durable request recovery'}
          on:click={() => deletion.submit()}
        >{$deletion.busy === 'submitting' ? 'Submitting delete…' : 'Delete'}</button>
      {/if}
      {/if}
    </div>
  </header>

  {#if $deletion.error}
    <p class="err small" role="alert">{$deletion.error}</p>
  {/if}
  {#if $auth.kind === 'signed-in'}
    {#if $deletion.attempt}
      <article class="card">
        <div class="card-head">
          <h3>Delete request</h3>
          <div class="card-actions">
            <button type="button" class="mini-btn" disabled={!!$deletion.busy || !$deletion.ready} on:click={() => deletion.recover()}>
              {$deletion.busy === 'recovering' ? 'Recovering…' : 'Recover request'}
            </button>
            {#if $deletion.attempt.operationId}
              <button type="button" class="mini-btn" on:click={inspectDeleteOperation}>Inspect delete operation</button>
            {/if}
            {#if $deletion.reviewable && $canEditDesired}
              <button type="button" class="mini-btn" disabled={!!$deletion.busy || !fresh || ($deletion.operation?.status === 'succeeded' && !data?.applied)} on:click={() => deletion.reviewNew()}>Review new delete attempt</button>
            {/if}
          </div>
        </div>
        <p class="small">Request ID: <code>{$deletion.attempt.requestId}</code></p>
        {#if $deletion.attempt.operationId}
          <p class="small">Operation ID: <code>{$deletion.attempt.operationId}</code></p>
        {/if}
        {#if $deletion.operation}
          <p role="status">Delete operation: <strong>{$deletion.operation.status}</strong></p>
          {#if $deletion.operation.error}<p class="err">{$deletion.operation.error}</p>{/if}
          {#if $deletion.operation.status === 'succeeded'}
            <p class="muted small">The Delete operation succeeded. A missing resource alone is not proof of success.</p>
          {:else if ['failed', 'interrupted', 'canceled'].includes($deletion.operation.status)}
            <p class="muted small">This Delete did not succeed. Inspect its operation and current resource before considering any new deletion.</p>
          {/if}
        {:else}
          <p class="muted small">{$deletion.attempt.operationId ? 'Delete acknowledged; operation outcome not yet observed.' : 'Delete admission is uncertain or not yet acknowledged.'}</p>
        {/if}
        <p class="muted small">Recovery is read-only and works without a live resource or saved declaration. Unknown or active attempts never resubmit Delete. A new deletion requires confirmed terminal recovery, a fresh provider observation and new typed consent; a succeeded attempt also requires a currently tracked declaration. Leaving this page does not cancel accepted work.</p>
      </article>
    {:else if !$deletion.ready}
      <button type="button" class="mini-btn" disabled={!!$deletion.busy} on:click={() => deletion.verify()}>
        {$deletion.busy === 'binding' ? 'Verifying delete recovery…' : 'Verify delete recovery'}
      </button>
    {/if}
  {/if}

  {#if activeOp}
    <article class="op-banner op-{activeOp.status}">
      <span class="op-dot" class:running={activeOp.status === 'running'}></span>
      <span>
        <strong>{activeOp.type}</strong> — {activeOp.status}
        {#if activeOp.label}· {activeOp.label}{/if}
      </span>
      <code class="mono op-id">{activeOp.id.slice(0, 12)}</code>
    </article>
  {/if}

  {#each runtimeActionReview && fresh ? [runtimeActionReview] : [] as review (review)}
      <form class="runtime-action-review" aria-label="Review runtime action" on:submit|preventDefault={() => submitRuntimeActionReview(review)}>
        <div class="runtime-action-review-head">Review {review.spec.label || review.spec.name}</div>
        <p class="small">API version: <code>{review.lease.target.apiVersion}</code><br />
          Kind: <code>{review.lease.target.kind}</code><br />
          Resource name: <code>{review.lease.target.name}</code><br />
          Action ID: <code>{review.spec.name}</code></p>
        {#if review.spec.description}
          <p class="muted small">{review.spec.description}</p>
        {/if}
        <p class="muted small">This runs a live runtime action, not saved desired intent or durable Apply. There are no automatic retries; inspect an uncertain outcome before repeating. Downloads and URLs may contain sensitive information.</p>
        {#each review.spec.parameters ?? [] as p}
          <label class="runtime-action-field">
            <span class="runtime-action-label">{p.name}{#if p.required}<span class="req">*</span>{/if}</span>
            {#if p.type === 'bool'}
              <select value={review.values[p.name]} on:change={(event) => { review.values[p.name] = event.currentTarget.value; }} required={p.required}>
                <option value="">Select a value</option>
                <option value="true">true</option>
                <option value="false">false</option>
              </select>
            {:else}
              <input
                type={p.type === 'int' ? 'number' : 'text'}
                value={review.values[p.name]}
                on:input={(event) => { review.values[p.name] = event.currentTarget.value; }}
                placeholder={p.description || ''}
                required={p.required}
              />
            {/if}
            {#if p.description}<span class="runtime-action-help muted small">{p.description}</span>{/if}
          </label>
        {/each}
        {#if review.error}<p class="err small" role="alert">{review.error}</p>{/if}
        <div class="runtime-action-review-actions">
          <button type="submit" class="action-btn" disabled={!fresh || !$canMutate || !!actionInflight}>
            Run {review.spec.label || review.spec.name}
          </button>
          <button type="button" class="runtime-action-cancel" on:click={() => { if (runtimeActionReview === review) cancelRuntimeActionReview(); }}>Cancel</button>
        </div>
      </form>
  {/each}
  {#if actionMsg}
    <p class="muted small">{actionMsg}</p>
  {/if}
  {#if actionErr}
    <p class="err small">{actionErr}</p>
  {/if}

  {#if !released && $inspection.observation !== 'unauthorized'}
    <article class="card">
      <div class="card-head">
        <h3>Provider observation</h3>
        <button type="button" class="mini-btn" disabled={loading} on:click={() => inspection.refresh()}>Retry observation</button>
      </div>
      {#if loading}
        <p class="muted" role="status">Observing provider… Readiness, drift and ownership are unknown until Get succeeds.</p>
      {:else if error}
        <p class="err" role="alert">{error}</p>
        <p class="muted small">Readiness, drift and ownership are unknown. Mutations are disabled; saved intent is not a replacement instruction.</p>
      {:else}
        <p class="muted small">Live Get succeeded. Saved intent remains separate from this provider observation.</p>
      {/if}
    </article>

    {#if apiVersion === 'proxmox.openctl.io/v1' && kind === 'VirtualMachine'}
      <article class="card" aria-label="Guest address">
        <div class="card-head">
          <h3>Guest address</h3>
          <span class="state state-unknown">{guestAddress?.state === 'available'
            ? 'Address observed' : guestAddress?.state === 'unavailable'
              ? 'Address unavailable' : 'Address observation unknown'}</span>
        </div>
        {#if guestAddress?.state === 'available'}
          <p>Observed address: <code>{guestAddress.ip}</code></p>
        {:else if guestAddress?.state === 'unavailable'}
          <p><code>{guestAddress.code}</code> — {guestAddress.message}</p>
        {:else}
          {#if guestAddress?.code}<p>Reason code: <code>{guestAddress.code}</code></p>{/if}
          <p class="muted">No current guest address evidence. Inspect VM power, configured networking and guest-agent support, then retry the provider observation.</p>
        {/if}
        <p class="muted small">Reachability unverified. An observed address does not prove SSH access or service readiness. IP-dependent application setup needs address evidence and separate access checks.</p>
      </article>
    {/if}

    {#if saved}
      <article class="card">
        <div class="card-head">
          <h3>Saved desired intent</h3>
          <div class="card-actions">
            <button type="button" class="mini-btn" on:click={copyYAML}>Copy YAML</button>
            <button type="button" class="mini-btn" on:click={downloadYAML}>Download YAML</button>
            {#if copyMsg}<span class="muted small">{copyMsg}</span>{/if}
          </div>
        </div>
        <p class="muted small">Read-only persisted declaration, not observed readiness or proof of ownership. Unresolved markers are preserved; inspection does not resolve secrets or write state.</p>
        <p class="muted small">Source: <span class="path">{saved.source || 'Not recorded'}</span> · Applied at: <span class="path">{saved.appliedAt || 'Not recorded'}</span>{#if saved.appliedAt} ({timeAgo(saved.appliedAt)}){/if}</p>
        <pre>{pretty({ apiVersion: saved.resource.apiVersion, kind: saved.resource.kind, metadata: saved.resource.metadata, spec: saved.resource.spec })}</pre>
      </article>
    {:else if $inspection.savedLoading}
      <p class="muted" role="status">Loading saved declaration independently of provider observation…</p>
    {:else if $inspection.savedError}
      <p class="muted small">{$inspection.savedError}</p>
    {/if}

    <article class="card">
      <div class="card-head">
        <h3>Operation history</h3>
        <button type="button" class="mini-btn" disabled={historyLoading} on:click={inspectHistory}>{historyLoading ? 'Loading…' : 'Inspect operation history'}</button>
      </div>
      <p class="muted small">Historical operations are read-only evidence, not current provider readiness or authority to retry.</p>
      {#if historyError}<p class="err" role="alert">{historyError}</p>{/if}
      {#if operationHistory}
        {#if operationHistory.length === 0}<p class="muted">No recorded operations for this resource.</p>{/if}
        {#each operationHistory as op}
          <p class="small"><code>{op.id}</code> · {op.type} · {op.status} · {op.submittedAt || 'Time not recorded'}{#if op.error} · {op.error}{/if}</p>
        {/each}
      {/if}
    </article>
  {/if}

  {#if released}
    <article class="card">
      <h3>Tracking released — VM kept</h3>
      <p role="status">{migrationMessage}</p>
      <p>Release is not Delete. The VM remains at the provider; destination adoption has not been proven.</p>
      <a href={routeHref({ name: 'discover' })}>Discover or import the bundle at a destination controller</a>
    </article>
  {:else if fresh && data}
    {@const drift = data.resource.drift ?? []}
    {@const lastApplied = data.appliedAt}

    {#if lastApplied}
      <p class="muted small">
        Last applied {timeAgo(lastApplied)}
        <span class="path"> · {lastApplied}</span>
      </p>
    {:else}
      <p class="muted small">
        No applied manifest on file — this resource was created out-of-band.
      </p>
    {/if}

    {#if standaloneVM}
      <article class="card">
        <h3>Export and release tracking</h3>
        <p>Export preserves ownership. Release tracking is <strong>not Delete</strong>: it keeps the provider VM intact but stops this controller managing it. Neither action proves adoption at a destination controller.</p>
        <p class="muted small">Requires an admin, an idle controller, and manifest GitOps watch/pull disabled. Owned VMs, managed references, or opaque provider state may block migration; the server enforces all safeguards.</p>
        {#if $canManageOwnership}
          <label class="migration-confirm"><input type="checkbox" bind:checked={secretAcknowledged} disabled={migrating} /> I understand the downloaded bundle may contain literal cloud-init credentials and other secrets; I will store and transfer it securely.</label>
          <div class="card-actions">
            <button class="action-btn" disabled={migrating || !secretAcknowledged || !!activeOp} on:click={exportBundle}>{migrating ? 'Working…' : 'Export bundle'}</button>
            <button class="action-btn destructive" disabled={migrating || !secretAcknowledged || !exportedBundle || !!activeOp} on:click={forgetVM}>Release tracking (keep VM)</button>
          </div>
          {#if migrationMessage}<p class="muted small" role="status">{migrationMessage}</p>{/if}
          {#if migrationError}<p class="err small" role="alert">{migrationError}</p>{/if}
        {:else}<p class="muted">Export/release requires admin access and controller-owned intent with GitOps disabled.</p>{/if}
      </article>
    {/if}

    {#if appChecks.length > 0}
      <article class="card">
        <h3>Application checks</h3>
        {#if typeof data.resource.status?.message === 'string'}<p>{data.resource.status.message}</p>{/if}
        <p class="muted small">Checks show individual readiness, not just whether the VM is running. An unconfigured public endpoint does not imply the application itself is unhealthy.</p>
        <div class="table-scroll">
          <table>
            <thead><tr><th>Check</th><th>Readiness</th><th>Details</th></tr></thead>
            <tbody>
              {#each appChecks as check}
                {@const badge = applicationCheckBadge(check.state)}
                <tr><td>{check.name}</td><td><span class="state state-{badge.tone}">{badge.label}</span></td><td>{check.message || 'No details reported'}</td></tr>
              {/each}
            </tbody>
          </table>
        </div>
      </article>
    {/if}
    {#if drift.length > 0}
      <article class="card drift-card">
        <h3>Drift ({drift.length})</h3>
        <div class="table-scroll">
        <table>
          <thead>
            <tr><th>Path</th><th>Desired</th><th>Observed</th></tr>
          </thead>
          <tbody>
            {#each drift as d}
              <tr>
                <td class="mono">{d.path}</td>
                <td class="mono">{d.desired}</td>
                <td class="mono">{d.observed}</td>
              </tr>
            {/each}
          </tbody>
        </table>
        </div>
      </article>
    {/if}

    <div class="grid">
      {#if !saved}
      <article class="card">
        <div class="card-head">
          <h3>Desired manifest</h3>
          <div class="card-actions">
            <button type="button" class="mini-btn" on:click={copyYAML} title="Copy YAML to clipboard">
              Copy YAML
            </button>
            <button type="button" class="mini-btn" on:click={downloadYAML} title="Download as .yaml file">
              Download
            </button>
            {#if copyMsg}<span class="muted small">{copyMsg}</span>{/if}
          </div>
        </div>
        {#if data.applied}
          <pre>{pretty({
              apiVersion: data.applied.apiVersion,
              kind: data.applied.kind,
              metadata: data.applied.metadata,
              spec: data.applied.spec,
            })}</pre>
        {:else}
          <p class="muted">No manifest on file.</p>
        {/if}
      </article>
      {/if}

      <article class="card">
        <h3>Observed state</h3>
        <pre>{pretty(observedStatus)}</pre>
      </article>
    </div>

    {@const ownerRefs = data.resource.metadata?.ownerRefs ?? []}
    {@const children = data.resource.children ?? []}

    {#if ownerRefs.length > 0}
      <article class="card owner-card">
        <h3>Owner</h3>
        <p class="muted small">
          This resource is owned by another and can only be modified by
          editing its owner. To change anything here, open the owner instead.
        </p>
        <ul class="ref-list">
          {#each ownerRefs as owner}
            <li>
              <a href={routeHref({ name: 'detail', apiVersion: owner.apiVersion, kind: owner.kind, resourceName: owner.name })}>
                <span class="ref-kind">{owner.kind}</span>
                <span class="ref-name">{owner.name}</span>
                <span class="path"> · {owner.apiVersion}</span>
              </a>
            </li>
          {/each}
        </ul>
      </article>
    {/if}

    {#key watching}
      <DagView {apiVersion} {kind} {resourceName} version={graphVersion} signal={inspectionSignal} onUnauthorized={inspection.unauthorized} />
    {/key}

    {#if children.length > 0}
      <article class="card">
        <h3>Children ({children.length})</h3>
        <p class="muted small">
          Composed resources are read-only — edit this resource to add,
          remove, or respec them.
        </p>
        <ul class="ref-list">
          {#each children as child}
            {@const s = childStates[childKey(child)]}
            <li>
              <a href={routeHref({ name: 'detail', apiVersion: child.apiVersion, kind: child.kind, resourceName: child.name })}>
                <span class="ref-kind">{child.kind}</span>
                <span class="ref-name">{child.name}</span>
                {#if s == null}
                  <span class="state state-unknown shimmer">loading / unknown</span>
                {:else if s?.error}
                  <span class="state state-bad" title={s.error}>error</span>
                {:else if s}
                  <span class="state state-{s.badge.tone}">{s.badge.label}</span>
                  {#if s.driftCount > 0}
                    <span class="state state-warn" title="{s.driftCount} field{s.driftCount === 1 ? '' : 's'} drifted">
                      drift {s.driftCount}
                    </span>
                  {/if}
                {/if}
                <span class="path"> · {child.apiVersion}</span>
              </a>
            </li>
          {/each}
        </ul>
      </article>
    {/if}

    {#key watching}
      <HistoryDiff {apiVersion} {kind} {resourceName} appliedYaml={appliedYamlStr} signal={inspectionSignal} onUnauthorized={inspection.unauthorized} />
    {/key}
  {/if}
</section>

<style>
  section {
    max-width: 72rem;
  }
  header {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 1rem;
    margin-bottom: 1rem;
  }
  .crumbs {
    margin: 0 0 0.25rem;
    color: #888;
    font-size: 0.85rem;
  }
  .crumbs a {
    color: #6ea8ff;
    text-decoration: none;
  }
  .crumbs a:hover {
    text-decoration: underline;
  }
  .path {
    font-family: ui-monospace, SFMono-Regular, monospace;
    font-size: 0.8em;
    color: #888;
  }
  h2 {
    margin: 0;
    font-size: 1.4rem;
  }
  h3 {
    margin: 0 0 0.75rem;
    font-size: 0.95rem;
    color: #aaa;
  }
  .header-right {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    flex-wrap: wrap;
  }
  .edit-btn {
    background: #4a8ef0;
    color: white;
    padding: 0.4em 1em;
    border-radius: 6px;
    font-size: 0.9rem;
    text-decoration: none;
    font-weight: 500;
  }
  .edit-btn:hover {
    background: #3a7ee0;
  }
  .reconcile-btn {
    background: transparent;
    color: #4a8ef0;
    border: 1px solid #4a8ef0;
    padding: 0.4em 1em;
    border-radius: 6px;
    font-size: 0.9rem;
    font-weight: 500;
    cursor: pointer;
  }
  .reconcile-btn:hover:not(:disabled) {
    background: rgba(74, 142, 240, 0.1);
  }
  .reconcile-btn:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
  .action-btn {
    background: transparent;
    color: #ccc;
    border: 1px solid rgba(127, 127, 127, 0.35);
    padding: 0.4em 0.9em;
    border-radius: 6px;
    font-size: 0.85rem;
    cursor: pointer;
  }
  .action-btn:hover:not(:disabled) {
    background: rgba(127, 127, 127, 0.12);
    color: #fff;
  }
  .action-btn:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
  .action-btn.destructive {
    color: #ff8980;
    border-color: rgba(255, 137, 128, 0.35);
  }
  .action-btn.destructive:hover:not(:disabled) {
    background: rgba(255, 137, 128, 0.1);
    color: #ff8980;
  }
  .runtime-action-review {
    margin: 0.75em 0;
    padding: 0.9em 1em;
    border: 1px solid rgba(127, 127, 127, 0.3);
    border-radius: 8px;
    background: rgba(127, 127, 127, 0.06);
    max-width: 32rem;
  }
  .runtime-action-review-head {
    font-weight: 600;
    margin-bottom: 0.4em;
  }
  .runtime-action-field {
    display: flex;
    flex-direction: column;
    gap: 0.2em;
    margin: 0.6em 0;
  }
  .runtime-action-label {
    font-size: 0.85rem;
    color: #ccc;
  }
  .runtime-action-label .req {
    color: #ff8980;
    margin-left: 0.15em;
  }
  .runtime-action-field input,
  .runtime-action-field select {
    padding: 0.4em 0.6em;
    border-radius: 6px;
    border: 1px solid rgba(127, 127, 127, 0.35);
    background: transparent;
    color: #eee;
    font: inherit;
  }
  .runtime-action-help {
    opacity: 0.75;
  }
  .runtime-action-review-actions {
    display: flex;
    gap: 0.5em;
    margin-top: 0.6em;
  }
  .runtime-action-cancel {
    background: transparent;
    color: #aaa;
    border: 1px solid rgba(127, 127, 127, 0.35);
    padding: 0.4em 0.9em;
    border-radius: 6px;
    font-size: 0.85rem;
    cursor: pointer;
  }
  .runtime-action-cancel:hover {
    color: #fff;
    background: rgba(127, 127, 127, 0.12);
  }
  .delete-btn {
    background: transparent;
    color: #ff8980;
    border: 1px solid rgba(255, 137, 128, 0.5);
    padding: 0.4em 1em;
    border-radius: 6px;
    font-size: 0.9rem;
    font-weight: 500;
    cursor: pointer;
  }
  .delete-btn:hover:not(:disabled) {
    background: rgba(255, 137, 128, 0.12);
  }
  .delete-btn:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
  .op-banner {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    padding: 0.55rem 0.9rem;
    border-radius: 6px;
    background: rgba(74, 142, 240, 0.08);
    border-left: 3px solid #4a8ef0;
    font-size: 0.85rem;
    margin: 0.5rem 0;
  }
  .op-dot {
    width: 0.6rem;
    height: 0.6rem;
    border-radius: 50%;
    background: #4a8ef0;
    flex-shrink: 0;
  }
  .op-dot.running {
    animation: pulse 1.2s ease-in-out infinite;
  }
  @keyframes pulse {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.4; }
  }
  .op-id {
    margin-left: auto;
    color: #888;
    font-size: 0.75rem;
  }
  .card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
    margin-bottom: 0.5rem;
  }
  .card-head h3 {
    margin: 0;
  }
  .card-actions {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    flex-wrap: wrap;
  }
  .mini-btn {
    background: transparent;
    color: #aaa;
    border: 1px solid rgba(127, 127, 127, 0.3);
    padding: 0.2em 0.7em;
    border-radius: 4px;
    font-size: 0.75rem;
    cursor: pointer;
  }
  .mini-btn:hover {
    color: #fff;
    background: rgba(127, 127, 127, 0.12);
  }
  .state {
    padding: 0.15em 0.7em;
    border-radius: 999px;
    font-size: 0.85rem;
    font-weight: 500;
  }
  .state-good { background: rgba(46, 160, 67, 0.18); color: #5fdb78; }
  .state-warn { background: rgba(255, 184, 0, 0.18); color: #ffce4d; }
  .state-bad  { background: rgba(248, 81, 73, 0.18); color: #ff8980; }
  .state-unknown { background: rgba(127, 127, 127, 0.18); color: #aaa; }
  .small {
    font-size: 0.85rem;
  }
  .muted {
    color: #888;
  }
  .err {
    color: #f57171;
  }
  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 1rem;
    margin: 1.25rem 0;
  }
  @media (max-width: 70rem) {
    .grid {
      grid-template-columns: 1fr;
    }
  }
  .card {
    background: #232323;
    border-radius: 8px;
    padding: 1.25rem 1.5rem;
    margin: 0 0 1rem;
  }
  .drift-card {
    border-left: 3px solid #ffce4d;
  }
  @media (prefers-color-scheme: light) {
    .card {
      background: #fff;
      box-shadow: 0 1px 4px rgba(0, 0, 0, 0.04);
    }
  }
  pre {
    margin: 0;
    padding: 0.75rem 1rem;
    background: rgba(0, 0, 0, 0.25);
    border-radius: 4px;
    overflow-x: auto;
    font-family: ui-monospace, SFMono-Regular, monospace;
    font-size: 0.8rem;
    line-height: 1.5;
  }
  @media (prefers-color-scheme: light) {
    pre {
      background: #f4f4f4;
    }
  }
  .mono {
    font-family: ui-monospace, SFMono-Regular, monospace;
    font-size: 0.85rem;
  }
  table {
    width: 100%;
    border-collapse: collapse;
  }
  th, td {
    text-align: left;
    padding: 0.4rem 0.6rem;
    border-bottom: 1px solid rgba(127, 127, 127, 0.18);
  }
  th {
    font-size: 0.75rem;
    color: #888;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .owner-card {
    border-left: 3px solid #6ea8ff;
  }
  .ref-list {
    list-style: none;
    margin: 0.5rem 0 0;
    padding: 0;
  }
  .ref-list li {
    padding: 0.4rem 0;
    border-bottom: 1px solid rgba(127, 127, 127, 0.12);
  }
  .ref-list li:last-child {
    border-bottom: none;
  }
  .ref-list a {
    display: inline-flex;
    align-items: baseline;
    gap: 0.5rem;
    color: inherit;
    text-decoration: none;
  }
  .ref-list a:hover .ref-name {
    text-decoration: underline;
  }
  .ref-kind {
    color: #aaa;
    font-size: 0.85rem;
  }
  .ref-name {
    color: #6ea8ff;
    font-weight: 500;
  }
  .ref-list .state {
    font-size: 0.72rem;
    padding: 0.1em 0.55em;
  }
  .migration-confirm { display: flex; align-items: baseline; gap: 0.6rem; margin: 1rem 0; }
  @media (max-width: 45rem) {
    header { align-items: flex-start; flex-direction: column; }
    .header-right { gap: 0.5rem; }
    .card-head { flex-wrap: wrap; }
  }
  .shimmer {
    opacity: 0.55;
  }
</style>
