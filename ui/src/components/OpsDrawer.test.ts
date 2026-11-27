import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushSync, mount, tick, unmount } from 'svelte';
import { get } from 'svelte/store';
import OpsDrawer from './OpsDrawer.svelte';
import { operations, overviewApi, type Operation, type OverviewResponse } from '../lib/api';
import { auth } from '../lib/auth';
import { recordAuthority, resetAuthority } from '../lib/authority';
import { ops, opsError } from '../lib/ops';
import { clearOpsDrawerFocus, focusOpsDrawer, opsDrawerFocus } from '../lib/opsDrawer';
import { startOverview, stopOverview } from '../lib/overview';

const historical: Operation = {
  id: 'older-than-the-live-tail', type: 'apply', status: 'failed',
  apiVersion: 'saved.io/v1', kind: 'Saved', resourceName: 'historical-resource',
  submittedAt: '2025-01-01T00:00:00Z', error: 'retained failure detail',
};
let component: Record<string, unknown> | undefined;
let target: HTMLDivElement;

beforeEach(() => {
  stopOverview();
  target = document.createElement('div');
  document.body.append(target);
  auth.set({ kind: 'signed-in', me: { role: 'viewer', userId: 'reader', sessionId: 'session' } });
  recordAuthority('mirror');
  ops.set([]);
  opsError.set('');
  clearOpsDrawerFocus();
});
afterEach(async () => {
  if (component) await unmount(component);
  component = undefined;
  target.remove();
  ops.set([]);
  stopOverview();
  clearOpsDrawerFocus();
  resetAuthority();
  auth.set({ kind: 'unknown' });
  vi.useRealTimers();
  vi.restoreAllMocks();
});

async function renderUpdates() {
  await Promise.resolve();
  await tick();
}

