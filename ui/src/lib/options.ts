// Lazy cache of resource-name lists, keyed by "apiVersion/kind". Used
// by FormField to render `@options(kind="X")`-annotated string fields
// as select dropdowns instead of free-text inputs.
//
// The cache is per-session and shared across all FormField instances
// in the page. ensureOptions kicks off a fetch on first request; the
// store update triggers re-render of every subscribed field.
//
// This is deliberately a simple one-shot fetch — no live Watch — to
// keep the create form fast and predictable. If ProxmoxNode names
// change while the form is open, the user reloads. That's fine for
// homelab-scale infra where nodes rarely appear/disappear during a
// single Create session.

import { get, writable, type Readable } from 'svelte/store';
import { resources, UnauthorizedError } from './api';

type Names = string[];

interface OptionsState {
  // Cached name lists, keyed as "apiVersion/kind".
  data: Record<string, Names>;
  // Error text keyed the same way. Present when the fetch failed and
  // we want the renderer to surface a graceful "couldn't load" hint
  // (rather than showing an empty dropdown that looks like "no nodes
  // exist").
  errors: Record<string, string>;
}

const store = writable<OptionsState>({ data: {}, errors: {} });
const inflight = new Set<string>();

function key(apiVersion: string, kind: string): string {
  return `${apiVersion}/${kind}`;
}

// ensureOptions fetches the name list for (apiVersion, kind) once,
// dedup-ing concurrent callers. Subsequent calls with the same key
// are no-ops until refreshOptions is called.
export async function ensureOptions(apiVersion: string, kind: string): Promise<void> {
  const k = key(apiVersion, kind);
  if (inflight.has(k)) return;
  const state = get(store);
  if (k in state.data || k in state.errors) return;
  inflight.add(k);
  try {
    const resp = await resources.list(apiVersion, kind);
    const names = (resp.resources ?? [])
      .map((r) => r.metadata.name)
      .sort((a, b) => a.localeCompare(b));
    store.update((s) => ({ ...s, data: { ...s.data, [k]: names } }));
  } catch (err) {
    if (err instanceof UnauthorizedError) return;
    const msg = err instanceof Error ? err.message : String(err);
    store.update((s) => ({ ...s, errors: { ...s.errors, [k]: msg } }));
  } finally {
    inflight.delete(k);
  }
}

// refreshOptions drops the cached entry so the next ensureOptions call
// refetches. Not wired to anything yet; exported for future use (e.g.
// after a Create/Delete completes for the referenced kind).
export function refreshOptions(apiVersion: string, kind: string): void {
  const k = key(apiVersion, kind);
  store.update((s) => {
    const nextData = { ...s.data };
    const nextErrors = { ...s.errors };
    delete nextData[k];
    delete nextErrors[k];
    return { ...s, data: nextData, errors: nextErrors };
  });
}

// getOptions returns the resolved names for a key, or null if the
// fetch is still in flight / hasn't been requested. Callers should
// pair this with ensureOptions() to trigger the fetch on demand.
export function getOptions(apiVersion: string, kind: string): Names | null {
  const s = get(store);
  const k = key(apiVersion, kind);
  return s.data[k] ?? null;
}

// optionsStore is the underlying Svelte store; subscribe from a
// component to re-render when a fetch resolves.
export const optionsStore: Readable<OptionsState> = store;

// --- Dependent options ---------------------------------------------------
// A `@options(kind, field, dependsOn)` field reads its list from a dotted
// path (e.g. "status.storages") of a SPECIFIC resource instance, named by the
// value of another field (dependsOn). Example: a VM's disk `storage` options
// come from the selected ProxmoxNode's `status.storages`. These are keyed by
// "apiVersion/kind/name/field" so switching the depended-on node re-resolves.

function depKey(apiVersion: string, kind: string, name: string, field: string): string {
  return `${apiVersion}/${kind}/${name}/${field}`;
}

// readPath walks a dotted path into an object and returns a string[] when the
// resolved value is a string array, else null.
function readStringList(obj: unknown, path: string): string[] | null {
  let cur: unknown = obj;
  for (const seg of path.split('.')) {
    if (cur == null || typeof cur !== 'object') return null;
    cur = (cur as Record<string, unknown>)[seg];
  }
  if (Array.isArray(cur) && cur.every((v) => typeof v === 'string')) return cur as string[];
  return null;
}

// ensureDependentOptions fetches a specific resource once and extracts the
// string list at `field`. Deduped and cached like ensureOptions. `name` is the
// depended-on value (e.g. the selected node); an empty name is a no-op (the
// dependency isn't chosen yet).
export async function ensureDependentOptions(
  apiVersion: string,
  kind: string,
  name: string,
  field: string,
): Promise<void> {
  if (!name) return;
  const k = depKey(apiVersion, kind, name, field);
  if (inflight.has(k)) return;
  const state = get(store);
  if (k in state.data || k in state.errors) return;
  inflight.add(k);
  try {
    const resp = await resources.get(apiVersion, kind, name);
    const list = readStringList(resp.resource, field) ?? [];
    store.update((s) => ({ ...s, data: { ...s.data, [k]: [...list].sort((a, b) => a.localeCompare(b)) } }));
  } catch (err) {
    if (err instanceof UnauthorizedError) return;
    const msg = err instanceof Error ? err.message : String(err);
    store.update((s) => ({ ...s, errors: { ...s.errors, [k]: msg } }));
  } finally {
    inflight.delete(k);
  }
}

// dependentKey builds the store key a component uses to read dependent options.
export function dependentKey(apiVersion: string, kind: string, name: string, field: string): string {
  return depKey(apiVersion, kind, name, field);
}
