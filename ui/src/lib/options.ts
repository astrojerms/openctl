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