describe('explicit operation inspection', () => {
  it('uses exact fresh root totals, not cached child rows, and labels stale or unavailable totals', async () => {
    vi.useFakeTimers();
    const pending = Promise.withResolvers<OverviewResponse>();
    const snapshot: OverviewResponse = {
      asOf: '2026-11-27T12:00:00Z', managedResources: 0, kinds: [],
      pendingOperations: 2, runningOperations: 3, reviewResources: 0,
      activeOperations: [], reviewOperations: [],
    };
    vi.spyOn(overviewApi, 'get')
      .mockReturnValueOnce(pending.promise)
      .mockRejectedValueOnce(new Error('refresh failed'))
      .mockRejectedValueOnce(new Error('controller offline'))
      .mockResolvedValueOnce({ ...snapshot, pendingOperations: 0, runningOperations: 0 });
    ops.set(Array.from({ length: 200 }, (_, i) => ({
      id: `child-${i}`, parentId: 'root-request', type: 'apply', status: 'running',
    })));
    component = mount(OpsDrawer, { target });
    startOverview();
    await tick();
    expect(target.querySelector('.handle')?.textContent).toContain('root totals unavailable');
    expect(target.querySelector('.inflight')).toBeNull();
    pending.resolve(snapshot);
    await vi.advanceTimersByTimeAsync(0);
    await tick();
    expect(target.querySelector('.inflight')?.textContent).toBe('5 root requests in flight');
    await vi.advanceTimersByTimeAsync(5000);
    await tick();
    expect(target.querySelector('.handle')?.textContent).toContain('root totals stale');
    expect(target.querySelector('.inflight')).toBeNull();
    stopOverview();
    startOverview();
    await vi.advanceTimersByTimeAsync(0);
    await tick();
    expect(target.querySelector('.handle')?.textContent).toContain('root totals unavailable');
    expect(target.querySelector('.inflight')).toBeNull();
    await vi.advanceTimersByTimeAsync(5000);
    await tick();
    expect(target.querySelector('.inflight')?.textContent).toBe('0 root requests in flight');
  });

  it('fetches a focused historical operation beyond the 200-row live tail, including when the tail is empty', async () => {
    const pending = Promise.withResolvers<Operation>();
    const request = vi.spyOn(operations, 'get').mockReturnValue(pending.promise);
    const cancel = vi.spyOn(operations, 'cancel');
    ops.set(Array.from({ length: 200 }, (_, i) => ({ id: `recent-${i}`, type: 'apply', status: 'succeeded' })));
    component = mount(OpsDrawer, { target });
    flushSync(() => focusOpsDrawer({ opId: historical.id }));
    expect(request).toHaveBeenCalledExactlyOnceWith(historical.id, true, { signal: expect.any(AbortSignal) });
    expect(target.textContent).toContain('Loading focused operation');
    pending.resolve(historical);
    await renderUpdates();
    expect(target.querySelector('tr.focused')?.textContent).toContain('historical-resource');
    expect(target.textContent).toContain('retained failure detail');
    expect(target.querySelector('.row-btn.retry')).toBeNull();
    expect(target.querySelector('.row-btn.cancel')).toBeNull();
    flushSync(() => ops.set([]));
    expect(target.querySelector('tr.focused')?.textContent).toContain('historical-resource');
    expect(cancel).not.toHaveBeenCalled();
  });

  it('can close and reopen the same focus and never fetches an already-known operation', async () => {
    const request = vi.spyOn(operations, 'get').mockResolvedValue(historical);
    component = mount(OpsDrawer, { target });
    flushSync(() => focusOpsDrawer({ opId: historical.id }));
    await renderUpdates();
    const handle = target.querySelector<HTMLButtonElement>('button.handle')!;
    flushSync(() => handle.click());
    expect(target.querySelector('aside.open')).toBeNull();
    expect(get(opsDrawerFocus)).toBeNull();
    flushSync(() => focusOpsDrawer({ opId: historical.id }));
    await renderUpdates();
    expect(target.querySelector('aside.open')).not.toBeNull();
    expect(target.querySelector('tr.focused')?.textContent).toContain('historical-resource');
    expect(request).toHaveBeenCalledTimes(2);

    flushSync(() => {
      ops.set([{ ...historical, id: 'already-live', resourceName: 'live-resource' }]);
      focusOpsDrawer({ opId: 'already-live' });
    });
    expect(target.querySelector('tr.focused')?.textContent).toContain('live-resource');
    expect(request).toHaveBeenCalledTimes(2);
  });

  it('aborts superseded focus and ignores old responses after focus changes or closes', async () => {
    const old = Promise.withResolvers<Operation>();
    const current = Promise.withResolvers<Operation>();
    const closed = Promise.withResolvers<Operation>();
    const request = vi.spyOn(operations, 'get').mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise).mockReturnValueOnce(closed.promise);
    component = mount(OpsDrawer, { target });
    flushSync(() => focusOpsDrawer({ opId: historical.id }));
    const oldSignal = request.mock.calls[0][2]?.signal;
    flushSync(() => focusOpsDrawer({ opId: 'new-focus' }));
    expect(oldSignal?.aborted).toBe(true);
    current.resolve({ ...historical, id: 'new-focus', resourceName: 'new-resource' });
    await renderUpdates();
    old.resolve(historical);
    await renderUpdates();
    expect(target.querySelector('tr.focused')?.textContent).toContain('new-resource');
    expect(target.textContent).not.toContain('historical-resource');

    flushSync(() => focusOpsDrawer({ opId: 'close-while-loading' }));
    const closingSignal = request.mock.calls[2][2]?.signal;
    flushSync(() => target.querySelector<HTMLButtonElement>('button.handle')!.click());
    expect(closingSignal?.aborted).toBe(true);
    closed.resolve({ ...historical, id: 'close-while-loading' });
    await renderUpdates();
    expect(target.querySelector('aside.open')).toBeNull();
    expect(target.textContent).not.toContain('historical-resource');
  });

  it('shows a retained-history read failure without automatically retrying or applying', async () => {
    const request = vi.spyOn(operations, 'get').mockRejectedValue(new Error('operation expired from retention'));
    component = mount(OpsDrawer, { target });
    flushSync(() => focusOpsDrawer({ opId: historical.id }));
    await renderUpdates();
    expect(target.querySelector('[role="alert"]')?.textContent).toContain('operation expired from retention');
    flushSync(() => ops.set([{ id: 'unrelated', type: 'apply', status: 'running' }]));
    expect(request).toHaveBeenCalledTimes(1);
  });
});
