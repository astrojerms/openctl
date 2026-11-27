import { writable } from 'svelte/store';
import { overviewApi, UnauthorizedError, type OverviewResponse } from './api';

export interface OverviewState {
  snapshot: OverviewResponse | null;
  loading: boolean;
  error: string;
}

const emptyState = (): OverviewState => ({ snapshot: null, loading: false, error: '' });
const state = writable<OverviewState>(emptyState());
export const overview = { subscribe: state.subscribe };
const POLL_MS = 5000;

type PollSession = { controller: AbortController; timer?: ReturnType<typeof setTimeout> };
let session: PollSession | null = null;

// Missing fields are not zero. A matching controller must return a complete
// snapshot, including explicit zero totals, rather than triggering live scans.
function completeSnapshot(snapshot: OverviewResponse): boolean {
  const counts = [snapshot.managedResources, snapshot.pendingOperations, snapshot.runningOperations, snapshot.reviewResources];
  return typeof snapshot.asOf === 'string' && !Number.isNaN(Date.parse(snapshot.asOf))
    && counts.every((count) => Number.isSafeInteger(count) && count >= 0)
    && Array.isArray(snapshot.kinds) && Array.isArray(snapshot.activeOperations) && Array.isArray(snapshot.reviewOperations)
    && snapshot.kinds.every((kind) => typeof kind.apiVersion === 'string' && typeof kind.kind === 'string'
      && Number.isSafeInteger(kind.resources) && kind.resources >= 0);
}

export function startOverview(): void {
  if (session) return;
  const current: PollSession = { controller: new AbortController() };
  session = current;
  state.set({ snapshot: null, loading: true, error: '' });
  void poll(current);
}

export function stopOverview(): void {
  const previous = session;
  session = null;
  previous?.controller.abort();
  if (previous?.timer !== undefined) clearTimeout(previous.timer);
  state.set(emptyState());
}

async function poll(current: PollSession): Promise<void> {
  let terminal = false;
  try {
    const snapshot = await overviewApi.get(10, { signal: current.controller.signal });
    if (session !== current || current.controller.signal.aborted) return;
    if (!completeSnapshot(snapshot)) throw new Error('Incomplete overview response; a matching controller is required.');
    state.set({ snapshot, loading: false, error: '' });
  } catch (err) {
    if (session !== current || current.controller.signal.aborted) return;
    terminal = err instanceof UnauthorizedError;
    state.update((previous) => ({
      snapshot: previous.snapshot,
      loading: false,
      error: terminal ? 'Session expired.' : err instanceof Error ? err.message : String(err),
    }));
  }
  // Schedule only after settlement: slow requests never overlap. The session
  // identity also rejects late responses from an aborted logout/restart.
  if (session === current && !current.controller.signal.aborted && !terminal) {
    current.timer = setTimeout(() => { void poll(current); }, POLL_MS);
  }
}
