import { writable, type Readable } from 'svelte/store';
import { inventoryApi, resources, UnauthorizedError, type Resource, type SavedResourceMetadata } from './api';
import { statusBadge, type StatusBadge } from './format';
import { watchResources, type WatchEvent } from './watch';

export type ObservationState = 'pending' | 'fresh' | 'stale' | 'unavailable' | 'not-returned';

export interface ResourceListRow {
  name: string;
  saved?: SavedResourceMetadata;
  observed?: Resource;
  observation: ObservationState;
}

export interface ResourceListState {
  rows: ResourceListRow[];
  savedLoading: boolean;
  liveLoading: boolean;
  savedKnown: boolean;
  savedError: string;
  observationError: string;
}

export interface ResourceListController extends Readable<ResourceListState> {
  select(apiVersion: string, kind: string, observedOnly: boolean): void;
  stop(): void;
}
const emptyState = (): ResourceListState => ({
  rows: [], savedLoading: false, liveLoading: false, savedKnown: false,
  savedError: '', observationError: '',
});
const SAVED_REFRESH_MS = 5000;

type ListSession = {
  apiVersion: string;
  kind: string;
  observedOnly: boolean;
  controller: AbortController;
  saved: Map<string, SavedResourceMetadata>;
  observed: Map<string, { resource: Resource; fresh: boolean }>;
  notReturned: Set<string>;
  savedKnown: boolean;
  liveKnown: boolean;
  savedLoading: boolean;
  liveLoading: boolean;
  savedError: string;
  observationError: string;
  savedTimer?: ReturnType<typeof setTimeout>;
  retryTimer?: ReturnType<typeof setTimeout>;
  backoffMs: number;
};

