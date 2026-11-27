import { ApiError, UnauthorizedError, inventoryApi, resources, type GetResourceResponse, type GetSavedResourceResponse } from './api';
import { writable, type Readable } from 'svelte/store';
import { resourceToYAML } from './yaml';

export interface DetailTarget {
  apiVersion: string;
  kind: string;
  name: string;
}

export interface DetailInspectionState {
  data: GetResourceResponse | null;
  saved: GetSavedResourceResponse | null;
  loading: boolean;
  savedLoading: boolean;
  error: string;
  savedError: string;
  observation: 'loading' | 'fresh' | 'unavailable' | 'not-found' | 'unauthorized';
}

function emptyState(): DetailInspectionState {
  return { data: null, saved: null, loading: true, savedLoading: true, error: '', savedError: '', observation: 'loading' };
}

interface Scope {
  target: DetailTarget;
  controller: AbortController;
  version: number;
  request: Promise<void> | null;
  queued: boolean;
}

export interface DetailLease {
  signal: AbortSignal;
  target: DetailTarget;
  version: number;
}

export interface DetailInspection extends Readable<DetailInspectionState> {
  open(target: DetailTarget): void;
  refresh(): Promise<void>;
  capture(): DetailLease | null;
  current(lease: DetailLease | null, requireFresh?: boolean): boolean;
  invalidate(err: unknown): void;
  unauthorized(): void;
  stop(): void;
  canAct(): boolean;
}

// Saved intent and provider observation deliberately never share a resource slot.
// A lease ties all asynchronous detail work to one route and (for actions/children)
// one successful observation. Even a transport that ignores abort cannot publish late.
export function createDetailInspection(onUnauthorized: () => void): DetailInspection {
  let scope: Scope | null = null;
  let state = emptyState();
  const store = writable(state);
  const publish = (next: DetailInspectionState) => { state = next; store.set(state); };
  const owns = (s: Scope) => scope === s && !s.controller.signal.aborted;

  function unauthorized() {
    scope?.controller.abort();
    scope = null;
    publish({ ...emptyState(), loading: false, savedLoading: false, observation: 'unauthorized', error: 'Session expired. Sign in to inspect this resource.' });
    onUnauthorized();
  }

  async function loadSaved(s: Scope) {
    try {
      const saved = await inventoryApi.get(s.target.apiVersion, s.target.kind, s.target.name, { signal: s.controller.signal });
      if (owns(s)) publish({ ...state, saved, savedLoading: false, savedError: '' });
    } catch (err) {
      if (!owns(s)) return;
      if (err instanceof UnauthorizedError) { unauthorized(); return; }
      publish({ ...state, saved: null, savedLoading: false, savedError: err instanceof ApiError && err.status === 404
        ? 'No saved declaration on file.'
        : `Saved intent unavailable: ${err instanceof Error ? err.message : String(err)}` });
    }
  }

  async function observe(s: Scope) {
    do {
      s.queued = false;
      const version = s.version;
      try {
        const data = await resources.get(s.target.apiVersion, s.target.kind, s.target.name, { signal: s.controller.signal });
        if (owns(s) && version === s.version) publish({ ...state, data, loading: false, error: '', observation: 'fresh' });
      } catch (err) {
        if (!owns(s)) return;
        // A 401 terminates the inspection even if another refresh was queued.
        if (err instanceof UnauthorizedError) { unauthorized(); return; }
        if (version === s.version) {
          const notFound = err instanceof ApiError && err.status === 404;
          publish({ ...state, data: null, loading: false, observation: notFound ? 'not-found' : 'unavailable', error: notFound
            ? 'Provider/Get reported not found. This does not authorize replacement or prove the saved declaration is absent.'
            : `Observation unavailable: ${err instanceof Error ? err.message : String(err)}` });
        }
      }
    } while (owns(s) && s.queued);
  }

  function refresh(): Promise<void> {
    const s = scope;
    if (!s || !owns(s)) return Promise.resolve();
    s.version += 1;
    publish({ ...state, data: null, loading: true, error: '', observation: 'loading' });
    if (s.request) { s.queued = true; return s.request; }
    s.request = observe(s).finally(() => { s.request = null; });
    return s.request;
  }

  function open(target: DetailTarget) {
    scope?.controller.abort();
    scope = { target: { ...target }, controller: new AbortController(), version: 0, request: null, queued: false };
    publish(emptyState());
    void loadSaved(scope);
    void refresh();
  }

  function capture(): DetailLease | null {
    return scope && owns(scope) ? { signal: scope.controller.signal, target: scope.target, version: scope.version } : null;
  }

  function current(lease: DetailLease | null, requireFresh = false): boolean {
    return !!lease && !!scope && owns(scope) && lease.signal === scope.controller.signal
      && (!requireFresh || (lease.version === scope.version && state.observation === 'fresh' && !!state.data));
  }

  function invalidate(err: unknown) {
    if (!scope || !owns(scope)) return;
    if (err instanceof UnauthorizedError) { unauthorized(); return; }
    scope.version += 1;
    publish({ ...state, data: null, loading: false, observation: 'unavailable', error: `Observation stale / unknown: ${err instanceof Error ? err.message : String(err)}` });
  }

  return {
    subscribe: store.subscribe, open, refresh, capture, current, invalidate, unauthorized,
    stop() { scope?.controller.abort(); scope = null; },
    canAct() { return !!scope && owns(scope) && state.observation === 'fresh' && !!state.data; },
  };
}

// Inspection exports only desired intent, never observed status/drift/ownership.
// The YAML serializer preserves unresolved secret/retry markers verbatim.
export function detailManifestYAML(saved: GetSavedResourceResponse | null, data: GetResourceResponse | null): string {
  const source = saved?.resource ?? data?.applied ?? data?.resource;
  return source ? resourceToYAML(source) : '';
}
