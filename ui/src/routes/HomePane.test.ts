import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { mount, tick, unmount } from 'svelte';
import HomePane from './HomePane.svelte';
import { overviewApi, resources, type OverviewResponse } from '../lib/api';
import { startOverview, stopOverview } from '../lib/overview';
import { clearOpsDrawerFocus } from '../lib/opsDrawer';
import { stopCatalogue } from '../lib/catalogue';

const snapshot: OverviewResponse = {
  asOf: '2026-11-27T12:00:00Z', managedResources: 12,
  kinds: [{ apiVersion: 'disabled.io/v1', kind: 'SavedOnly', resources: 12 }],
  pendingOperations: 4, runningOperations: 5, reviewResources: 3,
  activeOperations: [{ id: 'active-request', type: 'apply', status: 'running', kind: 'SavedOnly', resourceName: 'one' }],
  reviewOperations: [{ id: 'old-review-request', type: 'delete', status: 'failed', kind: 'SavedOnly', resourceName: 'two' }],
};
let component: Record<string, unknown> | undefined;
let target: HTMLDivElement;

beforeEach(() => {
  vi.useFakeTimers();
  stopOverview();
  stopCatalogue();
  clearOpsDrawerFocus();
  target = document.createElement('div');
  document.body.append(target);
});
afterEach(async () => {
  if (component) await unmount(component);
  component = undefined;
  target.remove();
  stopOverview();
  stopCatalogue();
  clearOpsDrawerFocus();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe('read-only persisted overview', () => {
  it('shows exact totals, bounded lists, snapshot time, unknown-provider links, and explicit inspection', async () => {
    vi.spyOn(overviewApi, 'get').mockResolvedValue(snapshot);
    const liveList = vi.spyOn(resources, 'list');
    const apply = vi.spyOn(resources, 'apply');
    component = mount(HomePane, { target, props: { viewer: true } });
    startOverview();
    await vi.advanceTimersByTimeAsync(0);
    await tick();
    expect([...target.querySelectorAll('.metric')].map((node) => node.textContent)).toEqual(['12', '4', '5', '3']);
    expect(target.textContent).toContain('Showing 1 of 9');
    expect(target.textContent).toContain('Showing 1 of 3');
    expect(liveList).not.toHaveBeenCalled();
    expect(apply).not.toHaveBeenCalled();
  });

  it('never renders missing totals as zero and labels a retained snapshot stale after refresh failure', async () => {
    const pending = Promise.withResolvers<OverviewResponse>();
    vi.spyOn(overviewApi, 'get').mockReturnValueOnce(pending.promise).mockRejectedValue(new Error('controller offline'));
    component = mount(HomePane, { target });
    startOverview();
    await tick();
    expect(target.textContent).toContain('Counts are unknown');
    expect(target.querySelector('.metric')).toBeNull();
    pending.resolve(snapshot);
    await vi.advanceTimersByTimeAsync(0);
    await tick();
    await vi.advanceTimersByTimeAsync(5000);
    await tick();
    expect(target.querySelector('[role="alert"]')?.textContent).toContain('Stale snapshot — refresh failed');
    expect(target.querySelector('.metric')?.textContent).toBe('12');
    expect(target.querySelector('.snapshot-time')?.textContent).toContain('Last successful snapshot');

    stopOverview();
    startOverview();
    await vi.advanceTimersByTimeAsync(0);
    await tick();
    expect(target.querySelector('[role="alert"]')?.textContent).toContain('Overview unavailable — counts are unknown');
    expect(target.querySelector('.metric')).toBeNull();
  });
});