// Per-mounted-list state: only the selected kind probes providers. Persisted
// metadata and provider observations never share a Resource object.
export function createResourceListState(): ResourceListController {
  const state = writable<ResourceListState>(emptyState());
  let session: ListSession | null = null;

  function active(current: ListSession): boolean {
    return session === current && !current.controller.signal.aborted;
  }

  function dispose(current: ListSession | null): void {
    current?.controller.abort();
    if (current?.savedTimer !== undefined) clearTimeout(current.savedTimer);
    if (current?.retryTimer !== undefined) clearTimeout(current.retryTimer);
  }

  function matches(current: ListSession, resource: { apiVersion: string; kind: string }): boolean {
    return resource.apiVersion === current.apiVersion && resource.kind === current.kind;
  }

  function publish(current: ListSession): void {
    if (!active(current)) return;
    const names = new Set([...current.saved.keys(), ...current.observed.keys()]);
    const rows = [...names].map((name): ResourceListRow => {
      const observed = current.observed.get(name);
      return {
        name, saved: current.saved.get(name), observed: observed?.resource,
        observation: observed ? observed.fresh ? 'fresh' : 'stale'
          : current.notReturned.has(name) ? 'not-returned'
          : current.observationError ? 'unavailable'
          : current.liveKnown ? 'not-returned' : 'pending',
      };
    }).sort((a, b) => a.name.localeCompare(b.name));
    state.set({
      rows, savedLoading: current.savedLoading, liveLoading: current.liveLoading,
      savedKnown: current.savedKnown, savedError: current.savedError,
      observationError: current.observationError,
    });
  }

  async function refreshSaved(current: ListSession): Promise<void> {
    try {
      const response = await inventoryApi.list(current.apiVersion, current.kind, { signal: current.controller.signal });
      if (!active(current)) return;
      // Only a successful metadata snapshot can remove saved intent. Provider
      // omissions and DELETED watch events have no authority over this map.
      current.saved = new Map((response.resources ?? []).filter((resource) => matches(current, resource))
        .map((resource) => [resource.metadata.name, resource]));
      current.savedKnown = true;
      current.savedError = '';
    } catch (error) {
      if (!active(current)) return;
      current.savedError = error instanceof Error ? error.message : String(error);
      if (error instanceof UnauthorizedError) {
        expire(current);
        return;
      }
    }
    if (!active(current)) return;
    current.savedLoading = false;
    publish(current);
    // Schedule after settlement, independently of watch traffic: lightweight
    // metadata refresh notices completed Apply/Delete without event fan-out or
    // overlapping requests when the manifest store is slow.
    current.savedTimer = setTimeout(() => { void refreshSaved(current); }, SAVED_REFRESH_MS);
  }

  function stale(current: ListSession, error: string): void {
    current.observationError = error;
    current.notReturned.clear();
    for (const observed of current.observed.values()) observed.fresh = false;
    publish(current);
  }

  function expire(current: ListSession): void {
    dispose(current);
    session = null;
    state.set({ ...emptyState(), savedError: 'Session expired.', observationError: 'Session expired.' });
  }

  function applyEvent(current: ListSession, event: WatchEvent): void {
    if (!active(current) || !matches(current, event.resource)) return;
    const name = event.resource.metadata.name;
    if (event.type === 'DELETED') {
      current.observed.delete(name);
      current.notReturned.add(name);
    } else if (event.type === 'ADDED' || event.type === 'MODIFIED') {
      current.observed.set(name, { resource: event.resource, fresh: true });
      current.notReturned.delete(name);
    } else return;
    current.backoffMs = 1000;
    publish(current);
  }

  async function observe(current: ListSession): Promise<void> {
    try {
      const response = await resources.list(current.apiVersion, current.kind, { signal: current.controller.signal });
      if (!active(current)) return;
      current.observed = new Map((response.resources ?? []).filter((resource) => matches(current, resource))
        .map((resource) => [resource.metadata.name, { resource, fresh: true }]));
      current.notReturned.clear();
      current.liveKnown = true;
      current.observationError = '';
    } catch (error) {
      if (!active(current)) return;
      stale(current, error instanceof Error ? error.message : String(error));
      if (error instanceof UnauthorizedError) {
        expire(current);
        return;
      }
    }
    if (!active(current)) return;
    current.liveLoading = false;
    publish(current);
    try {
      await watchResources(current.apiVersion, current.kind, undefined,
        (event) => applyEvent(current, event), { signal: current.controller.signal });
      if (!active(current)) return;
      stale(current, 'Live observation stream disconnected.');
    } catch (error) {
      if (!active(current)) return;
      stale(current, error instanceof Error ? error.message : String(error));
      if (error instanceof UnauthorizedError) {
        expire(current);
        return;
      }
    }
    if (!active(current)) return;
    current.retryTimer = setTimeout(() => { void observe(current); }, current.backoffMs);
    current.backoffMs = Math.min(current.backoffMs * 2, 15_000);
  }

  function select(apiVersion: string, kind: string, observedOnly: boolean): void {
    if (session && active(session) && session.apiVersion === apiVersion
      && session.kind === kind && session.observedOnly === observedOnly) return;
    dispose(session);
    const current: ListSession = {
      apiVersion, kind, observedOnly, controller: new AbortController(),
      saved: new Map(), observed: new Map(), notReturned: new Set(),
      savedKnown: false, liveKnown: false, savedLoading: !observedOnly,
      liveLoading: true, savedError: '', observationError: '', backoffMs: 1000,
    };
    session = current;
    publish(current);
    if (!observedOnly) void refreshSaved(current);
    void observe(current);
  }

  function stop(): void {
    dispose(session);
    session = null;
    state.set(emptyState());
  }

  return { subscribe: state.subscribe, select, stop };
}

export function resourceRowStatus(row: ResourceListRow): StatusBadge {
  switch (row.observation) {
    case 'fresh': return statusBadge(row.observed);
    case 'stale': return { label: `Stale · ${statusBadge(row.observed).label}`, tone: 'unknown' };
    case 'unavailable': return { label: 'Observation unavailable', tone: 'unknown' };
    case 'not-returned': return { label: 'Not returned by observation', tone: 'unknown' };
    case 'pending': return { label: 'Observation pending', tone: 'unknown' };
  }
}

export function resourceRowDrift(row: ResourceListRow): number | null {
  return row.observation === 'fresh' ? row.observed?.drift?.length ?? 0 : null;
}
