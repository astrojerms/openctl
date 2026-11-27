import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { mount, tick, unmount } from 'svelte';
import { get } from 'svelte/store';
import Detail from './Detail.svelte';
import { ApiError, UnauthorizedError, inventoryApi, operations, ownership, repo, resources, type GetResourceResponse, type GetSavedResourceResponse } from '../lib/api';
import { auth } from '../lib/auth';
import { recordAuthority, resetAuthority } from '../lib/authority';
import { ops } from '../lib/ops';
import * as watch from '../lib/watch';

const props = { apiVersion: 'proxmox.openctl.io/v1', kind: 'VirtualMachine', resourceName: 'saved-vm' };
const saved: GetSavedResourceResponse = {
  resource: { apiVersion: props.apiVersion, kind: props.kind, metadata: { name: props.resourceName, annotations: { note: 'raw annotation' } }, spec: { password: '${secret:homelab/password}', retry: { requestId: 'keep-marker' } } },
  source: 'ui', appliedAt: '2026-11-27T12:00:00Z',
};
const observed: GetResourceResponse = {
  resource: { ...saved.resource, status: { phase: 'running', ready: true } },
  applied: saved.resource, appliedAt: saved.appliedAt,
};
let component: Record<string, unknown> | undefined;
let target: HTMLDivElement;
let notify: () => void;

beforeEach(() => {
  target = document.createElement('div');
  document.body.append(target);
  auth.set({ kind: 'signed-in', me: { role: 'admin', userId: 'admin', sessionId: 'session' } });
  recordAuthority('mirror');
  ops.set([]);
  vi.spyOn(inventoryApi, 'get').mockResolvedValue(saved);
  vi.spyOn(resources, 'listActions').mockResolvedValue({ actionSpecs: [{ name: 'start' }, { name: 'upgrade', parameters: [{ name: 'version', required: true, defaultValue: 'v1' }] }] });
  vi.spyOn(resources, 'childrenGraph').mockResolvedValue({ nodes: [], edges: [] });
  vi.spyOn(repo, 'history').mockResolvedValue({ commits: [] });
  vi.spyOn(watch, 'watchResources').mockImplementation((_av, _kind, _name, onEvent, opts) => {
    const pending = Promise.withResolvers<void>();
    notify = () => onEvent({ type: 'MODIFIED', resource: observed.resource });
    opts?.signal?.addEventListener('abort', () => pending.resolve(), { once: true });
    return pending.promise;
  });
});
afterEach(async () => {
  if (component) await unmount(component);
  component = undefined;
  target.remove();
  ops.set([]);
  resetAuthority();
  auth.set({ kind: 'unknown' });
  vi.restoreAllMocks();
});
async function settle() {
  await Promise.resolve();
  await tick();
  await Promise.resolve();
  await tick();
}
function button(label: string) {
  const found = [...target.querySelectorAll<HTMLButtonElement>('button')].find((node) => node.textContent?.replace(/\s+/g, '') === label.replace(/\s+/g, ''));
  if (!found) throw new Error(`Missing button: ${label}`);
  return found;
}

