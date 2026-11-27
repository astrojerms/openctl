import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from 'svelte/store';
import { overviewApi, resources, schemas, type ListSchemasResponse, type OverviewResponse, type SchemaInfo } from './api';
import { advancedKind, catalogue, catalogueError, refreshCatalogue, stopCatalogue } from './catalogue';
import { startOverview, stopOverview } from './overview';

const known: SchemaInfo = { apiVersion: 'vm.io/v1', kind: 'VirtualMachine', provider: 'VM provider', fileName: 'vm.cue' };
const observed: SchemaInfo = { apiVersion: 'vm.io/v1', kind: 'ObservedVM', provider: 'VM provider', fileName: 'observed.cue', observedOnly: true };
const snapshot: OverviewResponse = {
  asOf: '2026-11-27T12:00:00Z', managedResources: 6,
  kinds: [{ apiVersion: known.apiVersion, kind: known.kind, resources: 4 }, { apiVersion: 'disabled.io/v1', kind: 'SavedOnly', resources: 2 }],
  pendingOperations: 0, runningOperations: 0, reviewResources: 0, activeOperations: [], reviewOperations: [],
};

beforeEach(() => { vi.useFakeTimers(); stopOverview(); stopCatalogue(); });
afterEach(() => { stopOverview(); stopCatalogue(); vi.useRealTimers(); vi.restoreAllMocks(); });

describe('persisted navigation inventory', () => {
  it('merges unknown-provider inventory and never scans resources for badges', async () => {
    vi.spyOn(schemas, 'list').mockResolvedValue({ schemas: [known, observed, { ...known, kind: 'Empty' }] });
    vi.spyOn(overviewApi, 'get').mockResolvedValue(snapshot);
    const list = vi.spyOn(resources, 'list');
    const liveGet = vi.spyOn(resources, 'get');
    startOverview();
    await refreshCatalogue();
    await vi.advanceTimersByTimeAsync(0);
    const entries = get(catalogue).flat;
    expect(entries.find((entry) => entry.kind === known.kind)?.count).toBe(4);
    expect(entries.find((entry) => entry.kind === 'Empty')?.count).toBe(0);
    expect(entries.find((entry) => entry.kind === observed.kind)?.count).toBeNull();
    expect(entries.find((entry) => entry.kind === 'SavedOnly')).toMatchObject({
      schemaAvailable: false, count: 2,
    });
    await vi.advanceTimersByTimeAsync(15_000);
    expect(list).not.toHaveBeenCalled();
    expect(liveGet).not.toHaveBeenCalled();
  });

  it('keeps kind navigation but makes counts unknown when a snapshot fails or schemas are unavailable', async () => {
    vi.spyOn(schemas, 'list').mockResolvedValueOnce({ schemas: [known, observed] }).mockRejectedValue(new Error('schema service unavailable'));
    vi.spyOn(overviewApi, 'get').mockResolvedValueOnce(snapshot).mockRejectedValue(new Error('overview unavailable'));
    startOverview();
    await refreshCatalogue();
    await vi.advanceTimersByTimeAsync(0);
    expect(get(catalogue).flat.find((entry) => entry.kind === known.kind)?.count).toBe(4);
    await vi.advanceTimersByTimeAsync(5000);
    expect(get(catalogue).flat.map((entry) => entry.count)).toEqual([null, null, null]);
    await refreshCatalogue();
    expect(get(catalogueError)).toBe('schema service unavailable');
    expect(get(catalogue).flat.map((entry) => entry.kind)).toContain('SavedOnly');
    expect(get(catalogue).flat.map((entry) => entry.kind)).toContain(known.kind);

    stopCatalogue();
    await refreshCatalogue();
    expect(get(catalogue).flat).toHaveLength(2);
    expect(get(catalogue).flat.every((entry) => !entry.schemaAvailable && entry.count === null)).toBe(true);
  });

  it('does not apply a schema response from before logout to the next session', async () => {
    const old = Promise.withResolvers<ListSchemasResponse>();
    const request = vi.spyOn(schemas, 'list').mockReturnValueOnce(old.promise).mockResolvedValueOnce({ schemas: [known] });
    const first = refreshCatalogue();
    stopCatalogue();
    await refreshCatalogue();
    old.resolve({ schemas: [{ ...known, kind: 'OldChild', advanced: true, ownerKind: 'OldParent' }] });
    await first;
    expect(request.mock.calls[0][0]?.signal?.aborted).toBe(true);
    expect(get(catalogue).flat.map((entry) => entry.kind)).toEqual([known.kind]);
    expect(advancedKind(known.apiVersion, 'OldChild')).toBeUndefined();
  });
});
