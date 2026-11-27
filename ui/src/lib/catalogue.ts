import { derived, writable } from 'svelte/store';
import { schemas, type SchemaInfo } from './api';
import { overview } from './overview';

// Schema metadata keeps navigation available independently of the overview.
// Counts come only from the shell's single persisted-inventory snapshot.
export interface KindEntry extends SchemaInfo {
  key: string;
  count: number | null;
  schemaAvailable: boolean;
}

export interface Catalogue {
  byProvider: Map<string, KindEntry[]>;
  flat: KindEntry[];
}

const schemaEntries = writable<SchemaInfo[]>([]);
export const catalogueError = writable('');
const advancedByKey = new Map<string, { owner: string; note: string }>();

function kindKey(apiVersion: string, kind: string): string {
  return `${apiVersion}/${kind}`;
}

export function advancedKind(apiVersion: string, kind: string): { owner: string; note: string } | undefined {
  return advancedByKey.get(kindKey(apiVersion, kind));
}

export const catalogue = derived([schemaEntries, overview], ([$schemas, $overview]): Catalogue => {
  const saved = new Map(($overview.snapshot?.kinds ?? []).map((kind) => [kindKey(kind.apiVersion, kind.kind), kind]));
  const fresh = $overview.snapshot !== null && !$overview.error;
  const entries = new Map<string, KindEntry>();
  for (const schema of $schemas) {
    const key = kindKey(schema.apiVersion, schema.kind);
    entries.set(key, {
      ...schema, key, schemaAvailable: true,
      count: fresh && !schema.observedOnly ? saved.get(key)?.resources ?? 0 : null,
    });
  }
  // Disabled/unregistered providers must not erase their saved inventory.
  // Do not infer a provider or capabilities from an API-version string.
  for (const [key, kind] of saved) {
    if (entries.has(key)) continue;
    entries.set(key, {
      apiVersion: kind.apiVersion, kind: kind.kind,
      provider: 'Saved inventory (schema unavailable)', fileName: '',
      key, schemaAvailable: false, count: fresh ? kind.resources : null,
    });
  }
  const flat = [...entries.values()].sort((a, b) =>
    a.provider.localeCompare(b.provider) || a.kind.localeCompare(b.kind) || a.key.localeCompare(b.key));
  const byProvider = new Map<string, KindEntry[]>();
  for (const entry of flat) {
    const bucket = byProvider.get(entry.provider) ?? [];
    bucket.push(entry);
    byProvider.set(entry.provider, bucket);
  }
  return { byProvider, flat };
});

let aborter: AbortController | null = null;

export async function refreshCatalogue(): Promise<void> {
  aborter?.abort();
  const current = new AbortController();
  aborter = current;
  catalogueError.set('');
  try {
    const response = await schemas.list({ signal: current.signal });
    if (aborter !== current || current.signal.aborted) return;
    const entries = response.schemas ?? [];
    advancedByKey.clear();
    for (const schema of entries) {
      if (schema.advanced) advancedByKey.set(kindKey(schema.apiVersion, schema.kind), {
        owner: schema.ownerKind ?? '', note: schema.advancedNote ?? '',
      });
    }
    schemaEntries.set(entries);
  } catch (err) {
    if (aborter !== current || current.signal.aborted) return;
    catalogueError.set(err instanceof Error ? err.message : String(err));
  }
}

export function stopCatalogue(): void {
  aborter?.abort();
  aborter = null;
  advancedByKey.clear();
  schemaEntries.set([]);
  catalogueError.set('');
}
