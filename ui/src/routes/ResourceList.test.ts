import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { mount, tick, unmount } from 'svelte';
import ResourceList from './ResourceList.svelte';
import { inventoryApi, resources, schemas, type GetSchemaResponse, type Resource } from '../lib/api';
import { auth } from '../lib/auth';
import { manifestAuthority, resetAuthority } from '../lib/authority';
import * as watch from '../lib/watch';

const apiVersion = 'example.io/v1';
const kind = 'Machine';
const resource: Resource = { apiVersion, kind, metadata: { name: 'desired' }, status: { state: 'running' } };
let component: Record<string, unknown> | undefined;
let target: HTMLDivElement;

beforeEach(() => {
  vi.useFakeTimers();
  auth.set({ kind: 'signed-in', me: { userId: 'user', sessionId: 'session', role: 'editor' } });
  manifestAuthority.set({ mode: 'mirror', loading: false, error: '' });
  vi.spyOn(watch, 'watchResources').mockReturnValue(Promise.withResolvers<void>().promise);
  target = document.createElement('div');
  document.body.append(target);
});
afterEach(async () => {
  if (component) await unmount(component);
  component = undefined;
  target.remove();
  auth.set({ kind: 'unknown' });
  resetAuthority();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

async function settle() {
  await vi.advanceTimersByTimeAsync(0);
  await tick();
}

describe('resource-list saved versus observed presentation', () => {
  it('keeps unregistered saved names inspectable without green readiness, drift or schema/create controls', async () => {
    vi.spyOn(inventoryApi, 'list').mockResolvedValue({ resources: [{
      apiVersion, kind, metadata: { name: 'desired', labels: { environment: 'homelab' } }, source: 'ui',
    }] });
    vi.spyOn(resources, 'list').mockRejectedValue(new Error('provider unregistered'));
    const schema = vi.spyOn(schemas, 'get');
    component = mount(ResourceList, { target, props: {
      apiVersion, kind, provider: '', schemaAvailable: false, observedOnly: false,
    } });
    await settle();
    const link = target.querySelector('tbody a');
    expect(link?.getAttribute('href')).toContain('/desired');
    expect(target.querySelector('tbody .state-unknown')).not.toBeNull();
    expect(target.querySelector('tbody .state-good')).toBeNull();
    expect(target.querySelector('tbody .drift-clean')).toBeNull();
    expect(target.querySelector('.new-btn')).toBeNull();
    expect(target.querySelector('.schema-btn')).toBeNull();
    expect(schema).not.toHaveBeenCalled();

    const filter = target.querySelector<HTMLInputElement>('input')!;
    filter.value = 'homelab';
    filter.dispatchEvent(new Event('input', { bubbles: true }));
    await tick();
    expect(target.querySelectorAll('tbody tr')).toHaveLength(1);
    filter.value = 'not-a-label';
    filter.dispatchEvent(new Event('input', { bubbles: true }));
    await tick();
    expect(target.querySelectorAll('tbody tr')).toHaveLength(0);
  });

  it('does not expose creation for discovery-only kinds even to an editor with mirror authority', async () => {
    const saved = vi.spyOn(inventoryApi, 'list');
    vi.spyOn(resources, 'list').mockResolvedValue({ resources: [resource] });
    component = mount(ResourceList, { target, props: {
      apiVersion, kind, provider: 'discovery', schemaAvailable: true, observedOnly: true,
    } });
    await settle();
    expect(target.querySelectorAll('tbody tr')).toHaveLength(1);
    expect(target.querySelector('.new-btn')).toBeNull();
    expect(saved).not.toHaveBeenCalled();
  });

  it('preserves the role and running-authority gates for supported desired-resource creation', async () => {
    vi.spyOn(inventoryApi, 'list').mockResolvedValue({ resources: [] });
    vi.spyOn(resources, 'list').mockResolvedValue({ resources: [] });
    component = mount(ResourceList, { target, props: {
      apiVersion, kind, provider: 'example', schemaAvailable: true, observedOnly: false,
    } });
    await settle();
    expect(target.querySelector('.new-btn')).not.toBeNull();
    auth.set({ kind: 'signed-in', me: { userId: 'user', sessionId: 'viewer', role: 'viewer' } });
    await tick();
    expect(target.querySelector('.new-btn')).toBeNull();
    auth.set({ kind: 'signed-in', me: { userId: 'user', sessionId: 'editor', role: 'editor' } });
    manifestAuthority.set({ mode: 'gitops', loading: false, error: '' });
    await tick();
    expect(target.querySelector('.new-btn')).toBeNull();
    manifestAuthority.set({ mode: null, loading: false, error: 'unavailable' });
    await tick();
    expect(target.querySelector('.new-btn')).toBeNull();
  });

  it('aborts the lazy schema viewer on route teardown and rejects its late response in the next route', async () => {
    vi.spyOn(inventoryApi, 'list').mockResolvedValue({ resources: [] });
    vi.spyOn(resources, 'list').mockResolvedValue({ resources: [] });
    const old = Promise.withResolvers<GetSchemaResponse>();
    const current = Promise.withResolvers<GetSchemaResponse>();
    const schema = vi.spyOn(schemas, 'get').mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise);
    component = mount(ResourceList, { target, props: {
      apiVersion, kind, provider: 'example', schemaAvailable: true,
    } });
    await settle();
    target.querySelector<HTMLButtonElement>('.schema-btn')!.click();
    await tick();
    expect(schema).toHaveBeenCalledTimes(1);
    await unmount(component);
    component = undefined;
    expect(schema.mock.calls[0][2]?.signal?.aborted).toBe(true);

    component = mount(ResourceList, { target, props: {
      apiVersion: 'next.io/v1', kind: 'Next', provider: 'next', schemaAvailable: true,
    } });
    await settle();
    target.querySelector<HTMLButtonElement>('.schema-btn')!.click();
    await tick();
    old.resolve({ cueSource: 'old-route-schema' });
    await settle();
    expect(target.querySelector('pre.cue')).toBeNull();
    expect(schema.mock.calls[1].slice(0, 2)).toEqual(['next.io/v1', 'Next']);
    expect(schema.mock.calls[1][2]?.signal?.aborted).toBe(false);
    current.resolve({ cueSource: 'current-route-schema' });
    await settle();
    expect(target.querySelector('pre.cue')?.textContent).toBe('current-route-schema');
  });
});
