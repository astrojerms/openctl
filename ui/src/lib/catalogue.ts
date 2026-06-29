import { writable, type Writable } from 'svelte/store';
import { schemas, resources, type SchemaInfo } from './api';

// Catalogue is the populated nav model: every kind the controller knows,
// each carrying the live resource count for the badge. We fetch once on
// shell mount, then refresh on demand (after an apply etc.). U3.4 will
// drive refreshes via Watch streams so the count is always live.
//
// counts is keyed by `<apiVersion>/<kind>` so two providers can share a
// kind name without colliding. `null` value = count fetch in flight or
// failed; the UI just hides the badge in that state.

export interface KindEntry extends SchemaInfo {
  // `<apiVersion>/<kind>` — stable key.
  key: string;
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

export async function refreshCatalogue(): Promise<void> {
  catalogueError.set('');
  let schemaList: SchemaInfo[];
  try {
    const resp = await schemas.list();
    schemaList = resp.schemas ?? [];
  } catch (err) {
    catalogueError.set(err instanceof Error ? err.message : String(err));
    return;
  }

  // Sort kinds: provider asc, kind asc — stable nav ordering.
  schemaList.sort((a, b) => {
    const p = a.provider.localeCompare(b.provider);
    return p !== 0 ? p : a.kind.localeCompare(b.kind);
  });

  // Seed entries with null counts so the nav can render immediately while
  // per-kind List requests fan out in parallel.
  const entries: KindEntry[] = schemaList.map((s) => ({
    ...s,
    key: kindKey(s.apiVersion, s.kind),
    count: null,
  }));
  publish(entries);

  await Promise.all(
    entries.map(async (e) => {
      try {
        const r = await resources.list(e.apiVersion, e.kind);
        e.count = r.resources?.length ?? 0;
      } catch {
        // Leave count null; the kind still appears in the nav so the
        // user can click in and see the error in the list view.
        e.count = null;
      }
      publish(entries);
    }),
  );
}

function publish(entries: KindEntry[]): void {
  const byProvider = new Map<string, KindEntry[]>();
  for (const e of entries) {
    const bucket = byProvider.get(e.provider) ?? [];
    bucket.push(e);
    byProvider.set(e.provider, bucket);
  }
  catalogue.set({ byProvider, flat: entries });
}
