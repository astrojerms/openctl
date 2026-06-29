import { writable, type Writable } from 'svelte/store';
import { watchOperations, type OperationRow } from './watch';
import { UnauthorizedError } from './api';

// Global op log driven by WatchOperations. The shell starts the
// subscription on mount and re-establishes on transient errors. UI
// surfaces (drawer, detail) subscribe to this store rather than each
// running their own stream — one connection per browser tab.

const MAX_KEEP = 200;

export const ops: Writable<OperationRow[]> = writable([]);
export const opsError = writable<string>('');

let stopped = false;
let controller: AbortController | null = null;

export function startOpsWatcher(): void {
  if (controller) return;
  stopped = false;
  void loop();
}

export function stopOpsWatcher(): void {
  stopped = true;
  controller?.abort();
  controller = null;
}

async function loop(): Promise<void> {
  // Reconnect with a small backoff. We intentionally keep watching even
  // through transient 5xx — the controller may be restarting. 401 is
  // terminal: the session is gone, and there's nothing useful to do
  // until the user re-logs.
  let backoffMs = 1000;
  while (!stopped) {
    controller = new AbortController();
    try {
      opsError.set('');
      // UI Phase U7: include children so the drawer's substep checklist
      // can render live progress for composite ops without a second
      // round-trip per parent.
      await watchOperations({ includeChildren: true }, applyEvent, {
        signal: controller.signal,
      });
      // Server closed the stream cleanly — usually means a deploy or
      // graceful shutdown. Try to reconnect after a brief pause.
      backoffMs = 1000;
    } catch (err) {
      if (stopped) return;
      if (err instanceof UnauthorizedError) {
        opsError.set('session expired');
        return;
      }
      // AbortError is normal (we triggered it).
      if (err instanceof DOMException && err.name === 'AbortError') return;
      opsError.set(err instanceof Error ? err.message : String(err));
      backoffMs = Math.min(backoffMs * 2, 15_000);
    }
    await sleep(backoffMs);
  }
}

function applyEvent(e: { operation: OperationRow }): void {
  const incoming = e.operation;
  ops.update((list) => {
    const idx = list.findIndex((o) => o.id === incoming.id);
    if (idx >= 0) {
      const next = list.slice();
      next[idx] = incoming;
      return next;
    }
    const next = [incoming, ...list];
    if (next.length > MAX_KEEP) next.length = MAX_KEEP;
    return next;
  });
}

function sleep(ms: number): Promise<void> {
  return new Promise((res) => setTimeout(res, ms));
}

export function clearOps(): void {
  ops.set([]);
}
