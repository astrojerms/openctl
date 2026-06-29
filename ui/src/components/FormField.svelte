<script lang="ts">
  import { createEventDispatcher } from 'svelte';
  import type { FormField } from '../lib/formSchema';
  import { initialValue } from '../lib/formSchema';
  import Self from './FormField.svelte';

  // The field schema and its current value. Value is `unknown` here
  // because every Field can carry a different shape; downstream the
  // dispatch on `field.type` narrows it.
  export let field: FormField;
  export let value: unknown;
  // Depth controls indent for nested objects. The root container in
  // the form renderer passes depth=0.
  export let depth: number = 0;

  const dispatch = createEventDispatcher<{ change: unknown }>();

  function set(v: unknown) {
    value = v;
    dispatch('change', v);
  }

  function setObjectKey(key: string, v: unknown) {
    const obj = (typeof value === 'object' && value && !Array.isArray(value))
      ? { ...(value as Record<string, unknown>) }
      : {};
    obj[key] = v;
    set(obj);
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
    {#each field.fields ?? [] as child (child.name)}
      {@const obj = (typeof value === 'object' && value && !Array.isArray(value)) ? value : {}}
      {@const childValue = (obj as Record<string, unknown>)[child.name ?? '']}
      <div class="row">
        <span class="row-label">
          {child.name}{#if !child.optional}<span class="req" aria-label="required">*</span>{/if}
        </span>
        <div class="row-value">
          <Self
            field={child}
            value={childValue}
            depth={depth + 1}
            on:change={(e) => setObjectKey(child.name ?? '', e.detail)}
          />
          {#if child.description && child.type !== 'object'}
            <p class="desc small">{child.description}</p>
          {/if}
        </div>
      </div>
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
  <div class="map">
    {#if value && typeof value === 'object' && !Array.isArray(value)}
      {#each Object.entries(value as Record<string, unknown>) as [k, v] (k)}
        <div class="map-row">
          <input
            type="text"
            class="map-key"
            value={k}
            placeholder="key"
            on:change={(e) => setMapKey(k, (e.currentTarget as HTMLInputElement).value)}
          />
          <span class="map-sep">:</span>
          <div class="map-value">
            <Self
              field={field.valueType ?? { type: 'string' }}
              value={v}
              depth={depth + 1}
              on:change={(e) => setMapValue(k, e.detail)}
            />
          </div>
          <button type="button" class="remove" on:click={() => removeMapKey(k)}
            title="Remove entry">×</button>
        </div>
      {/each}
    {/if}
    <button type="button" class="add" on:click={addMapRow}>+ Add row</button>
  </div>
{:else if field.const !== undefined}
  <input class="const" type="text" value={String(field.const)} readonly tabindex="-1" />
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
  .object {
    display: flex;
    flex-direction: column;
    gap: 0.6rem;
  }
  /* Nested object indent via inline padding-left from --depth, set
     on the wrapper. CSS-only nested-selector .object .object doesn't
     work because each :host .object is a separate root in Svelte. */
  :global(.object .object) {
    border-left: 1px solid rgba(127, 127, 127, 0.15);
    padding-left: 0.75rem;
    margin-top: 0.3rem;
  }
  .row {
    display: grid;
    grid-template-columns: 10rem 1fr;
    align-items: start;
    gap: 0.75rem;
  }
  .row-label {
    font-size: 0.85rem;
    color: #aaa;
    padding-top: 0.4rem;
    font-family: ui-monospace, SFMono-Regular, monospace;
  }
  .req {
    color: #ff8980;
    margin-left: 0.2em;
  }
  .row-value {
    min-width: 0;
  }
  .desc {
    color: #888;
    font-size: 0.8rem;
    margin: 0;
  }
  .small {
    font-size: 0.75rem;
    margin-top: 0.2rem;
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
