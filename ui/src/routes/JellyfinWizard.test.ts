import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { mount, tick, unmount } from 'svelte';
import { get } from 'svelte/store';
import JellyfinWizard from './JellyfinWizard.svelte';
import { discovery, inventoryApi, operations, ping, resources, session, UnauthorizedError, type DiscoveryContext, type ListSavedResourcesResponse } from '../lib/api';
import { auth } from '../lib/auth';
import { recordAuthority, resetAuthority } from '../lib/authority';
import { REVIEW_KEY } from '../lib/deploymentReview';

let component: Record<string, unknown> | undefined;
let host: HTMLDivElement;
const saved = (name: string) => ({ apiVersion: 'proxmox.openctl.io/v1', kind: 'VirtualMachine', metadata: { name } });
beforeEach(() => {
  host = document.createElement('div'); document.body.append(host);
  window.sessionStorage.removeItem(REVIEW_KEY);
  auth.set({ kind: 'signed-in', me: { userId: 'operator', sessionId: 'session', principalId: 'operator', role: 'admin' } });
  recordAuthority('mirror');
  vi.spyOn(inventoryApi, 'list').mockResolvedValue({ resources: [saved('offline-gpu')] });
  vi.spyOn(discovery, 'contexts').mockResolvedValue({ contexts: [{ name: 'lab', defaultContext: true }] });
  vi.spyOn(ping, 'ping').mockResolvedValue({ echo: '', serverVersion: 'fixture', supportsIdempotentApply: true, controllerId: 'controller' });
  vi.spyOn(session, 'whoami').mockResolvedValue({ userId: 'operator', sessionId: 'session', principalId: 'operator', role: 'admin' });
  vi.spyOn(resources, 'list').mockRejectedValue(new Error('An unrelated Proxmox context is offline'));
  vi.spyOn(resources, 'get').mockRejectedValue(new Error('No live observation should be requested while choosing a saved name'));
  vi.spyOn(resources, 'apply').mockRejectedValue(new Error('Unexpected Apply'));
  vi.spyOn(operations, 'byRequest').mockRejectedValue(new Error('Unexpected request lookup'));
  vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('Unexpected network in isolated wizard test')));
});
afterEach(async () => {
  if (component) await unmount(component); component = undefined;
  host.remove(); window.sessionStorage.removeItem(REVIEW_KEY);
  resetAuthority(); auth.set({ kind: 'unknown' }); vi.restoreAllMocks(); vi.unstubAllGlobals();
});
async function settle() { for (let i = 0; i < 7; i++) { await Promise.resolve(); await tick(); } }
async function open() { component = mount(JellyfinWizard, { target: host }); await settle(); }
function button(label: string) {
  const node = [...host.querySelectorAll<HTMLButtonElement>('button')].find((item) => item.textContent?.trim() === label);
  if (!node) throw new Error(`Missing action ${label}`); return node;
}
function control(label: string) {
  const node = [...host.querySelectorAll<HTMLLabelElement>('label')].find((item) => item.textContent?.trim().startsWith(label));
  if (!node) throw new Error(`Missing field ${label}`);
  return node.querySelector<HTMLInputElement | HTMLSelectElement>('input,select')!;
}
function check(label: string, checked = true) {
  const node = control(label) as HTMLInputElement; node.checked = checked; node.dispatchEvent(new Event('change', { bubbles: true }));
}
function choices() { return [...(control('Saved managed VM') as HTMLSelectElement).options].map((option) => option.value); }

