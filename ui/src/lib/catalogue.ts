import { writable, type Writable } from 'svelte/store';
import { schemas, resources, type SchemaInfo, UnauthorizedError } from './api';

// Catalogue is the populated nav model: every kind the controller knows,
// each carrying a resource count for the badge. We:
//   1. Fetch the kind list once on shell mount (`refreshCatalogue`).
//   2. Poll a one-shot List per kind on an interval to refresh counts.
//
// Why polling and not a long-lived Watch per kind: a persistent Watch holds
// one browser->gateway HTTP/1.1 connection open for its entire lifetime, and
// browsers allow only ~6 connections per origin. With one stream per kind
// (5 kinds today) plus the ops-drawer's WatchOperations stream, the nav alone
// pinned all ~6 connection slots, so any unrelated page fetch (e.g. the
// Templates list `GET /v1/templates`) had no free connection and hung on
// "Loading..." forever. A transient List returns its connection immediately,
// so the persistent-stream budget is left for the ops watch and whichever
// resource page the user is actually viewing. (The gateway now speaks
// HTTP/2 — one connection, many streams — so this cap is gone and live
// Watch counts would be safe again; polling is kept because it's cheap and
// avoids holding a stream per kind for cosmetic badges.)
//
// counts is keyed by `<apiVersion>/<kind>` so two providers can share a
// kind name without colliding. `null` value = count fetch in flight or
// failed; the UI just hides the badge in that state.

// COUNT_POLL_MS is the badge-count refresh cadence. Counts are cosmetic, so
// a few seconds of staleness is fine; this trades instant updates for not
// holding a connection open per kind.
const COUNT_POLL_MS = 5000;

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

// Composite-child / expert kinds: normally produced by a parent composite (a
// k3s Cluster fans out into VMs + K3sNodes + AgentInstalls) rather than
// authored directly. The nav marks them "advanced" and the create form nudges
// toward the owning composite.
//
// This is now backend-derived: the controller stamps `advanced`/`ownerKind`/
// `advancedNote` onto each SchemaInfo (from the provider's AdvancedKindDescriber
// capability), so an external plugin's composite children get flagged too — no
// hardcoded client-side list. `advancedByKey` is a lookup rebuilt on every
// catalogue refresh, keyed by `<apiVersion>/<kind>`, for callers (the create
// form) that only hold an (apiVersion, kind) pair rather than the full entry.
const advancedByKey = new Map<string, { owner: string; note: string }>();

// advancedKind returns the advanced-kind metadata for (apiVersion, kind), or
// undefined for ordinary top-level kinds. Reflects the last catalogue refresh.
export function advancedKind(apiVersion: string, kind: string): { owner: string; note: string } | undefined {
  return advancedByKey.get(kindKey(apiVersion, kind));
}

let aborter: AbortController | null = null;
let entries: KindEntry[] = [];

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

  advancedByKey.clear();
  for (const s of schemaList) {
    if (s.advanced) {
      advancedByKey.set(kindKey(s.apiVersion, s.kind), {
        owner: s.ownerKind ?? '',
        note: s.advancedNote ?? '',
      });
    }
  }

  entries = schemaList.map((s) => ({
    ...s,
    key: kindKey(s.apiVersion, s.kind),
    count: null,
  }));
  publish();

  for (const e of entries) {
    void runCounter(e, aborter.signal);
  }
}

export function stopCatalogue(): void {
  aborter?.abort();
  aborter = null;
}

// runCounter polls List for one kind on an interval, updating its badge
// count. Each List is a short-lived request that releases its connection
// immediately, unlike the persistent Watch this replaced. On error the
// badge hides (count=null) and we keep polling with a backoff — a provider
// (e.g. an offline Proxmox host) may recover.
async function runCounter(e: KindEntry, signal: AbortSignal): Promise<void> {
  let backoffMs = COUNT_POLL_MS;
  while (!signal.aborted) {
    try {
      const resp = await resources.list(e.apiVersion, e.kind);
      if (signal.aborted) return;
      e.count = resp.resources?.length ?? 0;
      publish();
      backoffMs = COUNT_POLL_MS;
    } catch (err) {
      if (signal.aborted) return;
      if (err instanceof UnauthorizedError) return;
      // Provider unreachable / transient error: hide the badge but keep the
      // kind in the nav so the user can still click in and read the error.
      e.count = null;
      publish();
      backoffMs = Math.min(backoffMs * 2, 30_000);
    }
    await sleep(backoffMs, signal);
  }
}

// sleep resolves after ms, or early if the signal aborts, so a torn-down
// catalogue stops polling promptly instead of on the next tick boundary.
function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const t = setTimeout(resolve, ms);
    signal.addEventListener(
      'abort',
      () => {
        clearTimeout(t);
        resolve();
      },
      { once: true },
    );
  });
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