describe('saved-only detail inspection', () => {
  it.each([503, 404])('shows raw saved intent with unknown readiness on live HTTP %i without applying or replacing', async (status) => {
    vi.spyOn(resources, 'get').mockRejectedValue(new ApiError(status, 'provider unavailable'));
    const apply = vi.spyOn(resources, 'apply');
    const invoke = vi.spyOn(resources, 'invokeAction');
    const remove = vi.spyOn(resources, 'delete');
    const exportBundle = vi.spyOn(ownership, 'export');
    const forget = vi.spyOn(ownership, 'forget');
    component = mount(Detail, { target, props });
    await settle();
    expect(target.textContent).toContain('Saved desired intent');
    expect(target.textContent).toContain('${secret:homelab/password}');
    expect(target.textContent).toContain('raw annotation');
    expect(target.textContent).toContain(saved.appliedAt);
    expect(target.textContent).toContain('Source: ui');
    expect(target.textContent).toContain('Observation unknown');
    if (status === 404) expect(target.textContent).toContain('Provider/Get reported not found');
    expect(target.querySelector('.state-good')).toBeNull();
    expect(target.textContent).not.toContain('Observed state');
    expect(button('Reconcile').disabled).toBe(true);
    expect(button('Delete').disabled).toBe(true);
    expect(target.querySelector('a.edit-btn')).toBeNull();
    expect(apply).not.toHaveBeenCalled();
    expect(invoke).not.toHaveBeenCalled();
    expect(remove).not.toHaveBeenCalled();
    expect(exportBundle).not.toHaveBeenCalled();
    expect(forget).not.toHaveBeenCalled();
  });

  it('guards already-open action forms and every mutation handler as soon as a healthy observation is invalidated', async () => {
    vi.spyOn(resources, 'get').mockResolvedValueOnce(observed).mockRejectedValue(new Error('provider now offline'));
    const apply = vi.spyOn(resources, 'apply');
    const invoke = vi.spyOn(resources, 'invokeAction');
    const remove = vi.spyOn(resources, 'delete');
    const exportBundle = vi.spyOn(ownership, 'export').mockResolvedValue({ bundle: { version: 1, resource: saved.resource, controllerId: 'controller', manifestDigest: 'digest', identity: 'identity' } });
    const forget = vi.spyOn(ownership, 'forget');
    const prompt = vi.spyOn(window, 'prompt').mockReturnValue(props.resourceName);
    vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:download');
    vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {});
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    component = mount(Detail, { target, props });
    await settle();
    const acknowledgment = target.querySelector<HTMLInputElement>('.migration-confirm input')!;
    acknowledgment.checked = true;
    acknowledgment.dispatchEvent(new Event('change', { bubbles: true }));
    await tick();
    button('Export bundle').click();
    await settle();
    expect(exportBundle).toHaveBeenCalledTimes(1);
    button('Upgrade…').click();
    await tick();
    const form = target.querySelector<HTMLFormElement>('form.param-form')!;
    expect(form).not.toBeNull();
    const runtime = button('Start');
    const reconcile = button('Reconcile');
    const removeButton = button('Delete');
    const exportButton = button('Export bundle');
    const forgetButton = button('Release tracking (keep VM)');
    const edit = target.querySelector<HTMLAnchorElement>('a.edit-btn')!;

    // Before Svelte redraws the still-enabled DOM, the functions themselves
    // must consult the invalidated inspection, not old rendered booleans.
    notify();
    runtime.click();
    reconcile.click();
    removeButton.click();
    exportButton.click();
    forgetButton.click();
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    const editEvent = new MouseEvent('click', { bubbles: true, cancelable: true });
    edit.dispatchEvent(editEvent);
    expect(editEvent.defaultPrevented).toBe(true);
    await settle();
    expect(invoke).not.toHaveBeenCalled();
    expect(apply).not.toHaveBeenCalled();
    expect(remove).not.toHaveBeenCalled();
    expect(exportBundle).toHaveBeenCalledTimes(1);
    expect(forget).not.toHaveBeenCalled();
    expect(prompt).not.toHaveBeenCalled();
    expect(target.querySelector('form.param-form')).toBeNull();
    expect(target.querySelector('.state-good')).toBeNull();
    expect(target.textContent).not.toContain('Observed state');
    expect(target.textContent).toContain('Saved desired intent');
    expect(button('Start').disabled).toBe(true);
    expect(button('Reconcile').disabled).toBe(true);
    expect(button('Delete').disabled).toBe(true);
  });

  it('loads explicit read-only operation history independently of the failed provider', async () => {
    vi.spyOn(resources, 'get').mockRejectedValue(new Error('provider offline'));
    const history = vi.spyOn(operations, 'list').mockResolvedValue({ operations: [{ id: 'previous-operation', type: 'apply', status: 'failed', error: 'persisted error' }] });
    const apply = vi.spyOn(resources, 'apply');
    component = mount(Detail, { target, props });
    await settle();
    expect(history).not.toHaveBeenCalled();
    button('Inspect operation history').click();
    await settle();
    expect(history).toHaveBeenCalledExactlyOnceWith({ apiVersion: props.apiVersion, kind: props.kind, resourceName: props.resourceName, limit: 100 }, { signal: expect.any(AbortSignal) });
    expect(target.textContent).toContain('previous-operation');
    expect(target.textContent).toContain('persisted error');
    expect(target.textContent).toContain('not current provider readiness or authority to retry');
    expect(apply).not.toHaveBeenCalled();
  });

  it('opens fresh editor review for reconciliation without submitting Apply', async () => {
    vi.spyOn(resources, 'get').mockResolvedValue(observed);
    const apply = vi.spyOn(resources, 'apply');
    const previousHash = location.hash;
    try {
      component = mount(Detail, { target, props });
      await settle();
      button('Reconcile').click();
      expect(location.hash).toBe('#/k/proxmox.openctl.io/v1/VirtualMachine/saved-vm/edit');
      expect(apply).not.toHaveBeenCalled();
    } finally {
      location.hash = previousHash;
    }
  });

  it('recovers the full live detail only after an explicit retry succeeds', async () => {
    vi.spyOn(resources, 'get').mockRejectedValueOnce(new ApiError(404, 'missing')).mockResolvedValueOnce(observed);
    const apply = vi.spyOn(resources, 'apply');
    component = mount(Detail, { target, props });
    await settle();
    expect(target.textContent).not.toContain('Observed state');
    button('Retry observation').click();
    await settle();
    expect(target.textContent).toContain('Observed state');
    expect(target.textContent).toContain('Saved desired intent');
    expect(button('Start').disabled).toBe(false);
    expect(button('Reconcile').disabled).toBe(false);
    expect(apply).not.toHaveBeenCalled();
  });

  it('renders mixed application evidence neutrally for skipped, absent optional, and unsupported checks', async () => {
    const appProps = { ...props, apiVersion: 'apps.openctl.io/v1', kind: 'Jellyfin' };
    const resource = { ...saved.resource, apiVersion: appProps.apiVersion, kind: appProps.kind };
    vi.spyOn(inventoryApi, 'get').mockResolvedValue({ ...saved, resource });
    vi.spyOn(resources, 'get').mockResolvedValue({ resource: { ...resource, status: { ready: false, checks: {
      application: { state: 'ready', message: '<img src=x onerror=alert(1)>' },
      http: { state: 'not_ready', message: 'Attempted probe failed' },
      gpu: { state: 'unknown', message: 'Skipped without SSH' },
      publicEndpoint: { state: 'not_configured' },
      future: { state: 'future' }, obsolete: { ready: true }, malformed: [],
    } } } });
    component = mount(Detail, { target, props: appProps });
    await settle();
    const rows = [...target.querySelectorAll('tr')];
    for (const [name, label, tone] of [
      ['application', 'Ready', 'good'], ['http', 'Not ready', 'warn'],
      ['gpu', 'Unknown', 'unknown'], ['publicEndpoint', 'Not configured', 'unknown'],
      ['future', 'Unknown', 'unknown'], ['obsolete', 'Unknown', 'unknown'], ['malformed', 'Unknown', 'unknown'],
    ]) {
      const row = rows.find((node) => node.cells[0]?.textContent === name)!;
      expect(row.cells[1].textContent).toBe(label);
      expect(row.cells[1].querySelector(`.state-${tone}`)).not.toBeNull();
    }
    expect(target.textContent).toContain('<img src=x onerror=alert(1)>');
    expect(target.querySelector('img')).toBeNull();
  });

  it('removes application checks and child readiness and ignores late child results when observation fails', async () => {
    const child = Promise.withResolvers<GetResourceResponse>();
    const appProps = { ...props, apiVersion: 'apps.openctl.io/v1', kind: 'Application' };
    const appSaved = { ...saved, resource: { ...saved.resource, apiVersion: appProps.apiVersion, kind: appProps.kind } };
    const appObserved: GetResourceResponse = { resource: {
      ...appSaved.resource,
      status: { phase: 'ready', ready: true, checks: { endpoint: { state: 'ready', message: 'previous green check' } } },
      children: [{ apiVersion: props.apiVersion, kind: props.kind, name: 'child' }],
    } };
    vi.spyOn(inventoryApi, 'get').mockResolvedValue(appSaved);
    let rootCalls = 0;
    const live = vi.spyOn(resources, 'get').mockImplementation((_av, _kind, name) => {
      if (name === 'child') return child.promise;
      rootCalls += 1;
      return rootCalls === 1 ? Promise.resolve(appObserved) : Promise.reject(new Error('provider offline'));
    });
    component = mount(Detail, { target, props: appProps });
    await settle();
    expect(target.textContent).toContain('previous green check');
    expect(target.textContent).toContain('Children (1)');
    const childSignal = live.mock.calls.find((call) => call[2] === 'child')?.[3]?.signal;
    notify();
    await settle();
    expect(childSignal?.aborted).toBe(true);
    child.resolve(observed);
    await settle();
    expect(target.textContent).not.toContain('previous green check');
    expect(target.textContent).not.toContain('Children (1)');
    expect(target.querySelector('.state-good')).toBeNull();
    expect(target.textContent).toContain('Observation unknown');
    expect(target.textContent).toContain('Saved desired intent');
  });

  it('clears saved inspection and signs out on live 401 rather than falling back to persisted intent', async () => {
    const live = Promise.withResolvers<GetResourceResponse>();
    vi.spyOn(resources, 'get').mockReturnValue(live.promise);
    component = mount(Detail, { target, props });
    await settle();
    expect(target.textContent).toContain('Saved desired intent');
    live.reject(new UnauthorizedError('expired session'));
    await settle();
    expect(get(auth).kind).toBe('signed-out');
    expect(target.textContent).not.toContain('Saved desired intent');
    expect(target.textContent).not.toContain('${secret:homelab/password}');
    expect(target.textContent).not.toContain('Provider/Get reported not found');
  });
});
