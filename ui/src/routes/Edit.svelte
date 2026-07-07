<script lang="ts">
  import { onDestroy, setContext } from 'svelte';
  import { writable } from 'svelte/store';
  import {
    resources, schemas, operations as opsApi, UnauthorizedError,
    type DryRunApplyResponse,
  } from '../lib/api';
  import { parseYAML, resourceToYAML } from '../lib/yaml';
  import { advancedKind } from '../lib/catalogue';
  import { canMutate } from '../lib/auth';
  import { ops as opsStore } from '../lib/ops';
  import type { OperationRow } from '../lib/watch';
  import { routeHref, navigate } from '../lib/router';
  import { extraKeys, fromManifest, initialValue, scrubEmpty, type FormField as FF } from '../lib/formSchema';
  import MonacoEditor from '../components/MonacoEditor.svelte';
  import MonacoDiffEditor from '../components/MonacoDiffEditor.svelte';
  import FormField from '../components/FormField.svelte';

  export let apiVersion: string;
  export let kind: string;
  // Empty string = create mode (no existing resource to fetch). Edit
  // mode requires a non-empty name. The Shell route dispatch and the
  // routeHref builder keep these two cases distinct.
  export let resourceName = '';

  $: isCreate = resourceName === '';

  // Expose apiVersion via context so nested FormField instances can
  // resolve @options(kind="X") without an explicit apiVersion by
  // defaulting to the containing resource's provider. setContext must
  // fire during init, so we register a writable store once and update
  // it reactively as the apiVersion prop changes (route navigation
  // between kinds).
  const apiVersionCtx = writable(apiVersion);
  setContext('resourceAPIVersionStore', apiVersionCtx);
  $: apiVersionCtx.set(apiVersion);

  // Shown on the disabled Apply button for a read-only (viewer) session.
  const readOnlyTitle = 'Read-only session — requires editor or admin role';

  // Publish per-path validation errors so nested FormField instances
  // can highlight themselves without threading errors through props.
  // Keyed by dotted path (e.g. "spec.cpu.cores"); value is the CUE
  // message. Refreshed by runPreview on each debounce tick.
  const fieldErrorsCtx = writable<Record<string, string>>({});
  setContext('resourceFieldErrorsStore', fieldErrorsCtx);

  // Publish the current form value (the whole manifest object) so a nested
  // FormField with a `dependsOn` option (e.g. a disk's storage dropdown that
  // depends on spec.node) can read the value it depends on. Updated on every
  // form change and on load.
  const manifestCtx = writable<unknown>(null);
  setContext('resourceManifestStore', manifestCtx);
  $: manifestCtx.set(formState);

  // Captured at submit time in create mode so the success-handoff knows
  // where to navigate after the op succeeds — `resourceName` is empty
  // until then.
  let createdName = '';

  // Existing resource names for this kind (create mode only). Filled on
  // load and used to warn + block when the user types a name that's
  // already in use — without this, Apply silently upserts instead of
  // creating, which violates the Create intent.
  let existingNames = new Set<string>();

  let baseline = '';
  let text = '';
  let loading = true;
  let loadError = '';

  // Debounced preview: validate + dry-run-apply fire together so the
  // panel can show both diagnostics and the planned diff in one update.
  const PREVIEW_DEBOUNCE_MS = 350;
  let timer: number | undefined;
  let lastPreviewedText = '';
  let previewing = false;
  let parseError = '';
  let validateErrors: string[] = [];
  let plan: DryRunApplyResponse | null = null;

  // Gate checkboxes — keyed by gate string (e.g. "allow_destructive").
  let checkedGates: Record<string, boolean> = {};

  // Apply lifecycle. `applying` covers the submit roundtrip; once we
  // have an op id we switch to live op-progress mode driven off the
  // existing ops store so we don't double-subscribe to WatchOperations.
  let applying = false;
  let applyError = '';
  let liveOpId = '';

  // U6 composite-UX gate: when the resource being edited is owned by
  // another (e.g. a Cluster's member VM), editing is blocked with a
  // banner pointing back to the owner. Captured on load and stable for
  // the lifetime of the route.
  let ownerRefs: import('../lib/api').ResourceRef[] = [];
  // U6: live children of this resource (from Resource.children on load).
  // Used to resolve the apiVersion of a child action in the apply
  // preview — DryRunApply ChildAction carries only kind+name today, so
  // we look it up here to build the detail-page link.
  let liveChildren: import('../lib/api').ResourceRef[] = [];
  // U7 retry handoff: the op id we pre-filled from, surfaced as a
  // banner so the user knows they're re-applying a specific failed op
  // (and isn't surprised when the diff shows their changes vs the
  // applied baseline).
  let retryFromOpId = '';
  const RETRY_KEY = 'openctl.retryFromOp';

  // View toggle: 'form' is the typed-form view (U5.2), 'edit' is the
  // Monaco editor (default), 'diff' is the read-only side-by-side diff.
  // Form and edit share the same `text` state — typing in either
  // updates the other via the parse+stringify round-trip below.
  let view: 'form' | 'edit' | 'diff' = 'edit';

  // U8.20: manifest-preview toggle in the form view. Persisted in
  // localStorage so the preference sticks across sessions. Hidden by
  // default when the user has previously opted out; shown otherwise.
  const PREVIEW_KEY = 'openctl.form.showPreview';
  let showPreview = (() => {
    try {
      const v = localStorage.getItem(PREVIEW_KEY);
      return v === null ? true : v === '1';
    } catch {
      return true;
    }
  })();
  function togglePreview() {
    showPreview = !showPreview;
    try { localStorage.setItem(PREVIEW_KEY, showPreview ? '1' : '0'); } catch { /* ignore */ }
  }

  // Form schema lazy-loaded once on mount; null while fetching, and
  // also when the controller has no schema for this kind (form tab
  // stays disabled in that case).
  let formSchema: FF | null = null;
  let formSchemaError = '';
  let formState: unknown = null;
  // Suppress one round-trip when the user edits the form: form edit →
  // formState changes → derived YAML updates → text changes → preview
  // schedules. We don't want the resulting `text` change to re-seed
  // formState from itself (re-fromManifest would clobber in-progress
  // typing).
  let formDriving = false;

  $: void load(apiVersion, kind, resourceName);

  async function load(av: string, k: string, n: string) {
    loading = true;
    loadError = '';
    ownerRefs = [];
    retryFromOpId = '';
    createdName = '';
    try {
      if (n === '') {
        // Create mode: no remote fetch, no baseline. Seed the editor with
        // a minimal manifest stub; loadFormSchema below will replace it
        // with a schema-driven seed once the schema arrives, but only if
        // the user hasn't started typing.
        baseline = '';
        text = seedManifest(av, k);
        existingNames = new Set();
        schedulePreview();
        void loadFormSchema(av, k);
        void loadExistingNames(av, k);
        return;
      }
      const r = await resources.get(av, k, n);
      const applied = r.applied;
      if (applied) {
        baseline = resourceToYAML(applied);
      } else {
        baseline = resourceToYAML({
          apiVersion: av,
          kind: k,
          metadata: { name: n },
          spec: r.resource.spec ?? {},
        });
      }
      // U6: stash ownerRefs so the edit gate + banner can react to them.
      // We prefer ownerRefs off the observed resource (live state) so a
      // brand-new but already-owned resource still surfaces correctly.
      ownerRefs = r.resource.metadata?.ownerRefs ?? applied?.metadata?.ownerRefs ?? [];
      liveChildren = r.resource.children ?? [];
      text = baseline;
      // U7 retry handoff: if the user clicked Retry on an op in the
      // drawer, sessionStorage carries the op id. Pull the original
      // manifest and seed `text` with it instead of the applied
      // baseline — otherwise the retry would silently re-submit the
      // last successful state, not what failed.
      try {
        const opId = sessionStorage.getItem(RETRY_KEY);
        if (opId) {
          sessionStorage.removeItem(RETRY_KEY);
          const op = await opsApi.get(opId);
          if (op.manifestJson && op.kind === k && op.resourceName === n) {
            const parsed = JSON.parse(op.manifestJson);
            text = resourceToYAML(parsed);
            retryFromOpId = opId;
          }
        }
      } catch (e) {
        // Best-effort: a missing/expired op or sessionStorage failure
        // just falls through to the applied baseline.
        // eslint-disable-next-line no-console
        console.warn('retry-from-op handoff failed', e);
      }
      schedulePreview();
      void loadFormSchema(av, k);
    } catch (err) {
      if (err instanceof UnauthorizedError) return;
      loadError = err instanceof Error ? err.message : String(err);
    } finally {
      loading = false;
    }
  }

  async function loadFormSchema(av: string, k: string) {
    formSchema = null;
    formSchemaError = '';
    try {
      const resp = await schemas.getForm(av, k);
      if (!resp.json) return;
      formSchema = JSON.parse(resp.json) as FF;
      // Create mode: upgrade the stub seed to the schema's defaults if
      // the user hasn't touched it yet. We compare against the stub —
      // any divergence means typing has begun and we leave it alone.
      // The name suggestion carries over so the field isn't blank.
      if (isCreate && text === seedManifest(av, k)) {
        const specField = (formSchema.fields ?? []).find((f) => f.name === 'spec')
          ?? { type: 'object' as const };
        const seededSpec = scrubEmpty(specField, initialValue(specField));
        const merged = {
          apiVersion: av,
          kind: k,
          metadata: { name: suggestName(k) },
          spec: seededSpec as Record<string, unknown> | undefined,
        };
        text = resourceToYAML(merged as unknown as import('../lib/api').Resource);
        schedulePreview();
      }
      reseedFormState();
    } catch (err) {
      // 404 = no schema for this kind — silently disable the Form tab.
      if (err instanceof Error && err.message.includes('404')) return;
      if (err instanceof UnauthorizedError) return;
      formSchemaError = err instanceof Error ? err.message : String(err);
    }
  }

  // seedManifest returns a minimal placeholder manifest used as the
  // initial editor text in create mode before the form schema arrives.
  // Kept tiny on purpose — once loadFormSchema runs we replace it with
  // a richer schema-driven seed (defaults + required fields).
  //
  // metadata.name is pre-filled with a kind-derived suggestion (e.g.
  // "vm-a3b2") so the field isn't blank when the form opens — users
  // can accept the suggestion or type over it. The suggestion is
  // stable per-render so the schema-upgrade path can equality-check
  // against the same seed.
  function seedManifest(av: string, k: string): string {
    return `apiVersion: ${av}\nkind: ${k}\nmetadata:\n  name: ${suggestName(k)}\nspec: {}\n`;
  }

  // Stable per-Edit-instance name suggestion. Generated once on mount
  // so the seedManifest string doesn't churn on every render (which
  // would defeat the "did the user edit?" equality check in the
  // schema-upgrade path).
  const suggestedName = generateSuggestedName();
  function suggestName(k: string): string {
    const prefix = kindPrefix(k);
    return `${prefix}-${suggestedName}`;
  }

  function kindPrefix(k: string): string {
    switch (k) {
      case 'VirtualMachine': return 'vm';
      case 'Cluster': return 'cluster';
      default: return k.toLowerCase().slice(0, 12);
    }
  }

  function generateSuggestedName(): string {
    // Four random lowercase-alphanumeric characters — enough for
    // uniqueness at homelab scale (26^4 == 456k names per kind) and
    // short enough to be memorable / disposable if the user overrides.
    const alphabet = 'abcdefghijklmnopqrstuvwxyz0123456789';
    let out = '';
    for (let i = 0; i < 4; i++) {
      out += alphabet[Math.floor(Math.random() * alphabet.length)];
    }
    return out;
  }

  // loadExistingNames fetches the names of resources of this kind so
  // the create flow can warn on collision. Best-effort: an error here
  // (controller down, perms) just leaves the set empty — the apply RPC
  // will still surface real failures.
  async function loadExistingNames(av: string, k: string) {
    try {
      const resp = await resources.list(av, k);
      existingNames = new Set((resp.resources ?? []).map((r) => r.metadata.name));
    } catch (err) {
      if (err instanceof UnauthorizedError) return;
      // Don't block the create flow on a list failure — surface in
      // console for debugging but keep the form usable.
      // eslint-disable-next-line no-console
      console.warn('loadExistingNames failed', err);
    }
  }

  // Rebuild form state from the current text. Used on initial load and
  // whenever the user switches Form → Editor → Form so the form
  // reflects edits made in the editor view.
  function reseedFormState() {
    if (!formSchema) return;
    const parsed = parseYAML(text);
    formState = fromManifest(formSchema, parsed.doc);
  }

  function onFormChange(detail: unknown) {
    if (!formSchema) return;
    formState = detail;
    // Form drives the text: serialise the scrubbed state back to YAML
    // and feed it through the same change path the editor uses.
    formDriving = true;
    try {
      const scrubbed = scrubEmpty(formSchema, formState) as Record<string, unknown>;
      text = resourceToYAML(scrubbed as unknown as import('../lib/api').Resource);
      schedulePreview();
    } finally {
      formDriving = false;
    }
  }

  function onChange(e: CustomEvent<string>) {
    text = e.detail;
    if (!formDriving && formSchema && view === 'form') {
      // Shouldn't usually fire — editor is hidden when view is 'form'
      // — but if it does, keep form in sync.
      reseedFormState();
    }
    // Any edit invalidates a successful in-progress apply view — drop
    // back to preview mode. (We don't cancel an in-flight Apply RPC;
    // the user's edit can't change the server-side outcome at that
    // point.)
    if (liveOpId && !inflightOpRow(liveOpId)) {
      liveOpId = '';
    }
    schedulePreview();
  }

  function schedulePreview() {
    if (timer !== undefined) clearTimeout(timer);
    timer = setTimeout(runPreview, PREVIEW_DEBOUNCE_MS) as unknown as number;
  }

  async function runPreview() {
    if (text === lastPreviewedText) return;
    lastPreviewedText = text;
    parseError = '';
    validateErrors = [];
    plan = null;

    const parsed = parseYAML(text);
    if (parsed.error) {
      parseError = parsed.error;
      return;
    }
    if (!parsed.doc.apiVersion || !parsed.doc.kind) {
      parseError = 'apiVersion and kind are required';
      return;
    }

    const resource = {
      apiVersion: parsed.doc.apiVersion,
      kind: parsed.doc.kind,
      metadata: { name: parsed.doc.metadata?.name ?? '' },
      spec: parsed.doc.spec as Record<string, unknown> | undefined,
    };

    previewing = true;
    try {
      // Validate + DryRunApply in parallel — both round-trip the same
      // schema check internally so this isn't redundant work for the
      // server, just lets the UI render diagnostics independently of the
      // planning info.
      const [vResp, dResp] = await Promise.all([
        schemas.validate(resource),
        resources.dryRunApply(resource),
      ]);
      validateErrors = vResp.errors ?? [];
      plan = dResp;
      // Publish per-path field errors so FormField rows can highlight
      // themselves. DryRunApply always populates fieldErrors when
      // validation fails, so this is the single source of truth.
      const errs: Record<string, string> = {};
      for (const fe of dResp.fieldErrors ?? []) {
        if (fe.path && fe.message) errs[fe.path] = fe.message;
      }
      fieldErrorsCtx.set(errs);
      // Preserve existing checkbox state for gates that still apply;
      // drop gates that no longer apply.
      const next: Record<string, boolean> = {};
      for (const g of plan.requiredGates ?? []) {
        next[g] = checkedGates[g] ?? false;
      }
      checkedGates = next;
    } catch (err) {
      if (err instanceof UnauthorizedError) return;
      validateErrors = [err instanceof Error ? err.message : String(err)];
    } finally {
      previewing = false;
    }
  }

  async function doApply() {
    if (applyBlocked || !plan) return;
    const parsed = parseYAML(text);
    if (parsed.error || !parsed.doc.apiVersion || !parsed.doc.kind) return;

    applying = true;
    applyError = '';
    try {
      const submitName = parsed.doc.metadata?.name ?? '';
      const resp = await resources.apply({
        resource: {
          apiVersion: parsed.doc.apiVersion,
          kind: parsed.doc.kind,
          metadata: { name: submitName },
          spec: parsed.doc.spec as Record<string, unknown> | undefined,
        },
        allowDestructive: checkedGates['allow_destructive'] ?? false,
        iKnowThisBreaksTheCluster: checkedGates['i_know_this_breaks_the_cluster'] ?? false,
      });
      if (resp.operationId) {
        liveOpId = resp.operationId;
        // Treat the just-submitted text as the new baseline — Discard
        // should revert to what the user just sent, not the stale applied
        // manifest. Real applied-state catches up via the ops store +
        // Watch when the dispatcher writes back.
        baseline = text;
        // Create mode: remember the submitted name so the success
        // handoff can navigate to the detail page.
        if (isCreate) createdName = submitName;
      }
    } catch (err) {
      if (err instanceof UnauthorizedError) return;
      applyError = err instanceof Error ? err.message : String(err);
    } finally {
      applying = false;
    }
  }

  function inflightOpRow(opId: string): OperationRow | null {
    const found = $opsStore.find((o) => o.id === opId);
    if (!found) return null;
    if (found.status === 'succeeded' || found.status === 'failed' || found.status === 'interrupted') {
      return null;
    }
    return found;
  }

  onDestroy(() => {
    if (timer !== undefined) clearTimeout(timer);
  });

  function discard() {
    text = baseline;
    schedulePreview();
  }

  function back() {
    if (isCreate) {
      navigate({ name: 'list', apiVersion, kind });
      return;
    }
    navigate({ name: 'detail', apiVersion, kind, resourceName });
  }

  // Apply readiness: well-formed YAML + no validation errors + every
  // required gate checked + something to actually do. Also blocked
  // while a submission is in flight.
  $: gatesSatisfied = (plan?.requiredGates ?? []).every((g) => checkedGates[g]);
  $: hasChange = !!plan && (
    (plan.diff?.length ?? 0) > 0 ||
    (plan.children ?? []).some((c) => c.verb !== 'no-op')
  );
  $: ownedByAnother = ownerRefs.length > 0;
  // Composite-child kinds (K3sNode, AgentInstall) get an info banner in create
  // mode nudging toward the owning composite.
  $: advKind = isCreate ? advancedKind(apiVersion, kind) : undefined;

  // Name in the current manifest, used by the create-mode collision
  // check. Parsing on every keystroke is cheap (small docs); we don't
  // want to wait for the debounced preview because the warning should
  // appear immediately as the user types.
  $: currentName = (() => {
    if (!text) return '';
    const p = parseYAML(text);
    if (p.error) return '';
    return p.doc?.metadata?.name ?? '';
  })();
  $: nameCollision = isCreate && currentName !== '' && existingNames.has(currentName);

  $: applyBlocked =
    applying ||
    ownedByAnother ||
    parseError !== '' ||
    validateErrors.length > 0 ||
    !plan ||
    !hasChange ||
    !gatesSatisfied ||
    nameCollision;

  $: dirty = text !== baseline;
  // If the user reverts back to matching baseline while on the diff
  // view, snap back to editor — the diff would be empty and the tab
  // gets disabled.
  $: if (!dirty && view === 'diff') view = 'edit';
  // Create mode has no baseline to diff against — the tab would just
  // mirror the editor. Force it off if the user lands there somehow.
  $: if (isCreate && view === 'diff') view = 'edit';

  // Re-seed the form only when the user transitions into the Form tab
  // (edge trigger, not level trigger). A prior level-trigger version
  // re-parsed text on every keystroke and clobbered the form state:
  // click "+ cloudInit" → form gets cloudInit={} → scrubEmpty strips
  // it from text → reactive re-parses text → cloudInit gone from form
  // state → the "+ cloudInit" button reappears. Users saw no effect on
  // click and concluded the field wasn't editable. Every optional
  // composite (labels, annotations, networks, disks, cloudInit,
  // template/cloudImage/image) had the same shape.
  let prevView: 'form' | 'edit' | 'diff' = view;
  $: {
    if (view === 'form' && prevView !== 'form' && formSchema) {
      reseedFormState();
    }
    prevView = view;
  }

  // Detect when the editor text carries keys the form can't represent.
  // When non-empty, the Form tab is disabled with the offending paths in
  // the tooltip so the user knows what to remove (or stays in Editor).
  // Skipped while the form itself is driving — it always produces a
  // roundtrippable subset by construction.
  $: nonRoundtrippablePaths = (() => {
    if (!formSchema || formDriving) return [];
    const parsed = parseYAML(text);
    if (parsed.error) return [];
    return extraKeys(formSchema, parsed.doc);
  })();

  // If the user is on the Form tab when an external edit introduces a
  // non-roundtrippable key, bounce them to Editor so they can see the
  // raw YAML — otherwise the form would silently drop those keys on
  // the next save.
  $: if (view === 'form' && nonRoundtrippablePaths.length > 0) view = 'edit';

  // Live op row (when an Apply is in flight) drives the inline progress
  // banner. Driven off the shell-wide ops store so we don't open a
  // second WatchOperations.
  $: liveOp = liveOpId ? ($opsStore.find((o) => o.id === liveOpId) ?? null) : null;
  $: if (liveOp?.status === 'succeeded') {
    // Brief pause so the user sees the green flash, then jump to detail.
    setTimeout(() => {
      if (liveOpId && liveOp?.status === 'succeeded') {
        const target = isCreate ? createdName : resourceName;
        if (target) {
          navigate({ name: 'detail', apiVersion, kind, resourceName: target });
        }
      }
    }, 600);
  }

  $: markers = (() => {
    const out: Array<{
      severity: 'error' | 'warning' | 'info';
      message: string;
      startLineNumber: number;
      startColumn: number;
      endLineNumber: number;
      endColumn: number;
    }> = [];
    if (parseError) {
      out.push({
        severity: 'error', message: parseError,
        startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 1,
      });
    }
    for (const e of validateErrors) {
      out.push({
        severity: 'error', message: e,
        startLineNumber: 1, startColumn: 1, endLineNumber: 1, endColumn: 1,
      });
    }
    return out;
  })();

  function gateLabel(g: string): string {
    switch (g) {
      case 'allow_destructive':
        return 'Allow destructive changes';
      case 'i_know_this_breaks_the_cluster':
        return 'I know this breaks the cluster';
      default: return g;
    }
  }

  function verbClass(v: string): string {
    switch (v) {
      case 'create': return 'verb-create';
      case 'destroy': return 'verb-destroy';
      case 'respec': return 'verb-respec';
      default: return 'verb-noop';
    }
  }

  // U6: resolve a ChildAction's apiVersion via the live children list.
  // Returns null when the kind isn't in the live set (e.g. a brand-new
  // child being created — the link wouldn't 404-safely anyway).
  function childApiVersion(kind: string): string | null {
    const m = liveChildren.find((r) => r.kind === kind);
    return m?.apiVersion ?? null;
  }

  function applyBlockReason(): string {
    if (applying) return 'Apply already submitted';
    if (ownedByAnother) {
      const owner = ownerRefs[0];
      return `This resource is owned by ${owner.kind}/${owner.name} — edit the owner instead.`;
    }
    if (parseError) return 'Fix YAML parse error before applying';
    if (validateErrors.length > 0) return 'Fix validation errors before applying';
    if (nameCollision) {
      return `A ${kind} named "${currentName}" already exists — pick a different name or Edit the existing one.`;
    }
    if (!hasChange) {
      return isCreate ? 'Add a metadata.name and spec fields' : 'No changes to apply';
    }
    if (!gatesSatisfied) return 'Check the destructive-change confirmations above';
    return '';
  }
