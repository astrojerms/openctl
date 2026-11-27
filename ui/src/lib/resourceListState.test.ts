import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from 'svelte/store';
import {
  inventoryApi, resources, UnauthorizedError,
  type ListResourcesResponse, type ListSavedResourcesResponse,
  type Resource, type SavedResourceMetadata,
} from './api';
import {
  createResourceListState, resourceRowDrift, resourceRowStatus,
  type ResourceListController,
} from './resourceListState';
import * as watch from './watch';

const apiVersion = 'example.io/v1';
const kind = 'Machine';
const saved = (name: string, av = apiVersion, k = kind): SavedResourceMetadata => ({
  apiVersion: av, kind: k, metadata: { name, labels: { environment: 'homelab' } },
  source: 'ui', appliedAt: '2026-11-27T12:00:00Z',
});
const observed = (name: string, av = apiVersion, k = kind): Resource => ({
  apiVersion: av, kind: k, metadata: { name }, status: { state: 'running' }, drift: [],
});
let list: ResourceListController;
let streams: { apiVersion: string; kind: string; callback: (event: watch.WatchEvent) => void; signal?: AbortSignal }[];

beforeEach(() => {
  vi.useFakeTimers();
  list = createResourceListState();
  streams = [];
  vi.spyOn(watch, 'watchResources').mockImplementation((av, k, _name, callback, opts) => {
    streams.push({ apiVersion: av, kind: k, callback, signal: opts?.signal });
    return Promise.withResolvers<void>().promise;
  });
});
afterEach(() => {
  list.stop();
  vi.useRealTimers();
  vi.restoreAllMocks();
});
const settle = () => vi.advanceTimersByTimeAsync(0);

