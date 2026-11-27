import { afterEach, describe, expect, it, vi } from 'vitest';
import { get } from 'svelte/store';
import { parse } from 'yaml';
import { ApiError, UnauthorizedError, inventoryApi, resources, type GetResourceResponse, type GetSavedResourceResponse } from './api';
import { createDetailInspection, detailManifestYAML, type DetailInspection } from './detailInspection';

const target = { apiVersion: 'saved.io/v1', kind: 'Saved', name: 'one' };
const saved: GetSavedResourceResponse = {
  resource: { apiVersion: target.apiVersion, kind: target.kind, metadata: { name: target.name, annotations: { note: 'keep exact' } }, spec: { password: '${secret:media/password}', retry: { requestId: 'unresolved-request-marker' } } },
  appliedAt: '2026-11-27T12:00:00Z', source: 'ui',
};
const observed: GetResourceResponse = {
  resource: { ...saved.resource, status: { ready: true, phase: 'ready' }, children: [{ ...target, name: 'child' }], drift: [] },
  applied: saved.resource, appliedAt: saved.appliedAt,
};
const inspections: DetailInspection[] = [];
function create() {
  const unauthorized = vi.fn();
  const inspection = createDetailInspection(unauthorized);
  inspections.push(inspection);
  return { inspection, unauthorized };
}
async function settle() {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}
afterEach(() => { for (const inspection of inspections.splice(0)) inspection.stop(); vi.restoreAllMocks(); });

