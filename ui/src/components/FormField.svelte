<script lang="ts">
  import { createEventDispatcher, getContext } from 'svelte';
  import type { FormField } from '../lib/formSchema';
  import { initialValue } from '../lib/formSchema';
  import { ensureOptions, ensureDependentOptions, dependentKey, optionsStore } from '../lib/options';
  import Self from './FormField.svelte';

  // Set by Edit.svelte so @options(kind="X") without an explicit
  // apiVersion resolves against the containing resource's provider.
  // Empty when the context isn't set — cross-provider refs must then
  // include apiVersion in the CUE attribute.
  import type { Readable } from 'svelte/store';
  const apiVersionCtx = getContext<Readable<string> | undefined>('resourceAPIVersionStore');
  // Per-path field errors published by Edit.runPreview. Parent object
  // rows look this up for each child to decide whether to add the
  // error rail; individual fields don't consume it themselves.
  const fieldErrorsCtx = getContext<Readable<Record<string, string>> | undefined>('resourceFieldErrorsStore');
  // The whole manifest, published by Edit, so a `dependsOn` option field can
  // read the value it depends on (e.g. spec.node for a disk storage dropdown).
  const manifestCtx = getContext<Readable<unknown> | undefined>('resourceManifestStore');

  // The field schema and its current value. Value is `unknown` here
  // because every Field can carry a different shape; downstream the
  // dispatch on `field.type` narrows it.
  export let field: FormField;
  export let value: unknown;
  // Depth controls indent for nested objects. The root container in
  // the form renderer passes depth=0.
  export let depth: number = 0;
  // Dotted path from the root of the resource, extended by parents as
  // they recurse. Empty at the root. Used to look up field-specific
  // validation errors from context.
  export let path: string = '';

  const dispatch = createEventDispatcher<{ change: unknown }>();

  function set(v: unknown) {
    value = v;
    dispatch('change', v);
  }

  // --- Secret-reference control (@secret fields) ---------------------------
  // Parse the current value into a {source, key} pair for the control, and
  // note a leftover inline plaintext value so the UI can nudge the user to
  // replace it with a reference.
  function secretParts(v: unknown): { source: string; key: string; plaintext: string | null } {
    if (v && typeof v === 'object' && !Array.isArray(v) && '$secret' in (v as Record<string, unknown>)) {
      const s = ((v as Record<string, unknown>).$secret ?? {}) as Record<string, unknown>;
      if (typeof s.file === 'string') return { source: 'file', key: s.file, plaintext: null };
      if (typeof s.env === 'string') return { source: 'env', key: s.env, plaintext: null };
      if (typeof s.provider === 'string') return { source: s.provider, key: (s.key as string) ?? '', plaintext: null };
    }
    if (typeof v === 'string' && v !== '') return { source: 'file', key: '', plaintext: v };
    return { source: 'file', key: '', plaintext: null };
  }
  // Emit a {$secret: {...}} marker (or clear the field when the key is empty).
  function emitSecret(source: string, key: string) {
    if (!key) { set(undefined); return; }
    const inner = source === 'file' || source === 'env'
      ? { [source]: key }
      : { provider: source, key };
    set({ $secret: inner });
  }

  function setObjectKey(key: string, v: unknown) {
    const obj = (typeof value === 'object' && value && !Array.isArray(value))
      ? { ...(value as Record<string, unknown>) }
      : {};
    obj[key] = v;
    set(obj);
  }

  // unsetObjectKey deletes the key from the parent so scrubEmpty drops
  // it from the rendered YAML. Used by the collapse "×" affordance on
  // optional composite fields.
  function unsetObjectKey(key: string) {
    if (typeof value !== 'object' || value === null || Array.isArray(value)) return;
    const obj = { ...(value as Record<string, unknown>) };
    delete obj[key];
    set(obj);
  }

  // isCollapsible answers: should this child render as a "+ add"
  // button instead of the full sub-form? Yes when the field is optional,
  // composite, AND the user hasn't opened it yet.
  //
  // "Unset" here is strictly undefined/null — NOT empty. An empty object
  // ({}) or array ([]) means the user clicked "+ <name>" and is actively
  // working on it; collapsing it back would trap them in a loop where
  // the add button just re-fires setObjectKey({}) with no visible effect.
  // Composite fields with no required children (like cloudInit) seed to
  // {} on open, which is exactly the case that would loop.
  function isCollapsible(child: FormField, childValue: unknown): boolean {
    if (!child.optional) return false;
    if (child.type !== 'object' && child.type !== 'array' && child.type !== 'map') return false;
    return childValue === undefined || childValue === null;
  }

  function setArrayIndex(idx: number, v: unknown) {
    const arr = Array.isArray(value) ? value.slice() : [];
    arr[idx] = v;
    set(arr);
  }

  function addArrayItem() {
    if (!field.items) return;
    const arr = Array.isArray(value) ? value.slice() : [];
    arr.push(initialValue(field.items));
    set(arr);
  }

  function removeArrayItem(idx: number) {
    if (!Array.isArray(value)) return;
    const arr = value.slice();
    arr.splice(idx, 1);
    set(arr);
  }

  // The numeric input emits string values; coerce back to int/number
  // and respect "unset" (empty string → null) so the manifest stays
  // clean.
  function coerceNumber(raw: string): number | null {
    if (raw === '') return null;
    const n = Number(raw);
    return Number.isFinite(n) ? n : null;
  }

  // Map row editing: maintain a stable display order via Object.entries
  // so insertion ordering doesn't jitter while the user types. A draft
  // "new key" row is appended when the user clicks "+ Add row".
  function setMapKey(oldKey: string, newKey: string) {
    const m = (typeof value === 'object' && value && !Array.isArray(value))
      ? { ...(value as Record<string, unknown>) }
      : {};
    if (oldKey === newKey) return;
    if (newKey in m && oldKey !== newKey) return; // collision: ignore
    const v = m[oldKey];
    delete m[oldKey];
    m[newKey] = v;
    set(m);
  }
  function setMapValue(key: string, v: unknown) {
    const m = (typeof value === 'object' && value && !Array.isArray(value))
      ? { ...(value as Record<string, unknown>) }
      : {};
    m[key] = v;
    set(m);
  }
  function removeMapKey(key: string) {
    const m = (typeof value === 'object' && value && !Array.isArray(value))
      ? { ...(value as Record<string, unknown>) }
      : {};
    delete m[key];
    set(m);
  }
  // Resolve @options into a concrete (apiVersion, kind) pair. Returns
  // null when the field isn't annotated or when kind-only is used
  // outside a resource context (rare — Edit sets the context on mount).
  // readManifestPath walks a dotted path (from the manifest root) and returns
  // the string value there, or '' when absent / non-string.
  function readManifestPath(obj: unknown, path: string): string {
    let cur: unknown = obj;
    for (const seg of path.split('.')) {
      if (cur == null || typeof cur !== 'object') return '';
      cur = (cur as Record<string, unknown>)[seg];
    }
    return typeof cur === 'string' ? cur : '';
  }

  $: containingAV = apiVersionCtx ? $apiVersionCtx : '';
  $: optionsRef = (() => {
    const src = field.optionsSource;
    if (!src) return null;
    const av = src.apiVersion || containingAV;
    if (!av) return null;
    return { apiVersion: av, kind: src.kind };
  })();

  // Dependent options: when the source has `field` + `dependsOn`, the option
  // list comes from a specific resource instance (named by the dependsOn
  // value) rather than the list of names. Read the depended-on value from the
  // published manifest.
  $: depField = field.optionsSource?.field ?? '';
  $: depOn = field.optionsSource?.dependsOn ?? '';
  $: depValue = (depField && depOn && manifestCtx)
    ? readManifestPath($manifestCtx, depOn)
    : '';

  // Kick off the fetch as soon as we know the ref. Idempotent — the store
  // dedupes concurrent callers. Dependent fields fetch a specific resource;
  // plain option fields list names of the kind.
  $: if (optionsRef && depField && depOn) {
    void ensureDependentOptions(optionsRef.apiVersion, optionsRef.kind, depValue, depField);
  } else if (optionsRef) {
    void ensureOptions(optionsRef.apiVersion, optionsRef.kind);
  }

  // Reactive lookup of the current list. Reads from the store so that when the
  // fetch resolves (or the dependency changes), this field re-renders.
  $: optionsKey = !optionsRef
    ? ''
    : (depField && depOn)
      ? dependentKey(optionsRef.apiVersion, optionsRef.kind, depValue, depField)
      : `${optionsRef.apiVersion}/${optionsRef.kind}`;
  $: optionsNames = optionsKey ? $optionsStore.data[optionsKey] ?? null : null;
  $: optionsError = optionsKey ? $optionsStore.errors[optionsKey] ?? '' : '';
  // A dependent field whose dependency isn't chosen yet: no value to select on.
  $: depWaiting = !!(depField && depOn) && !depValue;

  // oneOf group support: preprocess field.fields into a render list
  // that collapses each group of `@oneOf(group="X")`-tagged siblings
  // into a single "picker" entry rendered at the position of the
  // first-appearing member. Non-grouped fields pass through as-is.
  type RenderItem =
    | { kind: 'field'; child: FormField; childValue: unknown }
    | { kind: 'group'; group: string; alternatives: FormField[]; selected: string };

  $: renderList = (() => {
    if (field.type !== 'object' || !field.fields) return [] as RenderItem[];
    const obj = (typeof value === 'object' && value && !Array.isArray(value))
      ? (value as Record<string, unknown>) : {};
    const seen = new Set<string>();
    const out: RenderItem[] = [];
    for (const child of field.fields) {
      if (child.oneOfGroup) {
        if (seen.has(child.oneOfGroup)) continue;
        seen.add(child.oneOfGroup);
        const alts = field.fields.filter((c) => c.oneOfGroup === child.oneOfGroup);
        // Selected = first alternative that has a value in the current
        // object. Empty when nothing is chosen yet.
        const selected = alts.find(
          (a) => a.name && obj[a.name] !== undefined,
        )?.name ?? '';
        out.push({ kind: 'group', group: child.oneOfGroup, alternatives: alts, selected });
      } else {
        const childValue = child.name ? obj[child.name] : undefined;
        out.push({ kind: 'field', child, childValue });
      }
    }
    return out;
  })();

  // selectOneOf switches the picked alternative: unsets any previously
  // selected sibling (deletes the key so scrubEmpty drops it), then
  // seeds the newly-chosen field to its initialValue. Setting to ""
  // clears the group entirely.
  function selectOneOf(alternatives: FormField[], newName: string) {
    const obj = (typeof value === 'object' && value && !Array.isArray(value))
      ? { ...(value as Record<string, unknown>) }
      : {};
    for (const alt of alternatives) {
      if (alt.name && alt.name !== newName) delete obj[alt.name];
    }
    if (newName) {
      const picked = alternatives.find((a) => a.name === newName);
      if (picked) obj[newName] = initialValue(picked);
    }
    set(obj);
  }

  function addMapRow() {
    const m = (typeof value === 'object' && value && !Array.isArray(value))
      ? { ...(value as Record<string, unknown>) }
      : {};
    // Pick a fresh placeholder key. Users edit it immediately; collisions
    // resolved by setMapKey.
    let i = 0;
    let base = 'key';
    while ((`${base}${i ? i : ''}`) in m) i++;
    const key = `${base}${i ? i : ''}`;
    m[key] = field.valueType ? initialValue(field.valueType) : '';
    set(m);
  }
