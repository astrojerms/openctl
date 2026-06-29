import { writable, type Writable } from 'svelte/store';
import { schemas, type SchemaInfo, UnauthorizedError } from './api';
import { watchResources } from './watch';

// Catalogue is the populated nav model: every kind the controller knows,
// each carrying a live resource count for the badge. We:
//   1. Fetch the kind list once on shell mount (`refreshCatalogue`).
//   2. Open a long-lived Watch per kind; ADDED grows the count, DELETED
//      shrinks it, MODIFIED leaves it unchanged. Catalogue counts stay
//      live without per-tick polling.
//
// counts is keyed by `<apiVersion>/<kind>` so two providers can share a
// kind name without colliding. `null` value = count fetch in flight or
// failed; the UI just hides the badge in that state.

export interface KindEntry extends SchemaInfo {
  key: string;            // `<apiVersion>/<kind>`
  count: number | null;
}

export interface Catalogue {
  byProvider: Map<string, KindEntry[]>;
  flat: KindEntry[];
}

export const catalogue: Writable<Catalogue> = writable({
  byProvider: new Map(),
  flat: [],
});

export const catalogueError = writable<string>('');

function kindKey(apiVersion: string, kind: string): string {
  return `${apiVersion}/${kind}`;
}

let aborter: AbortController | null = null;
let entries: KindEntry[] = [];
// Per-kind set of resource names — used to recompute the count on each
// Watch event without re-fetching the list. Keyed by kindKey.
const names: Map<string, Set<string>> = new Map();

export async function refreshCatalogue(): Promise<void> {
  catalogueError.set('');
  aborter?.abort();
  aborter = new AbortController();

  let schemaList: SchemaInfo[];
  try {
    const resp = await schemas.list();
    schemaList = resp.schemas ?? [];
  } catch (err) {
    catalogueError.set(err instanceof Error ? err.message : String(err));
    return;
  }

  schemaList.sort((a, b) => {
    const p = a.provider.localeCompare(b.provider);
    return p !== 0 ? p : a.kind.localeCompare(b.kind);
  });

  entries = schemaList.map((s) => ({
    ...s,
    key: kindKey(s.apiVersion, s.kind),
    count: null,
  }));
  names.clear();
  for (const e of entries) names.set(e.key, new Set());
  publish();

  for (const e of entries) {
    void runWatcher(e, aborter.signal);
  }
}

export function stopCatalogue(): void {
  aborter?.abort();
  aborter = null;
}

async function runWatcher(e: KindEntry, signal: AbortSignal): Promise<void> {
  let backoffMs = 1000;
  while (!signal.aborted) {
    // Reset the per-kind name set on each reconnect — the snapshot
    // ADDED events that come back will repopulate it. This keeps the
    // count correct even if a resource was deleted while we were
    // disconnected.
    names.get(e.key)?.clear();
    e.count = 0;
    publish();
    try {
      await watchResources(
        e.apiVersion,
        e.kind,
        undefined,
        (ev) => onEvent(e.key, ev.type, ev.resource.metadata.name),
        { signal },
      );
      backoffMs = 1000;
    } catch (err) {
      if (signal.aborted) return;
      if (err instanceof UnauthorizedError) return;
      if (err instanceof DOMException && err.name === 'AbortError') return;
      // Surface as null count so the badge hides; leave the kind in the
      // nav so the user can still click in and read the error.
      e.count = null;
      publish();
      backoffMs = Math.min(backoffMs * 2, 30_000);
    }
    await new Promise((r) => setTimeout(r, backoffMs));
  }
}

function onEvent(key: string, type: string, name: string): void {
  const set = names.get(key);
  if (!set) return;
  const entry = entries.find((e) => e.key === key);
  if (!entry) return;
  if (type === 'DELETED') {
    set.delete(name);
  } else {
    set.add(name);
  }
  entry.count = set.size;
  publish();
}

function publish(): void {
  const byProvider = new Map<string, KindEntry[]>();
  for (const e of entries) {
    const bucket = byProvider.get(e.provider) ?? [];
    bucket.push(e);
    byProvider.set(e.provider, bucket);
  }
  catalogue.set({ byProvider, flat: entries.slice() });
}