describe('independent saved intent and strict observation', () => {
  it.each([
    [new ApiError(503, 'provider unavailable'), 'unavailable'],
    [new ApiError(404, 'provider object disappeared'), 'not-found'],
  ] as const)('keeps raw saved intent inspectable when live Get fails with %s', async (failure, observation) => {
    const live = Promise.withResolvers<GetResourceResponse>();
    vi.spyOn(resources, 'get').mockReturnValue(live.promise);
    vi.spyOn(inventoryApi, 'get').mockResolvedValue(saved);
    const { inspection } = create();
    inspection.open(target);
    await settle();
    expect(get(inspection).saved).toEqual(saved);
    expect(get(inspection).loading).toBe(true);
    expect(get(inspection).data).toBeNull();
    expect(inspection.canAct()).toBe(false);
    live.reject(failure);
    await settle();
    expect(get(inspection)).toMatchObject({ saved, data: null, observation, loading: false });
    expect(inspection.canAct()).toBe(false);
    if (observation === 'not-found') expect(get(inspection).error).toContain('Provider/Get reported not found');
    expect(get(inspection).saved?.resource.status).toBeUndefined();
  });

  it('revokes action authority immediately on refresh and keeps old healthy data absent after failure', async () => {
    const retry = Promise.withResolvers<GetResourceResponse>();
    vi.spyOn(resources, 'get').mockResolvedValueOnce(observed).mockReturnValueOnce(retry.promise).mockResolvedValueOnce(observed);
    vi.spyOn(inventoryApi, 'get').mockResolvedValue(saved);
    const { inspection } = create();
    inspection.open(target);
    await settle();
    const previous = inspection.capture();
    expect(inspection.canAct()).toBe(true);
    expect(inspection.current(previous, true)).toBe(true);
    const refreshing = inspection.refresh();
    expect(inspection.canAct()).toBe(false);
    expect(inspection.current(previous, true)).toBe(false);
    expect(get(inspection).data).toBeNull();
    retry.reject(new Error('provider went offline'));
    await refreshing;
    expect(get(inspection)).toMatchObject({ data: null, saved, observation: 'unavailable' });
    expect(inspection.canAct()).toBe(false);
    await inspection.refresh();
    expect(inspection.canAct()).toBe(true);
    expect(inspection.current(previous, true)).toBe(false);
  });

  it('serializes watch refreshes and does not publish an observation superseded by a newer event', async () => {
    const first = Promise.withResolvers<GetResourceResponse>();
    const newest = Promise.withResolvers<GetResourceResponse>();
    const request = vi.spyOn(resources, 'get').mockReturnValueOnce(first.promise).mockReturnValueOnce(newest.promise);
    vi.spyOn(inventoryApi, 'get').mockResolvedValue(saved);
    const { inspection } = create();
    inspection.open(target);
    const finished = inspection.refresh();
    void inspection.refresh();
    expect(request).toHaveBeenCalledTimes(1);
    first.resolve(observed);
    await settle();
    expect(request).toHaveBeenCalledTimes(2);
    expect(get(inspection).data).toBeNull();
    expect(inspection.canAct()).toBe(false);
    newest.resolve({ resource: { ...saved.resource, status: { ready: false } } });
    await finished;
    expect(get(inspection).data?.resource.status?.ready).toBe(false);
  });

  it('invalidates pending observations after watch loss instead of reintroducing old green state', async () => {
    const live = Promise.withResolvers<GetResourceResponse>();
    vi.spyOn(resources, 'get').mockReturnValue(live.promise);
    vi.spyOn(inventoryApi, 'get').mockResolvedValue(saved);
    const { inspection } = create();
    inspection.open(target);
    inspection.invalidate(new Error('watch disconnected'));
    live.resolve(observed);
    await settle();
    expect(get(inspection)).toMatchObject({ data: null, observation: 'unavailable', saved });
    expect(inspection.canAct()).toBe(false);
  });

  it('aborts a previous route and ignores its late saved/live/unauthorized completions', async () => {
    const oldSaved = Promise.withResolvers<GetSavedResourceResponse>();
    const oldLive = Promise.withResolvers<GetResourceResponse>();
    const live = vi.spyOn(resources, 'get').mockReturnValueOnce(oldLive.promise).mockResolvedValueOnce(observed);
    const inventory = vi.spyOn(inventoryApi, 'get').mockReturnValueOnce(oldSaved.promise).mockResolvedValueOnce(saved);
    const { inspection, unauthorized } = create();
    inspection.open({ ...target, name: 'old' });
    const oldLease = inspection.capture();
    inspection.open(target);
    await settle();
    expect(live.mock.calls[0][3]?.signal?.aborted).toBe(true);
    expect(inventory.mock.calls[0][3]?.signal?.aborted).toBe(true);
    expect(inspection.current(oldLease)).toBe(false);
    oldSaved.resolve({ ...saved, source: 'wrong old route' });
    oldLive.reject(new UnauthorizedError('old session'));
    await settle();
    expect(get(inspection)).toMatchObject({ saved, data: observed, observation: 'fresh' });
    expect(unauthorized).not.toHaveBeenCalled();
  });

  it.each(['live', 'saved'] as const)('clears all session-bound evidence and aborts inspection on a %s 401', async (path) => {
    const live = Promise.withResolvers<GetResourceResponse>();
    const stored = Promise.withResolvers<GetSavedResourceResponse>();
    const liveRequest = vi.spyOn(resources, 'get').mockReturnValue(live.promise);
    vi.spyOn(inventoryApi, 'get').mockReturnValue(stored.promise);
    const { inspection, unauthorized } = create();
    inspection.open(target);
    if (path === 'live') { stored.resolve(saved); await settle(); live.reject(new UnauthorizedError('expired')); }
    else { live.resolve(observed); await settle(); stored.reject(new UnauthorizedError('expired')); }
    await settle();
    expect(get(inspection)).toMatchObject({ saved: null, data: null, observation: 'unauthorized', loading: false, savedLoading: false });
    expect(liveRequest.mock.calls[0][3]?.signal?.aborted).toBe(true);
    expect(inspection.capture()).toBeNull();
    expect(inspection.canAct()).toBe(false);
    expect(unauthorized).toHaveBeenCalledTimes(1);
  });

  it('keeps saved-store failure unavailable rather than inventing an empty declaration', async () => {
    vi.spyOn(resources, 'get').mockRejectedValue(new ApiError(404, 'not found'));
    vi.spyOn(inventoryApi, 'get').mockRejectedValue(new ApiError(503, 'store unavailable'));
    const { inspection } = create();
    inspection.open(target);
    await settle();
    expect(get(inspection).saved).toBeNull();
    expect(get(inspection).savedError).toContain('Saved intent unavailable');
    expect(detailManifestYAML(get(inspection).saved, get(inspection).data)).toBe('');
  });

  it('does not publish requests completing after teardown', async () => {
    const pending = Promise.withResolvers<GetSavedResourceResponse>();
    vi.spyOn(inventoryApi, 'get').mockReturnValue(pending.promise);
    vi.spyOn(resources, 'get').mockResolvedValue(observed);
    const { inspection } = create();
    inspection.open(target);
    inspection.stop();
    pending.resolve(saved);
    await settle();
    expect(get(inspection).data).toBeNull();
    expect(get(inspection).saved).toBeNull();
    expect(inspection.canAct()).toBe(false);
  });

  it('removes a previously saved declaration after authoritative deletion instead of exporting old intent', async () => {
    vi.spyOn(inventoryApi, 'get').mockResolvedValueOnce(saved).mockRejectedValueOnce(new ApiError(404, 'saved declaration removed'));
    vi.spyOn(resources, 'get').mockResolvedValueOnce(observed).mockRejectedValueOnce(new ApiError(404, 'instance removed'));
    const { inspection } = create();
    inspection.open(target);
    await settle();
    expect(get(inspection).saved).toEqual(saved);
    await inspection.refresh();
    await settle();
    const state = get(inspection);
    expect(state.saved).toBeNull();
    expect(state.savedLoading).toBe(false);
    expect(detailManifestYAML(state.saved, state.data)).toBe('');
    expect(state.observation).toBe('not-found');
  });

  it('coalesces saved refreshes and never republishes intent superseded by a newer resource event', async () => {
    const old = Promise.withResolvers<GetSavedResourceResponse>();
    const current = Promise.withResolvers<GetSavedResourceResponse>();
    const read = vi.spyOn(inventoryApi, 'get').mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise);
    vi.spyOn(resources, 'get').mockResolvedValue(observed);
    const { inspection } = create();
    inspection.open(target);
    await settle();
    void inspection.refresh();
    void inspection.refresh();
    expect(read).toHaveBeenCalledTimes(1);
    old.resolve(saved);
    await settle();
    expect(get(inspection).saved).toBeNull();
    const updated = { ...saved, source: 'gitops', resource: { ...saved.resource, spec: { generation: 'new' } } };
    current.resolve(updated);
    await settle();
    expect(get(inspection).saved).toEqual(updated);
    expect(get(inspection).savedLoading).toBe(false);
    expect(read).toHaveBeenCalledTimes(2);
  });

  it('keeps intent reads independent of observation invalidation', async () => {
    const pending = Promise.withResolvers<GetSavedResourceResponse>();
    vi.spyOn(inventoryApi, 'get').mockReturnValue(pending.promise);
    vi.spyOn(resources, 'get').mockResolvedValue(observed);
    const { inspection } = create();
    inspection.open(target);
    await settle();
    inspection.invalidate(new Error('observation stream disconnected'));
    pending.resolve(saved);
    await settle();
    expect(get(inspection).saved).toEqual(saved);
    expect(get(inspection).savedLoading).toBe(false);
    expect(get(inspection).data).toBeNull();
    expect(inspection.canAct()).toBe(false);
  });

  it('exports exact desired markers and metadata without observed status, drift, children or ownership', () => {
    const intent = { ...saved, resource: { ...saved.resource, metadata: { ...saved.resource.metadata, ownerRefs: [{ ...target }] }, status: { ready: true }, drift: [{ path: 'spec', desired: 'a', observed: 'b' }], children: [{ ...target }] } };
    const before = structuredClone(intent);
    const yaml = detailManifestYAML(intent, observed);
    expect(parse(yaml)).toEqual(saved.resource);
    expect(yaml).toContain('${secret:media/password}');
    expect(yaml).toContain('unresolved-request-marker');
    expect(intent).toEqual(before);
  });
});
