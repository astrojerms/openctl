import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from 'svelte/store';
import { overviewApi, UnauthorizedError, type OverviewResponse } from './api';
import { overview, startOverview, stopOverview } from './overview';

function snapshot(managedResources = 7): OverviewResponse {
  return {
    asOf: '2026-11-27T12:00:00Z', managedResources,
    kinds: managedResources ? [{ apiVersion: 'saved.io/v1', kind: 'Saved', resources: managedResources }] : [],
    pendingOperations: 2, runningOperations: 3, reviewResources: 1,
    activeOperations: [], reviewOperations: [],
  };
}

beforeEach(() => { vi.useFakeTimers(); stopOverview(); });
afterEach(() => { stopOverview(); vi.useRealTimers(); vi.restoreAllMocks(); });
const settle = () => vi.advanceTimersByTimeAsync(0);

describe('shell-owned overview polling', () => {
  it('cancels on stop and ignores a late successful response after logout', async () => {
    const pending = Promise.withResolvers<OverviewResponse>();
    const request = vi.spyOn(overviewApi, 'get').mockReturnValue(pending.promise);
    startOverview();
    const signal = request.mock.calls[0][1]?.signal;
    expect(signal?.aborted).toBe(false);
    stopOverview();
    expect(signal?.aborted).toBe(true);
    pending.resolve(snapshot());
    await settle();
    expect(get(overview)).toEqual({ snapshot: null, loading: false, error: '' });
    await vi.advanceTimersByTimeAsync(30_000);
    expect(request).toHaveBeenCalledTimes(1);
  });

  it('does not leak an old controller/session response or failure into a restarted session', async () => {
    const old = Promise.withResolvers<OverviewResponse>();
    const current = Promise.withResolvers<OverviewResponse>();
    const request = vi.spyOn(overviewApi, 'get').mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise);
    startOverview();
    stopOverview();
    startOverview();
    expect(get(overview).snapshot).toBeNull();
    current.resolve(snapshot(4));
    await settle();
    old.resolve(snapshot(99));
    await settle();
    expect(get(overview).snapshot?.managedResources).toBe(4);
    expect(get(overview).error).toBe('');
    expect(request.mock.calls[0][1]?.signal?.aborted).toBe(true);
    expect(request.mock.calls[1][1]?.signal?.aborted).toBe(false);

    stopOverview();
    const lateFailure = Promise.withResolvers<OverviewResponse>();
    request.mockReturnValueOnce(lateFailure.promise).mockResolvedValueOnce(snapshot(8));
    startOverview();
    stopOverview();
    startOverview();
    await settle();
    lateFailure.reject(new Error('old session unavailable'));
    await settle();
    expect(get(overview).snapshot?.managedResources).toBe(8);
    expect(get(overview).error).toBe('');
  });

  it('never overlaps slow polls and starts only one loop for repeated start calls', async () => {
    const first = Promise.withResolvers<OverviewResponse>();
    const second = Promise.withResolvers<OverviewResponse>();
    const request = vi.spyOn(overviewApi, 'get').mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    startOverview();
    startOverview();
    await vi.advanceTimersByTimeAsync(30_000);
    expect(request).toHaveBeenCalledTimes(1);
    first.resolve(snapshot());
    await settle();
    await vi.advanceTimersByTimeAsync(4999);
    expect(request).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(request).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(request).toHaveBeenCalledTimes(2);
    second.resolve(snapshot(3));
    await settle();
    expect(get(overview).snapshot?.managedResources).toBe(3);
  });

  it('keeps failures unavailable, not false zero, and preserves the last snapshot explicitly with an error', async () => {
    const request = vi.spyOn(overviewApi, 'get')
      .mockRejectedValueOnce(new Error('offline'))
      .mockResolvedValueOnce(snapshot())
      .mockRejectedValueOnce(new Error('refresh failed'))
      .mockResolvedValueOnce(snapshot(0));
    startOverview();
    await settle();
    expect(get(overview)).toEqual({ snapshot: null, loading: false, error: 'offline' });
    await vi.advanceTimersByTimeAsync(5000);
    expect(get(overview).snapshot?.managedResources).toBe(7);
    await vi.advanceTimersByTimeAsync(5000);
    expect(get(overview).snapshot?.managedResources).toBe(7);
    expect(get(overview).error).toBe('refresh failed');
    await vi.advanceTimersByTimeAsync(5000);
    expect(get(overview).snapshot?.managedResources).toBe(0);
    expect(get(overview).error).toBe('');
    expect(request).toHaveBeenCalledTimes(4);
  });

  it('rejects incomplete responses instead of turning omitted counts into zero', async () => {
    vi.spyOn(overviewApi, 'get').mockResolvedValue({ asOf: '2026-11-27T12:00:00Z' } as OverviewResponse);
    startOverview();
    await settle();
    expect(get(overview).snapshot).toBeNull();
    expect(get(overview).error).toContain('matching controller');
  });

  it('does not continue polling an expired session', async () => {
    const request = vi.spyOn(overviewApi, 'get').mockRejectedValue(new UnauthorizedError('expired'));
    startOverview();
    await settle();
    expect(get(overview).error).toBe('Session expired.');
    expect(get(overview).snapshot).toBeNull();
    await vi.advanceTimersByTimeAsync(30_000);
    expect(request).toHaveBeenCalledTimes(1);
  });
});