</script>

<section>
  <header>
    <div>
      <p class="crumbs">
        <a href={routeHref({ name: 'list', apiVersion, kind })}>{kind}</a>
        {#if !isCreate}
          <span> · </span>
          <a href={routeHref({ name: 'detail', apiVersion, kind, resourceName })}>{resourceName}</a>
        {/if}
      </p>
      <h2>{isCreate ? `New ${kind}` : `Edit ${resourceName}`}</h2>
    </div>
    <div class="actions">
      {#if !isCreate}
        <span class="state" class:dirty>{dirty ? 'unsaved changes' : 'no changes'}</span>
      {/if}
      {#if previewing}
        <span class="state state-busy">previewing…</span>
      {/if}
      {#if !isCreate}
        <button on:click={discard} disabled={!dirty || applying}>Discard</button>
      {/if}
      <button on:click={back} disabled={applying}>{isCreate ? 'Cancel' : 'Back'}</button>
      <button
        class="primary"
        disabled={applyBlocked || !$canMutate}
        on:click={doApply}
        title={!$canMutate ? readOnlyTitle : (applyBlocked && plan ? applyBlockReason() : '')}
      >
        {applying ? 'Submitting…' : isCreate ? 'Create' : 'Apply'}
      </button>
    </div>
  </header>

  {#if loading}
    <p class="muted">{isCreate ? 'Preparing…' : 'Loading manifest…'}</p>
  {:else if loadError}
    <p class="err">{loadError}</p>
  {:else}
    {#if retryFromOpId}
      <article class="retry-block">
        Pre-filled from operation
        <code class="mono">{retryFromOpId.slice(0, 12)}</code> — review the manifest before re-applying.
      </article>
    {/if}
    {#if advKind}
      <article class="advanced-block">
        <strong>Advanced — usually created via a {advKind.owner}.</strong>
        {advKind.note}
        <a class="primary-link" href={routeHref({ name: 'create', apiVersion: 'k3s.openctl.io/v1', kind: advKind.owner })}>
          Create a {advKind.owner} instead →
        </a>
      </article>
    {/if}
    {#if nameCollision}
      <article class="collision-block">
        <strong>Name already in use.</strong>
        A {kind} named <code class="mono">{currentName}</code> already exists. Pick a different
        name to create a new one, or
        <a href={routeHref({ name: 'edit', apiVersion, kind, resourceName: currentName })}>edit the existing {kind} →</a>
      </article>
    {/if}
    {#if ownedByAnother}
      {@const owner = ownerRefs[0]}
      <article class="owner-block">
        <strong>Owned resource — read-only.</strong>
        This {kind} is composed by
        <a href={routeHref({ name: 'detail', apiVersion: owner.apiVersion, kind: owner.kind, resourceName: owner.name })}>
          {owner.kind}/{owner.name}
        </a>.
        Editing this manifest directly won't take effect — the owner re-creates
        it from its own spec on every apply.
        <a class="primary-link" href={routeHref({ name: 'edit', apiVersion: owner.apiVersion, kind: owner.kind, resourceName: owner.name })}>
          Edit {owner.kind}/{owner.name} instead →
        </a>
      </article>
    {/if}
    <div class="view-toggle" role="tablist" aria-label="View">
      <button
        role="tab"
        aria-selected={view === 'form'}
        class:active={view === 'form'}
        on:click={() => (view = 'form')}
        disabled={!formSchema || nonRoundtrippablePaths.length > 0}
        title={
          !formSchema
            ? (formSchemaError || 'No form schema for this kind')
            : nonRoundtrippablePaths.length > 0
              ? `Manifest has fields the form can't represent — switching to Form would drop them. Offending keys: ${nonRoundtrippablePaths.slice(0, 5).join(', ')}${nonRoundtrippablePaths.length > 5 ? ', …' : ''}`
              : ''
        }
      >Form</button>
      <button
        role="tab"
        aria-selected={view === 'edit'}
        class:active={view === 'edit'}
        on:click={() => (view = 'edit')}
      >Editor</button>
      {#if !isCreate}
        <button
          role="tab"
          aria-selected={view === 'diff'}
          class:active={view === 'diff'}
          on:click={() => (view = 'diff')}
          disabled={!dirty}
          title={!dirty ? 'No changes to diff against the applied manifest' : ''}
        >Diff vs applied</button>
      {/if}
    </div>

    <div class="editor-wrap" class:form-view={view === 'form'} class:form-view-solo={view === 'form' && !showPreview}>
      {#if view === 'form' && formSchema}
        <div class="form-pane">
          <FormField field={formSchema} value={formState} on:change={(e) => onFormChange(e.detail)} />
        </div>
        {#if showPreview}
          <div class="form-preview">
            <div class="form-preview-head">
              <h4>Manifest</h4>
              <button type="button" class="preview-toggle" on:click={togglePreview} title="Hide manifest preview">
                Hide
              </button>
            </div>
            <pre>{text}</pre>
          </div>
        {:else}
          <button type="button" class="preview-show" on:click={togglePreview} title="Show manifest preview">
            Show manifest
          </button>
        {/if}
      {:else if view === 'edit'}
        <MonacoEditor value={text} on:change={onChange} {markers} disabled={applying} />
      {:else}
        <MonacoDiffEditor original={baseline} modified={text} />
      {/if}
    </div>

    {#if applyError}
      <article class="diag">
        <h3>Apply failed</h3>
        <p class="err mono">{applyError}</p>
      </article>
    {/if}

    {#if liveOp}
      <article class="op-card op-{liveOp.status}">
        <h3>Operation {liveOp.id.slice(0, 12)} — {liveOp.status}</h3>
        {#if liveOp.error}
          <p class="err mono">{liveOp.error}</p>
        {:else if liveOp.status === 'pending'}
          <p class="muted">Queued, waiting for the dispatcher to pick it up…</p>
        {:else if liveOp.status === 'running'}
          <p class="muted">Provider is converging the resource. Tail in the ops drawer for substeps.</p>
        {:else if liveOp.status === 'succeeded'}
          <p>Applied. Returning to detail…</p>
        {:else}
          <p class="muted">Status: {liveOp.status}</p>
        {/if}
      </article>
    {/if}

    {#if parseError || validateErrors.length > 0}
      <article class="diag">
        <h3>Validation</h3>
        {#if parseError}
          <p class="err mono">YAML: {parseError}</p>
        {/if}
        {#each validateErrors as e}
          <p class="err mono">{e}</p>
        {/each}
      </article>
    {/if}

    {#if plan && validateErrors.length === 0 && !parseError}
      <article class="preview">
        <header class="preview-head">
          <h3>Preview</h3>
          <span class="muted small">{plan.summary || (hasChange ? 'changes pending' : 'no-op')}</span>
        </header>

        {#if !hasChange}
          <p class="muted">No changes. The manifest matches what's already applied.</p>
        {/if}

        {#if (plan.diff?.length ?? 0) > 0}
          <table class="diff">
            <thead>
              <tr><th>Path</th><th>Current</th><th>Will become</th></tr>
            </thead>
            <tbody>
              {#each (plan.diff ?? []) as d}
                {@const _d = /** @type {DriftEntry} */ (d)}
                <tr>
                  <td class="mono">{_d.path}</td>
                  <td class="mono">{_d.desired}</td>
                  <td class="mono">{_d.observed}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}

        {#if (plan.children?.length ?? 0) > 0}
          <ul class="children">
            {#each plan.children ?? [] as c}
              {@const ref = childApiVersion(c.kind)}
              <li class={verbClass(c.verb)}>
                <span class="verb">{c.verb}</span>
                {#if ref && c.verb !== 'create'}
                  <a class="mono child-link" href={routeHref({ name: 'detail', apiVersion: ref, kind: c.kind, resourceName: c.name })}>{c.kind}/{c.name}</a>
                {:else}
                  <span class="mono">{c.kind}/{c.name}</span>
                {/if}
                {#if c.detail}<span class="muted small">— {c.detail}</span>{/if}
              </li>
            {/each}
          </ul>
        {/if}

        {#if (plan.requiredGates?.length ?? 0) > 0}
          <div class="gates">
            <h4>Destructive change — confirm before applying:</h4>
            {#each plan.requiredGates ?? [] as g}
              <label class="gate">
                <input type="checkbox" bind:checked={checkedGates[g]} />
                <span>{gateLabel(g)}</span>
                <code class="muted small">{g}</code>
              </label>
            {/each}
          </div>
        {/if}
      </article>
    {/if}
  {/if}
</section>

<style>
  section {
    display: flex;
    flex-direction: column;
    gap: 1rem;
    max-width: 80rem;
  }
  header {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 1rem;
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
  h2 {
    margin: 0;
    font-size: 1.25rem;
  }
  .actions {
    display: flex;
    align-items: center;
    gap: 0.6rem;
  }
  .state {
    font-size: 0.8rem;
    color: #888;
    padding: 0.1em 0.6em;
    border-radius: 999px;
    background: rgba(127, 127, 127, 0.12);
  }
  .state.dirty {
    color: #ffce4d;
    background: rgba(255, 184, 0, 0.15);
  }
  .state-busy {
    color: #6ea8ff;
    background: rgba(74, 142, 240, 0.12);
  }
  .primary {
    background: #4a8ef0;
    color: white;
    border-color: #4a8ef0;
  }
  .primary:disabled {
    opacity: 0.5;
  }
  .view-toggle {
    display: inline-flex;
    background: rgba(127, 127, 127, 0.1);
    border-radius: 6px;
    padding: 2px;
    align-self: flex-start;
  }
  .view-toggle button {
    background: transparent;
    border: none;
    padding: 0.25em 0.9em;
    font-size: 0.8rem;
    color: #aaa;
    border-radius: 4px;
    cursor: pointer;
  }
  .view-toggle button:hover:not(:disabled) {
    color: #fff;
  }
  .view-toggle button.active {
    background: #4a8ef0;
    color: white;
  }
  .view-toggle button:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
  .editor-wrap {
    min-height: 24rem;
    display: flex;
  }
  .editor-wrap :global(.wrapper) {
    flex: 1;
  }
  .editor-wrap.form-view {
    display: grid;
    grid-template-columns: minmax(0, 2fr) minmax(0, 1fr);
    gap: 1rem;
    align-items: stretch;
  }
  .editor-wrap.form-view-solo {
    grid-template-columns: minmax(0, 1fr);
  }
  .form-preview-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.5rem 0.9rem;
    background: rgba(127, 127, 127, 0.08);
    border-bottom: 1px solid rgba(127, 127, 127, 0.15);
  }
  .form-preview-head h4 {
    margin: 0;
    padding: 0;
    background: none;
    border: none;
    font-size: 0.75rem;
    color: #888;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .preview-toggle,
  .preview-show {
    background: transparent;
    border: 1px solid rgba(127, 127, 127, 0.3);
    color: #aaa;
    font-size: 0.75rem;
    padding: 0.15em 0.6em;
    border-radius: 4px;
    cursor: pointer;
  }
  .preview-toggle:hover,
  .preview-show:hover {
    color: #fff;
    background: rgba(127, 127, 127, 0.12);
  }
  .preview-show {
    align-self: flex-start;
  }
  .form-pane {
    overflow-y: auto;
    padding: 1rem 1.25rem;
    background: rgba(127, 127, 127, 0.04);
    border: 1px solid rgba(127, 127, 127, 0.18);
    border-radius: 6px;
    max-height: 70vh;
  }
  .form-preview {
    display: flex;
    flex-direction: column;
    border: 1px solid rgba(127, 127, 127, 0.18);
    border-radius: 6px;
    overflow: hidden;
  }
  /* Old .form-preview h4 background/padding style superseded by
     .form-preview-head above; the h4 inside that container is
     styled there. */
  .form-preview pre {
    margin: 0;
    padding: 0.75rem 1rem;
    flex: 1;
    overflow: auto;
    font-family: ui-monospace, SFMono-Regular, monospace;
    font-size: 0.8rem;
    line-height: 1.5;
    background: rgba(0, 0, 0, 0.18);
    color: inherit;
  }
  @media (prefers-color-scheme: light) {
    .form-preview pre {
      background: #f8f8f8;
    }
  }
  .diag {
    background: rgba(248, 81, 73, 0.06);
    border-left: 3px solid #f57171;
    padding: 0.75rem 1rem;
    border-radius: 4px;
  }
  .diag h3 {
    margin: 0 0 0.5rem;
    font-size: 0.85rem;
    color: #aaa;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .err {
    color: #f57171;
    margin: 0.25rem 0;
  }
  .mono {
    font-family: ui-monospace, SFMono-Regular, monospace;
    font-size: 0.85rem;
  }
  .muted {
    color: #888;
  }
  .small {
    font-size: 0.85rem;
  }
  .preview {
    background: rgba(127, 127, 127, 0.06);
    border-radius: 6px;
    padding: 1rem 1.25rem;
  }
  .owner-block {
    background: rgba(110, 168, 255, 0.08);
    border-left: 3px solid #6ea8ff;
    border-radius: 6px;
    padding: 0.85rem 1.1rem;
    font-size: 0.9rem;
    line-height: 1.55;
  }
  .owner-block a {
    color: #6ea8ff;
    text-decoration: none;
  }
  .owner-block a:hover {
    text-decoration: underline;
  }
  .owner-block .primary-link {
    display: inline-block;
    margin-left: 0.5rem;
    font-weight: 500;
  }
  .child-link {
    color: #6ea8ff;
    text-decoration: none;
  }
  .child-link:hover {
    text-decoration: underline;
  }
  .advanced-block {
    background: rgba(200, 162, 74, 0.09);
    border-left: 3px solid #c8a24a;
    border-radius: 6px;
    padding: 0.75rem 1.1rem;
    font-size: 0.88rem;
    line-height: 1.55;
  }
  .advanced-block a {
    color: #d6b45e;
    text-decoration: none;
  }
  .advanced-block a:hover {
    text-decoration: underline;
  }
  .advanced-block .primary-link {
    display: inline-block;
    margin-left: 0.4rem;
    font-weight: 500;
  }
  .retry-block {
    background: rgba(255, 184, 0, 0.08);
    border-left: 3px solid #ffce4d;
    border-radius: 6px;
    padding: 0.65rem 1rem;
    font-size: 0.88rem;
  }
  .retry-block code {
    background: rgba(0, 0, 0, 0.2);
    padding: 0.05em 0.4em;
    border-radius: 3px;
    margin: 0 0.3em;
  }
  .collision-block {
    background: rgba(248, 81, 73, 0.08);
    border-left: 3px solid #ff8980;
    border-radius: 6px;
    padding: 0.65rem 1rem;
    font-size: 0.88rem;
  }
  .collision-block code {
    background: rgba(0, 0, 0, 0.2);
    padding: 0.05em 0.4em;
    border-radius: 3px;
    margin: 0 0.3em;
  }
  .collision-block a {
    color: #6ea8ff;
    text-decoration: none;
    margin-left: 0.4em;
  }
  .collision-block a:hover {
    text-decoration: underline;
  }
  .preview-head {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    margin-bottom: 0.75rem;
  }
  .preview h3 {
    margin: 0;
    font-size: 0.9rem;
    color: #aaa;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  table.diff {
    width: 100%;
    border-collapse: collapse;
    margin-bottom: 0.75rem;
  }
  .diff th, .diff td {
    text-align: left;
    padding: 0.35rem 0.6rem;
    border-bottom: 1px solid rgba(127, 127, 127, 0.15);
  }
  .diff th {
    font-size: 0.7rem;
    color: #888;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  ul.children {
    list-style: none;
    margin: 0 0 0.75rem;
    padding: 0;
  }
  ul.children li {
    padding: 0.3rem 0;
    display: flex;
    gap: 0.5rem;
    align-items: baseline;
  }
  .verb {
    display: inline-block;
    min-width: 5rem;
    padding: 0.05em 0.5em;
    border-radius: 4px;
    font-size: 0.8rem;
    font-weight: 600;
    text-align: center;
    text-transform: lowercase;
  }
  .verb-create  .verb { background: rgba(46, 160, 67, 0.18); color: #5fdb78; }
  .verb-destroy .verb { background: rgba(248, 81, 73, 0.18); color: #ff8980; }
  .verb-respec  .verb { background: rgba(255, 184, 0, 0.18); color: #ffce4d; }
  .verb-noop    .verb { background: rgba(127, 127, 127, 0.18); color: #aaa; }
  .gates {
    margin-top: 0.75rem;
    padding: 0.75rem 1rem;
    background: rgba(255, 184, 0, 0.08);
    border-left: 3px solid #ffce4d;
    border-radius: 4px;
  }
  .gates h4 {
    margin: 0 0 0.5rem;
    font-size: 0.85rem;
    color: #ffce4d;
  }
  .gate {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.25rem 0;
    cursor: pointer;
  }
  .gate input {
    margin: 0;
  }
  .op-card {
    border-left: 3px solid #6ea8ff;
    background: rgba(74, 142, 240, 0.06);
    padding: 0.75rem 1rem;
    border-radius: 4px;
  }
  .op-card h3 {
    margin: 0 0 0.5rem;
    font-size: 0.9rem;
    color: #aaa;
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .op-succeeded { border-color: #5fdb78; background: rgba(46, 160, 67, 0.08); }
  .op-failed, .op-interrupted { border-color: #f57171; background: rgba(248, 81, 73, 0.06); }
  .op-card p {
    margin: 0;
  }

  /* Mobile: stack the form editor and its live preview instead of the
     side-by-side 2fr/1fr split, which is unusable at phone width. Keep in
     sync with --bp-mobile (48rem) in app.css. */
  @media (max-width: 48rem) {
    .editor-wrap.form-view {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