describe('selected-kind saved inventory', () => {
  it('publishes saved metadata independently of a pending or failed initial observation', async () => {
    const pending = Promise.withResolvers<ListResourcesResponse>();
    const metadata = saved('desired');
    vi.spyOn(inventoryApi, 'list').mockResolvedValue({ resources: [metadata] });
    vi.spyOn(resources, 'list').mockReturnValue(pending.promise);
    list.select(apiVersion, kind, false);
    await settle();
    expect(get(list).rows[0]).toMatchObject({ name: 'desired', saved: metadata, observation: 'pending' });
    expect(get(list).rows[0].observed).toBeUndefined();

    pending.reject(new Error('provider offline'));
    await settle();
    const row = get(list).rows[0];
    expect(row.saved).toBe(metadata);
    expect(row.observed).toBeUndefined();
    expect(row.observation).toBe('unavailable');
    expect(resourceRowStatus(row).tone).toBe('unknown');
    expect(resourceRowDrift(row)).toBeNull();
    expect(get(list).observationError).toBeTruthy();
  });

  it('keeps omitted desired names unknown and preserves them across watch deletion', async () => {
    vi.spyOn(inventoryApi, 'list').mockResolvedValue({ resources: [saved('omitted'), saved('returned')] });
    vi.spyOn(resources, 'list').mockResolvedValue({ resources: [observed('returned'), observed('unsaved')] });
    list.select(apiVersion, kind, false);
    await settle();
    const omitted = get(list).rows.find((row) => row.name === 'omitted')!;
    expect(omitted.observation).toBe('not-returned');
    expect(omitted.observed).toBeUndefined();
    expect(resourceRowStatus(omitted).tone).toBe('unknown');
    expect(resourceRowDrift(omitted)).toBeNull();

    streams[0].callback({ type: 'DELETED', resource: observed('returned') });
    streams[0].callback({ type: 'DELETED', resource: observed('unsaved') });
    expect(get(list).rows.map((row) => row.name)).toEqual(['omitted', 'returned']);
    const deleted = get(list).rows.find((row) => row.name === 'returned')!;
    expect(deleted.saved).toBeDefined();
    expect(deleted.observed).toBeUndefined();
    expect(deleted.observation).toBe('not-returned');
    expect(resourceRowDrift(deleted)).toBeNull();
  });

  it('marks retained observations stale on stream failure and only freshens names actually observed again', async () => {
    const stream = Promise.withResolvers<void>();
    vi.mocked(watch.watchResources).mockReturnValueOnce(stream.promise);
    vi.spyOn(inventoryApi, 'list').mockResolvedValue({ resources: [saved('one'), saved('two')] });
    vi.spyOn(resources, 'list').mockResolvedValueOnce({ resources: [observed('one'), observed('two')] })
      .mockRejectedValue(new Error('provider unavailable'));
    list.select(apiVersion, kind, false);
    await settle();
    expect(resourceRowStatus(get(list).rows[0]).tone).toBe('good');
    stream.reject(new Error('stream offline'));
    await settle();
    for (const row of get(list).rows) {
      expect(row.observation).toBe('stale');
      expect(resourceRowStatus(row).tone).toBe('unknown');
      expect(resourceRowDrift(row)).toBeNull();
    }
    await vi.advanceTimersByTimeAsync(1000);
    streams[0].callback({ type: 'MODIFIED', resource: observed('one') });
    expect(get(list).rows.find((row) => row.name === 'one')?.observation).toBe('fresh');
    expect(get(list).rows.find((row) => row.name === 'two')?.observation).toBe('stale');
  });

  it('refreshes metadata without watch-event fan-out or overlap, retaining failed snapshots until authoritative removal', async () => {
    const slow = Promise.withResolvers<ListSavedResourcesResponse>();
    const metadata = vi.spyOn(inventoryApi, 'list').mockResolvedValueOnce({ resources: [saved('desired')] })
      .mockRejectedValueOnce(new Error('manifest store offline')).mockReturnValueOnce(slow.promise)
      .mockResolvedValue({ resources: [] });
    vi.spyOn(resources, 'list').mockResolvedValue({ resources: [] });
    list.select(apiVersion, kind, false);
    await settle();
    for (let index = 0; index < 20; index++) {
      streams[0].callback({ type: 'DELETED', resource: observed('desired') });
    }
    expect(metadata).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(5000);
    expect(get(list).savedError).toBeTruthy();
    expect(get(list).rows.map((row) => row.name)).toEqual(['desired']);
    await vi.advanceTimersByTimeAsync(5000);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(metadata).toHaveBeenCalledTimes(3);
    expect(get(list).rows.map((row) => row.name)).toEqual(['desired']);
    slow.resolve({ resources: [saved('applied-later')] });
    await settle();
    expect(get(list).rows.map((row) => row.name)).toEqual(['applied-later']);
    expect(get(list).savedError).toBe('');
    await vi.advanceTimersByTimeAsync(5000);
    expect(get(list).rows).toEqual([]);
  });

  it('keeps discovery-only kinds observation-only, with live deletion semantics and no metadata polling', async () => {
    const metadata = vi.spyOn(inventoryApi, 'list');
    vi.spyOn(resources, 'list').mockResolvedValue({ resources: [observed('discovered')] });
    list.select(apiVersion, kind, true);
    await settle();
    expect(get(list).rows[0].saved).toBeUndefined();
    expect(get(list).rows[0].observed).toBeDefined();
    streams[0].callback({ type: 'DELETED', resource: observed('discovered') });
    expect(get(list).rows).toEqual([]);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(metadata).not.toHaveBeenCalled();
  });

  it.each(['success', 'failure'] as const)('ignores old initial %s even after revisiting the same route', async (outcome) => {
    const oldSaved = Promise.withResolvers<ListSavedResourcesResponse>();
    const oldLive = Promise.withResolvers<ListResourcesResponse>();
    const metadata = vi.spyOn(inventoryApi, 'list').mockReturnValueOnce(oldSaved.promise)
      .mockResolvedValue({ resources: [saved('current')] });
    const live = vi.spyOn(resources, 'list').mockReturnValueOnce(oldLive.promise)
      .mockResolvedValue({ resources: [observed('current')] });
    list.select(apiVersion, kind, false);
    list.select('other.io/v1', 'Other', false);
    list.select(apiVersion, kind, false);
    await settle();
    if (outcome === 'success') {
      oldSaved.resolve({ resources: [saved('old')] });
      oldLive.resolve({ resources: [observed('old')] });
    } else {
      oldSaved.reject(new Error('old manifest store failure'));
      oldLive.reject(new UnauthorizedError('old session'));
    }
    await settle();
    expect(get(list).rows.map((row) => row.name)).toEqual(['current']);
    expect(get(list).savedError).toBe('');
    expect(get(list).observationError).toBe('');
    expect(metadata.mock.calls[0][2]?.signal?.aborted).toBe(true);
    expect(live.mock.calls[0][2]?.signal?.aborted).toBe(true);
    expect(metadata.mock.calls[2][2]?.signal?.aborted).toBe(false);
    expect(streams).toHaveLength(1);
    expect(streams[0]).toMatchObject({ apiVersion, kind });
  });

  it('rejects late watch events, foreign-kind observations and stopped-session metadata', async () => {
    const oldRefresh = Promise.withResolvers<ListSavedResourcesResponse>();
    const metadata = vi.spyOn(inventoryApi, 'list').mockResolvedValueOnce({ resources: [saved('old')] })
      .mockReturnValueOnce(oldRefresh.promise).mockResolvedValue({ resources: [saved('current', 'next.io/v1', 'Next')] });
    vi.spyOn(resources, 'list').mockResolvedValueOnce({ resources: [observed('old')] })
      .mockResolvedValue({ resources: [observed('current', 'next.io/v1', 'Next'), observed('foreign')] });
    list.select(apiVersion, kind, false);
    await settle();
    await vi.advanceTimersByTimeAsync(5000);
    list.stop();
    list.select('next.io/v1', 'Next', false);
    await settle();
    expect(streams[0].signal?.aborted).toBe(true);
    streams[0].callback({ type: 'ADDED', resource: observed('late') });
    streams[1].callback({ type: 'ADDED', resource: observed('foreign') });
    oldRefresh.resolve({ resources: [saved('old-refresh')] });
    await settle();
    expect(get(list).rows.map((row) => row.name)).toEqual(['current']);
    expect(metadata.mock.calls.map(([av, k]) => `${av}/${k}`)).toEqual([
      `${apiVersion}/${kind}`, `${apiVersion}/${kind}`, 'next.io/v1/Next',
    ]);
  });

  it('clears already-visible saved intent when a later read revokes the session', async () => {
    vi.spyOn(inventoryApi, 'list').mockResolvedValue({ resources: [saved('private-name')] });
    const live = Promise.withResolvers<ListResourcesResponse>();
    vi.spyOn(resources, 'list').mockReturnValue(live.promise);
    list.select(apiVersion, kind, false);
    await settle();
    expect(get(list).rows[0].name).toBe('private-name');
    live.reject(new UnauthorizedError('expired'));
    await settle();
    expect(get(list).rows).toEqual([]);
    expect(get(list).savedKnown).toBe(false);
    expect(get(list).savedError).toBeTruthy();
  });

  it('does not invent an empty saved inventory or continue a revoked session', async () => {
    const metadata = vi.spyOn(inventoryApi, 'list').mockRejectedValue(new UnauthorizedError('expired'));
    const live = vi.spyOn(resources, 'list').mockReturnValue(Promise.withResolvers<ListResourcesResponse>().promise);
    list.select(apiVersion, kind, false);
    await settle();
    expect(get(list).savedKnown).toBe(false);
    expect(get(list).savedError).toBeTruthy();
    expect(live.mock.calls[0][2]?.signal?.aborted).toBe(true);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(metadata).toHaveBeenCalledTimes(1);
    expect(watch.watchResources).not.toHaveBeenCalled();
  });
});