describe('saved metadata guest selection', () => {
  it('offers saved guest names despite a failing whole-lab observation, without querying live guests', async () => {
    await open();
    expect(choices()).toContain('offline-gpu');
    expect(control('Saved managed VM').disabled).toBe(false);
    expect(resources.list).not.toHaveBeenCalled();
    expect(resources.get).not.toHaveBeenCalled();
    expect(resources.apply).not.toHaveBeenCalled();
    expect(window.sessionStorage.getItem(REVIEW_KEY)).toBeNull();
  });

  it('does not wait for active-context discovery before showing saved VM choices', async () => {
    const pending = Promise.withResolvers<{ contexts?: DiscoveryContext[] }>();
    vi.mocked(discovery.contexts).mockReturnValueOnce(pending.promise);
    await open();
    expect(choices()).toContain('offline-gpu');
    expect(control('Saved managed VM').disabled).toBe(false);
    pending.resolve({ contexts: [] }); await settle();
    expect(choices()).toContain('offline-gpu');
  });

  it('does not turn active-context failure into a saved guest inventory failure', async () => {
    vi.mocked(discovery.contexts).mockRejectedValueOnce(new Error('Active provider registry unavailable'));
    await open(); expect(choices()).toContain('offline-gpu');
    expect(control('Saved managed VM').disabled).toBe(false);
    check('Provision a new VM'); await tick();
    expect(control('Proxmox context').disabled).toBe(true);
    expect(button('Prepare and save deployment review').disabled).toBe(true);
    expect(host.querySelector('[role="alert"]')).not.toBeNull();
    check('Provision a new VM', false); await tick();
    expect(choices()).toContain('offline-gpu');
    expect(button('Prepare and save deployment review').disabled).toBe(false);
  });

  it('blocks an unavailable saved snapshot without blocking new-VM context selection', async () => {
    vi.mocked(inventoryApi.list).mockRejectedValueOnce(new Error('Saved metadata unavailable'));
    await open();
    expect(control('Saved managed VM').disabled).toBe(true);
    expect(button('Prepare and save deployment review').disabled).toBe(true);
    expect(host.querySelector('[role="alert"]')).not.toBeNull();
    expect(choices()).toEqual(['']);
    check('Provision a new VM'); await tick();
    expect(control('Proxmox context').disabled).toBe(false);
    expect(button('Prepare and save deployment review').disabled).toBe(false);
    expect(resources.list).not.toHaveBeenCalled();
  });

  it('distinguishes an empty saved snapshot from an unavailable snapshot', async () => {
    vi.mocked(inventoryApi.list).mockResolvedValueOnce({ resources: [] });
    await open(); expect(choices()).toEqual(['']);
    expect(host.querySelector('[role="alert"]')).toBeNull();
    expect(resources.list).not.toHaveBeenCalled();
  });

  it('refreshes saved choices after an error and clears stale guest consent without Apply', async () => {
    vi.mocked(inventoryApi.list).mockRejectedValueOnce(new Error('Temporary saved-store outage')).mockResolvedValueOnce({ resources: [saved('recovered-gpu')] });
    await open();
    check('This is a dedicated'); check('I will verify'); await tick();
    button('Refresh saved VM choices').click(); await settle();
    expect(choices()).toContain('recovered-gpu');
    expect(host.querySelector('[role="alert"]')).toBeNull();
    expect((control('This is a dedicated') as HTMLInputElement).checked).toBe(false);
    expect((control('I will verify') as HTMLInputElement).checked).toBe(false);
    expect(resources.apply).not.toHaveBeenCalled();
    expect(resources.list).not.toHaveBeenCalled();
  });

  it('does not populate an obsolete page when metadata arrives after teardown', async () => {
    const pending = Promise.withResolvers<ListSavedResourcesResponse>();
    vi.mocked(inventoryApi.list).mockReturnValueOnce(pending.promise);
    await open(); const signal = vi.mocked(inventoryApi.list).mock.calls[0][2]?.signal;
    await unmount(component!); component = undefined;
    expect(signal?.aborted).toBe(true);
    pending.resolve({ resources: [saved('late-private-name')] }); await settle();
    expect(host.textContent).toBe('');
    expect(resources.apply).not.toHaveBeenCalled();
  });

  it.each(['inventory', 'contexts'])('ends the session on %s authorization loss', async (source) => {
    if (source === 'inventory') vi.mocked(inventoryApi.list).mockRejectedValueOnce(new UnauthorizedError('expired'));
    else vi.mocked(discovery.contexts).mockRejectedValueOnce(new UnauthorizedError('expired'));
    await open(); expect(get(auth).kind).toBe('signed-out');
    expect(resources.apply).not.toHaveBeenCalled();
  });
});