</script>

{#if field.type === 'object'}
  <div class="object" style="--depth: {depth}">
    {#if field.description}
      <p class="desc">{field.description}</p>
    {/if}
    {#each renderList as item (item.kind === 'group' ? `__group__${item.group}` : item.child.name)}
      {#if item.kind === 'group'}
        <div class="oneof-group">
          <div class="oneof-picker" role="radiogroup" aria-label={item.group}>
            {#each item.alternatives as alt}
              <label class="oneof-choice" class:active={item.selected === alt.name}>
                <input
                  type="radio"
                  name={`__oneof__${item.group}__${depth}`}
                  value={alt.name}
                  checked={item.selected === alt.name}
                  on:change={() => selectOneOf(item.alternatives, alt.name ?? '')}
                />
                <span>{alt.name}</span>
              </label>
            {/each}
            {#if item.selected}
              <button
                type="button"
                class="oneof-clear"
                title="Clear selection"
                on:click={() => selectOneOf(item.alternatives, '')}
              >clear</button>
            {/if}
          </div>
          {#if item.selected}
            {@const picked = item.alternatives.find((a) => a.name === item.selected)}
            {@const obj = (typeof value === 'object' && value && !Array.isArray(value)) ? value : {}}
            {@const pickedValue = (obj as Record<string, unknown>)[item.selected]}
            {#if picked}
              {#if picked.description}
                <p class="desc small">{picked.description}</p>
              {/if}
              <Self
                field={picked}
                value={pickedValue}
                depth={depth + 1}
                path={path ? `${path}.${item.selected}` : item.selected}
                on:change={(e) => setObjectKey(item.selected, e.detail)}
              />
            {/if}
          {/if}
        </div>
      {:else}
        {@const child = item.child}
        {@const childValue = item.childValue}
        {@const collapsed = isCollapsible(child, childValue)}
        {@const removable = child.optional && !collapsed && (child.type === 'object' || child.type === 'array' || child.type === 'map')}
        {@const childPath = child.name ? (path ? `${path}.${child.name}` : child.name) : path}
        {@const childErr = (fieldErrorsCtx && childPath ? ($fieldErrorsCtx as Record<string, string>)[childPath] : '') ?? ''}
        <div class="row" class:row-error={childErr !== ''}>
          <div class="row-head">
            <span class="row-label">
              {child.name}{#if !child.optional}<span class="req" aria-label="required">*</span>{/if}
            </span>
            {#if removable}
              <button
                type="button"
                class="row-clear"
                title="Remove {child.name}"
                on:click={() => unsetObjectKey(child.name ?? '')}
              >×</button>
            {/if}
          </div>
          {#if child.description && (collapsed || child.type !== 'object')}
            <p class="desc small">{child.description}</p>
          {/if}
          {#if collapsed}
            <button
              type="button"
              class="add-opt"
              title={child.description ?? ''}
              on:click={() => setObjectKey(child.name ?? '', initialValue(child))}
            >+ {child.name}</button>
          {:else}
            <Self
              field={child}
              value={childValue}
              depth={depth + 1}
              path={childPath}
              on:change={(e) => setObjectKey(child.name ?? '', e.detail)}
            />
          {/if}
          {#if childErr}
            <p class="field-err">{childErr}</p>
          {/if}
        </div>
      {/if}
    {/each}
  </div>
{:else if field.type === 'array'}
  <div class="array">
    {#if Array.isArray(value)}
      {#each value as _item, i (i)}
        <div class="array-row">
          <div class="array-row-body">
            <Self
              field={field.items ?? { type: 'any' }}
              value={value[i]}
              depth={depth + 1}
              path={path ? `${path}.${i}` : `${i}`}
              on:change={(e) => setArrayIndex(i, e.detail)}
            />
          </div>
          <button type="button" class="remove" on:click={() => removeArrayItem(i)}
            title="Remove item">×</button>
        </div>
      {/each}
    {/if}
    <button type="button" class="add" on:click={addArrayItem}>+ Add item</button>
  </div>
{:else if field.type === 'map'}
  {@const valueIsComposite = field.valueType?.type === 'object' || field.valueType?.type === 'array' || field.valueType?.type === 'map'}
  <div class="map">
    {#if value && typeof value === 'object' && !Array.isArray(value)}
      {#each Object.entries(value as Record<string, unknown>) as [k, v] (k)}
        <div class="map-row" class:map-row-stacked={valueIsComposite}>
          <div class="map-key-line">
            <input
              type="text"
              class="map-key"
              value={k}
              placeholder="key"
              on:change={(e) => setMapKey(k, (e.currentTarget as HTMLInputElement).value)}
            />
            {#if !valueIsComposite}
              <span class="map-sep">:</span>
            {/if}
            {#if !valueIsComposite}
              <div class="map-value">
                <Self
                  field={field.valueType ?? { type: 'string' }}
                  value={v}
                  depth={depth + 1}
                  path={path ? `${path}.${k}` : k}
                  on:change={(e) => setMapValue(k, e.detail)}
                />
              </div>
            {/if}
            <button type="button" class="remove" on:click={() => removeMapKey(k)}
              title="Remove entry">×</button>
          </div>
          {#if valueIsComposite}
            <div class="map-nested">
              <Self
                field={field.valueType ?? { type: 'string' }}
                value={v}
                depth={depth + 1}
                path={path ? `${path}.${k}` : k}
                on:change={(e) => setMapValue(k, e.detail)}
              />
            </div>
          {/if}
        </div>
      {/each}
    {/if}
    <button type="button" class="add" on:click={addMapRow}>+ Add row</button>
  </div>
{:else if field.const !== undefined}
  <input class="const" type="text" value={String(field.const)} readonly tabindex="-1" />
{:else if field.secret}
  {@const sp = secretParts(value)}
  <div class="secret-field">
    <div class="secret-row">
      <select
        value={sp.source}
        on:change={(e) => emitSecret((e.currentTarget as HTMLSelectElement).value, sp.key)}
      >
        <option value="file">secret file</option>
        <option value="env">env var</option>
        {#if sp.source !== 'file' && sp.source !== 'env'}
          <option value={sp.source}>{sp.source}</option>
        {/if}
      </select>
      <input
        type="text"
        value={sp.key}
        placeholder={sp.source === 'env' ? 'ENV_VAR_NAME' : 'file-name.pw'}
        on:input={(e) => emitSecret(sp.source, (e.currentTarget as HTMLInputElement).value)}
      />
    </div>
    <p class="secret-hint">🔒 Stored as a reference — the value never enters this manifest or git.</p>
    {#if sp.plaintext !== null}
      <p class="secret-warn">
        This field holds an inline value that would be committed to the manifest.
        Set a reference above to replace it.
      </p>
    {/if}
  </div>
{:else if field.type === 'string' && optionsRef && depWaiting}
  <!-- Dependent field with no dependency selected yet: allow free text so
       authoring isn't blocked, and hint that picking the dependency enables
       the dropdown. -->
  <input
    type="text"
    value={value as string ?? ''}
    placeholder={`select ${depOn.split('.').pop()} to list options`}
    on:input={(e) => set((e.currentTarget as HTMLInputElement).value)}
  />
{:else if field.type === 'string' && optionsRef}
  {@const optLabel = depField ? 'options' : optionsRef.kind}
  <select
    value={value as string ?? ''}
    disabled={optionsNames === null && !optionsError}
    on:change={(e) => set((e.currentTarget as HTMLSelectElement).value)}
  >
    {#if optionsNames === null && !optionsError}
      <option value="">Loading {optLabel}…</option>
    {:else if optionsError}
      <option value="">Couldn't load {optLabel}</option>
    {:else if optionsNames?.length === 0}
      <option value="">No {optLabel} available</option>
    {:else}
      {#if field.optional || value === '' || value === undefined || value === null}
        <option value="">— pick {depField ? 'an option' : `a ${optionsRef.kind}`} —</option>
      {/if}
      {#each optionsNames ?? [] as name}
        <option value={name}>{name}</option>
      {/each}
      <!-- Preserve a value that isn't in the fetched list (e.g. a
           node that got renamed/removed) so the form doesn't silently
           drop it on save. -->
      {#if value && optionsNames && !optionsNames.includes(value as string)}
        <option value={value as string}>{value} (not found)</option>
      {/if}
    {/if}
  </select>
{:else if field.type === 'string' && field.enum && field.enum.length > 0}
  <select
    value={value as string ?? ''}
    on:change={(e) => set((e.currentTarget as HTMLSelectElement).value)}
  >
    {#if field.optional && (value === '' || value === undefined || value === null)}
      <option value="">— unset —</option>
    {/if}
    {#each field.enum as opt}
      <option value={opt}>{opt}</option>
    {/each}
  </select>
{:else if field.type === 'string'}
  <input
    type="text"
    value={value as string ?? ''}
    pattern={field.pattern ?? null}
    title={field.pattern ? `Must match: ${field.pattern}` : null}
    on:input={(e) => set((e.currentTarget as HTMLInputElement).value)}
  />
{:else if field.type === 'int' || field.type === 'number'}
  <input
    type="number"
    value={value === null || value === undefined ? '' : (value as number)}
    min={field.min}
    max={field.max}
    step={field.type === 'int' ? 1 : 'any'}
    on:input={(e) => set(coerceNumber((e.currentTarget as HTMLInputElement).value))}
  />
{:else if field.type === 'bool'}
  <label class="bool">
    <input
      type="checkbox"
      checked={!!value}
      on:change={(e) => set((e.currentTarget as HTMLInputElement).checked)}
    />
    <span>{!!value ? 'true' : 'false'}</span>
  </label>
{:else if field.type === 'any'}
  <textarea
    rows="3"
    placeholder="freeform (JSON / YAML)"
    value={value === null || value === undefined ? '' : typeof value === 'string' ? value : JSON.stringify(value)}
    on:input={(e) => set((e.currentTarget as HTMLTextAreaElement).value)}
  ></textarea>
{:else}
  <!-- type === 'unsupported' -->
  <div class="unsupported" title={field.reason ?? ''}>
    not editable in form view{#if field.reason}: {field.reason}{/if}
  </div>
{/if}

<style>
  .secret-field {
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
    min-width: 0;
  }
  .secret-row {
    display: flex;
    gap: 0.4rem;
  }
  .secret-row select {
    flex: 0 0 auto;
  }
  .secret-row input {
    flex: 1 1 auto;
    min-width: 0;
  }
  .secret-hint {
    margin: 0;
    font-size: 0.78rem;
    opacity: 0.7;
  }
  .secret-warn {
    margin: 0;
    font-size: 0.78rem;
    color: #e0a030;
  }
  .object {
    display: flex;
    flex-direction: column;
    gap: 0.85rem;
    min-width: 0;
  }
  /* Subtle left border for nested objects. Indent is intentionally
     small (0.6rem) so deep nesting still leaves usable input width. */
  :global(.object .object) {
    border-left: 1px solid rgba(127, 127, 127, 0.15);
    padding-left: 0.6rem;
    margin-top: 0.3rem;
  }
  .row {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    min-width: 0;
  }
  .row-head {
    display: flex;
    align-items: center;
    gap: 0.4rem;
  }
  .oneof-group {
    display: flex;
    flex-direction: column;
    gap: 0.6rem;
    padding: 0.6rem 0.75rem;
    background: rgba(74, 142, 240, 0.05);
    border-left: 2px solid rgba(74, 142, 240, 0.35);
    border-radius: 4px;
  }
  .oneof-picker {
    display: flex;
    flex-wrap: wrap;
    gap: 0.35rem;
    align-items: center;
  }
  .oneof-choice {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.25em 0.7em;
    border: 1px solid rgba(127, 127, 127, 0.25);
    border-radius: 4px;
    font-size: 0.85rem;
    cursor: pointer;
    background: transparent;
  }
  .oneof-choice.active {
    background: rgba(74, 142, 240, 0.15);
    border-color: #4a8ef0;
    color: #6ea8ff;
  }
  .oneof-choice input {
    margin: 0;
  }
  .oneof-clear {
    background: transparent;
    border: none;
    color: #888;
    font-size: 0.75rem;
    text-decoration: underline;
    cursor: pointer;
    padding: 0 0.35em;
  }
  .oneof-clear:hover {
    color: #ff8980;
  }
  .add-opt {
    align-self: flex-start;
    background: transparent;
    border: 1px dashed rgba(127, 127, 127, 0.4);
    color: #aaa;
    padding: 0.3em 0.8em;
    font-size: 0.85rem;
    border-radius: 4px;
    cursor: pointer;
  }
  .add-opt:hover {
    border-style: solid;
    border-color: #6ea8ff;
    color: #6ea8ff;
  }
  .row-clear {
    background: transparent;
    border: 1px solid transparent;
    color: #888;
    width: 1.4rem;
    height: 1.4rem;
    padding: 0;
    border-radius: 4px;
    cursor: pointer;
    font-size: 1rem;
    line-height: 1;
  }
  .row-clear:hover {
    color: #ff8980;
    border-color: rgba(255, 137, 128, 0.4);
  }
  /* Rows with a schema violation get a soft red left-border rail and
     an inline message below the input. Highlight is intentionally
     mild so the form doesn't flash on every keystroke during a
     multi-field edit. */
  .row-error {
    border-left: 2px solid rgba(255, 137, 128, 0.5);
    padding-left: 0.5rem;
    margin-left: -0.5rem;
  }
  .field-err {
    color: #ff8980;
    font-size: 0.75rem;
    margin: 0.1rem 0 0;
    font-family: ui-monospace, SFMono-Regular, monospace;
    white-space: pre-wrap;
  }
  .row-label {
    font-size: 0.85rem;
    color: #aaa;
    font-family: ui-monospace, SFMono-Regular, monospace;
  }
  .req {
    color: #ff8980;
    margin-left: 0.2em;
  }
  .desc {
    color: #888;
    font-size: 0.8rem;
    margin: 0;
  }
  .small {
    font-size: 0.75rem;
  }
  input[type='text'],
  input[type='number'],
  textarea {
    width: 100%;
    box-sizing: border-box;
    font-family: inherit;
    padding: 0.4rem 0.6rem;
    background: rgba(0, 0, 0, 0.2);
    color: inherit;
    border: 1px solid rgba(127, 127, 127, 0.25);
    border-radius: 4px;
    font-size: 0.9rem;
  }
  @media (prefers-color-scheme: light) {
    input[type='text'],
    input[type='number'],
    textarea {
      background: #fff;
      border-color: #ccc;
    }
  }
  input.const {
    background: rgba(127, 127, 127, 0.08);
    color: #888;
    cursor: not-allowed;
  }
  select {
    width: 100%;
    box-sizing: border-box;
    font-family: inherit;
    padding: 0.4rem 0.6rem;
    background: rgba(0, 0, 0, 0.2);
    color: inherit;
    border: 1px solid rgba(127, 127, 127, 0.25);
    border-radius: 4px;
    font-size: 0.9rem;
  }
  @media (prefers-color-scheme: light) {
    select {
      background: #fff;
      border-color: #ccc;
    }
  }
  input[type='text']:invalid {
    border-color: #ff8980;
  }
  .map {
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
  }
  .map-row {
    display: grid;
    grid-template-columns: minmax(8rem, 1fr) auto minmax(8rem, 2fr) auto;
    gap: 0.35rem;
    align-items: center;
    padding: 0.3rem;
    background: rgba(127, 127, 127, 0.06);
    border-radius: 4px;
  }
  /* When the value type is composite (object/array/map), the row
     stacks: the key + remove button on top; the nested sub-form
     spans the full width underneath. Fixes ipConfig-shaped maps
     that used to align the key input with the middle of the
     nested form. */
  .map-row-stacked {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .map-key-line {
    display: flex;
    align-items: center;
    gap: 0.35rem;
  }
  .map-row-stacked .map-key-line .map-key {
    max-width: 20rem;
  }
  .map-row-stacked .map-key-line .remove {
    margin-left: auto;
  }
  .map-nested {
    padding-left: 0.75rem;
    border-left: 1px solid rgba(127, 127, 127, 0.15);
  }
  .map-sep {
    color: #888;
  }
  .map-key {
    font-family: ui-monospace, SFMono-Regular, monospace;
  }
  .map-value {
    min-width: 0;
  }
  .bool {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.9rem;
    cursor: pointer;
  }
  .array {
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
  }
  .array-row {
    display: flex;
    gap: 0.5rem;
    align-items: flex-start;
    padding: 0.5rem;
    background: rgba(127, 127, 127, 0.06);
    border-radius: 4px;
  }
  .array-row-body {
    flex: 1;
    min-width: 0;
  }
  .remove {
    background: transparent;
    border: 1px solid rgba(127, 127, 127, 0.25);
    color: #aaa;
    width: 1.8rem;
    height: 1.8rem;
    padding: 0;
    border-radius: 4px;
    cursor: pointer;
    flex-shrink: 0;
  }
  .remove:hover {
    color: #ff8980;
    border-color: #ff8980;
  }
  .add {
    align-self: flex-start;
    font-size: 0.85rem;
    padding: 0.3em 0.8em;
    background: transparent;
    border: 1px dashed rgba(127, 127, 127, 0.4);
    color: #aaa;
  }
  .add:hover {
    border-style: solid;
    border-color: #6ea8ff;
    color: #6ea8ff;
  }
  .unsupported {
    color: #888;
    font-style: italic;
    font-size: 0.85rem;
    padding: 0.4rem 0.6rem;
    background: rgba(127, 127, 127, 0.08);
    border-radius: 4px;
  }
</style>
