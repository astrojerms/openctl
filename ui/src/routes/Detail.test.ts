import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { mount, tick, unmount } from 'svelte';
import { fromStore, get, writable } from 'svelte/store';
import Detail from './Detail.svelte';
import { ApiError, UnauthorizedError, inventoryApi, operations, ownership, repo, resources } from '../lib/api';
import type { ActionSpec, GetResourceResponse, GetSavedResourceResponse, InvokeActionResponse } from '../lib/api';
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
  vi.spyOn(resources, 'listActions').mockResolvedValue({ actionSpecs: [{ name: 'start', label: 'Start' }, { name: 'upgrade', label: 'Upgrade', parameters: [{ name: 'version', required: true, defaultValue: 'v1' }] }] });
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
    button('Upgrade').click();
    await tick();
    const form = target.querySelector<HTMLFormElement>('form.runtime-action-review')!;
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
    expect(target.querySelector('form.runtime-action-review')).toBeNull();
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

  it.each([false, true, undefined])('renders mixed application evidence without a green aggregate badge despite ready=%j', async (ready) => {
    const appProps = { ...props, apiVersion: 'apps.openctl.io/v1', kind: 'Jellyfin' };
    const resource = { ...saved.resource, apiVersion: appProps.apiVersion, kind: appProps.kind };
    vi.spyOn(inventoryApi, 'get').mockResolvedValue({ ...saved, resource });
    vi.spyOn(resources, 'get').mockResolvedValue({ resource: { ...resource, status: { phase: 'ready', ready, checks: {
      application: { state: 'ready', message: '<img src=x onerror=alert(1)>' },
      http: { state: 'not_ready', message: 'Attempted probe failed' },
      gpu: { state: 'unknown', message: 'Skipped without SSH' },
      publicEndpoint: { state: 'not_configured' },
      future: { state: 'future' }, obsolete: { ready: true }, malformed: [],
    } } } });
    component = mount(Detail, { target, props: appProps });
    await settle();
    expect(target.querySelector('header .header-right > .state')?.textContent).toBe('Not ready');
    expect(target.querySelector('header .header-right > .state-good')).toBeNull();
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

  it.each([
    { phase: 'ready' },
    { phase: 'ready', ready: true, checks: { gpu: { state: 'unknown' } } },
    { phase: 'ready', ready: true, checks: [] },
  ])('shows application aggregate unknown rather than trusting a ready phase: %j', async (status) => {
    const appProps = { ...props, apiVersion: 'apps.openctl.io/v1', kind: 'Jellyfin' };
    const resource = { ...saved.resource, apiVersion: appProps.apiVersion, kind: appProps.kind };
    vi.spyOn(inventoryApi, 'get').mockResolvedValue({ ...saved, resource });
    vi.spyOn(resources, 'get').mockResolvedValue({ resource: { ...resource, status } });
    component = mount(Detail, { target, props: appProps });
    await settle();
    expect(target.querySelector('header .header-right > .state')?.textContent).toBe('Unknown');
    expect(target.querySelector('header .header-right > .state-good')).toBeNull();
  });

  it('does not count an application child as healthy from phase or contradictory readiness', async () => {
    const child = { apiVersion: 'apps.openctl.io/v1', kind: 'Jellyfin', name: 'media' };
    vi.spyOn(resources, 'get').mockImplementation(async (_av, _kind, name) => name === child.name
      ? { resource: { apiVersion: child.apiVersion, kind: child.kind, metadata: { name }, status: {
        phase: 'ready', ready: true, checks: { gpu: { state: 'unknown' } },
      } } }
      : { ...observed, resource: { ...observed.resource, children: [child] } });
    component = mount(Detail, { target, props });
    await vi.waitFor(() => expect(target.querySelector('[title^="Aggregated child status"]')?.textContent).toContain('1 unknown / loading'));
    expect(target.querySelector('[title^="Aggregated child status"]')?.classList.contains('state-unknown')).toBe(true);
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

describe('guest address observation', () => {
  const available = { state: 'available', code: 'guest_ip_available', message: 'A unique guest address was observed.' };
  const unavailable = { state: 'unavailable', code: 'guest_agent_unavailable', message: 'Inspect guest-agent installation and Proxmox guest-agent support.' };
  const ip = '192.0.2.42';

  function withStatus(status: Record<string, unknown>): GetResourceResponse {
    return { ...observed, resource: { ...observed.resource, status } };
  }

  function addressCard() {
    return target.querySelector<HTMLElement>('article[aria-label="Guest address"]');
  }

  it('shows fresh address evidence without implying reachability or performing mutations', async () => {
    const live = vi.spyOn(resources, 'get').mockResolvedValue(withStatus({ state: 'running', ip, ipObservation: available }));
    const apply = vi.spyOn(resources, 'apply');
    const invoke = vi.spyOn(resources, 'invokeAction');
    const remove = vi.spyOn(resources, 'delete');
    component = mount(Detail, { target, props });
    await settle();
    expect(addressCard()?.textContent).toContain('Address observed');
    expect(addressCard()?.textContent).toContain(ip);
    expect(addressCard()?.textContent).toContain('Reachability unverified');
    expect(addressCard()?.textContent).toContain('does not prove SSH access or service readiness');
    expect(addressCard()?.textContent).not.toMatch(/Ready|healthy/);
    expect(live).toHaveBeenCalledTimes(1);
    expect(apply).not.toHaveBeenCalled();
    expect(invoke).not.toHaveBeenCalled();
    expect(remove).not.toHaveBeenCalled();
  });

  it('shows the safe blocker and repair guidance, not a contradictory retained address', async () => {
    vi.spyOn(resources, 'get').mockResolvedValue(withStatus({ state: 'running', ip, ipObservation: unavailable }));
    component = mount(Detail, { target, props });
    await settle();
    expect(addressCard()?.textContent).toContain('Address unavailable');
    expect(addressCard()?.textContent).toContain(unavailable.code);
    expect(addressCard()?.textContent).toContain(unavailable.message);
    expect(target.textContent).not.toContain(ip);
  });

  it.each([
    { state: 'running', ip },
    { state: 'running', ip, ipObservation: null },
    { state: 'running', ip, ipObservation: [] },
    { state: 'running', ip, ipObservation: 'available' },
    { state: 'running', ip, ipObservation: { ...available, message: null } },
    { state: 'running', ip, ipObservation: { ...available, code: undefined } },
    { state: 'running', ip, ipObservation: { ...available, state: 'future-state' } },
    { state: 'running', ip, ipObservation: { ...available, code: 'future_code' } },
    { state: 'running', ip, ipObservation: { ...unavailable, code: 'future_code' } },
    { state: 'stopped', ip, ipObservation: available },
    { state: 'running', ip: '  ', ipObservation: available },
    { state: 'running', ip: 42, ipObservation: available },
  ])('keeps malformed, legacy or unknown evidence unknown: %j', async (status) => {
    vi.spyOn(resources, 'get').mockResolvedValue(withStatus(status));
    component = mount(Detail, { target, props });
    await settle();
    expect(addressCard()?.textContent).toContain('Address observation unknown');
    expect(addressCard()?.textContent).toContain('Inspect VM power, configured networking and guest-agent support');
    expect(target.textContent).not.toContain(ip);
    expect(addressCard()?.textContent).not.toContain('Address observed');
  });

  it.each([
    { apiVersion: 'apps.openctl.io/v1', kind: 'VirtualMachine' },
    { apiVersion: props.apiVersion, kind: 'OtherResource' },
  ])('excludes other resource types: %j', async (type) => {
    vi.spyOn(resources, 'get').mockResolvedValue(withStatus({ state: 'running', ip, ipObservation: available }));
    component = mount(Detail, { target, props: { ...props, ...type } });
    await settle();
    expect(addressCard()).toBeNull();
  });

  it.each([available, unavailable])('invalidates retained evidence on outage and recovers only after a fresh Get: %j', async (diagnostic) => {
    const recovery = Promise.withResolvers<GetResourceResponse>();
    vi.spyOn(resources, 'get')
      .mockResolvedValueOnce(withStatus({ state: 'running', ip, ipObservation: diagnostic }))
      .mockRejectedValueOnce(new Error('provider offline'))
      .mockReturnValueOnce(recovery.promise);
    component = mount(Detail, { target, props });
    await settle();
    expect(addressCard()?.textContent).toContain(diagnostic === available ? 'Address observed' : diagnostic.message);
    notify();
    await settle();
    expect(addressCard()?.textContent).toContain('Address observation unknown');
    expect(addressCard()?.textContent).not.toContain(ip);
    expect(addressCard()?.textContent).not.toContain(diagnostic.message);
    button('Retry observation').click();
    await settle();
    expect(addressCard()?.textContent).toContain('Address observation unknown');
    recovery.resolve(withStatus({ state: 'running', ip: '192.0.2.43', ipObservation: available }));
    await settle();
    expect(addressCard()?.textContent).toContain('Address observed');
    expect(addressCard()?.textContent).toContain('192.0.2.43');
    expect(addressCard()?.textContent).not.toContain(ip);
  });

  it('does not display retained evidence after sign-out', async () => {
    vi.spyOn(resources, 'get').mockResolvedValue(withStatus({ state: 'running', ip, ipObservation: available }));
    component = mount(Detail, { target, props });
    await settle();
    expect(addressCard()?.textContent).toContain(ip);
    auth.set({ kind: 'signed-out' });
    await settle();
    expect(target.textContent).not.toContain(ip);
    expect(addressCard()?.textContent).not.toContain('Address observed');
    auth.set({ kind: 'signed-in', me: { role: 'admin', userId: 'admin', sessionId: 'new-session' } });
    await settle();
    expect(addressCard()?.textContent).toContain('Address observation unknown');
    expect(target.textContent).not.toContain(ip);
  });

  it('clears the previous route address while the next VM inspection is pending', async () => {
    const next = Promise.withResolvers<GetResourceResponse>();
    vi.spyOn(resources, 'get')
      .mockResolvedValueOnce(withStatus({ state: 'running', ip, ipObservation: available }))
      .mockReturnValueOnce(next.promise);
    const route = writable({ ...props });
    const current = fromStore(route);
    component = mount(Detail, { target, props: {
      get apiVersion() { return current.current.apiVersion; },
      get kind() { return current.current.kind; },
      get resourceName() { return current.current.resourceName; },
    } });
    await settle();
    expect(addressCard()?.textContent).toContain(ip);
    route.set({ ...props, resourceName: 'next-vm' });
    await settle();
    expect(addressCard()?.textContent).toContain('Address observation unknown');
    expect(addressCard()?.textContent).not.toContain(ip);
    next.resolve(withStatus({ state: 'running', ipObservation: unavailable }));
    await settle();
    expect(addressCard()?.textContent).toContain(unavailable.message);
    expect(addressCard()?.textContent).not.toContain(ip);
  });
});

describe('runtime action review', () => {
  function reviewForm() {
    const form = target.querySelector<HTMLFormElement>('form[aria-label="Review runtime action"]');
    if (!form) throw new Error('Missing runtime action review');
    return form;
  }

  function submit(form = reviewForm()) {
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
  }

  async function openAction(spec: ActionSpec) {
    vi.spyOn(resources, 'get').mockResolvedValue(observed);
    vi.mocked(resources.listActions).mockResolvedValue({ actionSpecs: [spec] });
    component = mount(Detail, { target, props });
    await settle();
    button(spec.label || spec.name).click();
    await tick();
    return reviewForm();
  }

  it.each([
    { name: 'restart', label: 'Restart Jellyfin', description: 'Restart this media server.' },
    { name: 'get-kubeconfig', label: 'Download credentials', description: 'Download cluster credentials.' },
    { name: 'upgrade', label: 'Upgrade service', parameters: [{ name: 'version', required: true, defaultValue: 'v1' }] },
  ])('reviews $name without invoking and cancel revokes the captured form', async (spec) => {
    const invoke = vi.spyOn(resources, 'invokeAction');
    const confirm = vi.spyOn(window, 'confirm');
    const form = await openAction(spec);
    expect(form.textContent).toContain(`Review ${spec.label}`);
    const reviewedIdentity = [...form.querySelectorAll('code')].map((node) => node.textContent);
    expect(reviewedIdentity).toEqual([props.apiVersion, props.kind, props.resourceName, spec.name]);
    if (spec.description) expect(form.textContent).toContain(spec.description);
    expect(button(`Run ${spec.label}`).type).toBe('submit');
    expect(invoke).not.toHaveBeenCalled();
    expect(confirm).not.toHaveBeenCalled();
    button('Cancel').click();
    submit(form);
    await tick();
    expect(target.querySelector('form.runtime-action-review')).toBeNull();
    expect(invoke).not.toHaveBeenCalled();
  });

  it('submits the raw ID and reviewed string parameters once, preserving defaults and blocking duplicate submits', async () => {
    const pending = Promise.withResolvers<InvokeActionResponse>();
    const invoke = vi.spyOn(resources, 'invokeAction').mockReturnValue(pending.promise);
    const form = await openAction({
      name: 'upgrade', label: 'Install release', description: 'Install on this target.',
      parameters: [
        { name: 'version', required: true, defaultValue: 'v1', description: 'Release version' },
        { name: 'replicas', type: 'int', defaultValue: '2' },
        { name: 'drain', type: 'bool', defaultValue: 'false' },
      ],
    });
    const inputs = [...form.querySelectorAll('input')];
    expect(inputs.map((input) => input.value)).toEqual(['v1', '2']);
    expect(form.querySelector('select')?.value).toBe('false');
    inputs[0].value = 'v2';
    inputs[0].dispatchEvent(new Event('input', { bubbles: true }));
    inputs[1].value = '3';
    inputs[1].dispatchEvent(new Event('input', { bubbles: true }));
    expect(invoke).not.toHaveBeenCalled();
    button('Run Install release').click();
    submit(form);
    expect(invoke).toHaveBeenCalledExactlyOnceWith(
      props.apiVersion, props.kind, props.resourceName, 'upgrade',
      { version: 'v2', replicas: '3', drain: 'false' }, { signal: expect.any(AbortSignal) },
    );
    pending.resolve({ message: 'installed' });
    await settle();
    expect(target.textContent).toContain('upgrade → installed');
    expect(resources.get).toHaveBeenCalledTimes(2);
  });

  it('blocks missing required values even when a captured form bypasses native validation', async () => {
    const invoke = vi.spyOn(resources, 'invokeAction').mockResolvedValue({});
    const form = await openAction({ name: 'new-operation', parameters: [{ name: 'destination', required: true }, { name: 'enabled', type: 'bool', required: true }] });
    submit(form);
    await tick();
    expect(form.textContent).toContain('destination is required');
    expect(invoke).not.toHaveBeenCalled();
    const input = form.querySelector('input')!;
    input.value = 'chosen-target';
    input.dispatchEvent(new Event('input', { bubbles: true }));
    submit(form);
    await tick();
    expect(form.textContent).toContain('enabled is required');
    expect(invoke).not.toHaveBeenCalled();
    const select = form.querySelector('select')!;
    select.value = 'false';
    select.dispatchEvent(new Event('change', { bubbles: true }));
    submit(form);
    expect(invoke).toHaveBeenCalledExactlyOnceWith(
      props.apiVersion, props.kind, props.resourceName, 'new-operation',
      { destination: 'chosen-target', enabled: 'false' }, { signal: expect.any(AbortSignal) },
    );
    await settle();
  });

  it('requires review for unknown raw names and escapes the action, label and description', async () => {
    const raw = '<script>alert("action")</script>';
    const invoke = vi.spyOn(resources, 'invokeAction').mockResolvedValue({ message: 'completed' });
    const form = await openAction({ name: raw, description: '<img src=x onerror=alert(1)>' });
    expect(form.textContent).toContain(`Review ${raw}`);
    expect(form.textContent).toContain('<img src=x onerror=alert(1)>');
    expect(form.querySelector('script, img')).toBeNull();
    expect(invoke).not.toHaveBeenCalled();
    button(`Run ${raw}`).click();
    expect(invoke).toHaveBeenCalledExactlyOnceWith(
      props.apiVersion, props.kind, props.resourceName, raw, undefined, { signal: expect.any(AbortSignal) },
    );
    await settle();
  });

  it.each(['viewer', 'signed-out', 'different-session'])('revokes review immediately on %s auth change and does not revive it after restoration', async (change) => {
    const invoke = vi.spyOn(resources, 'invokeAction');
    const form = await openAction({ name: 'restart', label: 'Restart service' });
    const previous = get(auth);
    if (change === 'signed-out') auth.set({ kind: 'signed-out' });
    else auth.set({ kind: 'signed-in', me: { role: change === 'viewer' ? 'viewer' : 'admin', userId: 'admin', sessionId: 'new-session' } });
    submit(form);
    auth.set(previous);
    submit(form);
    await tick();
    expect(target.querySelector('form.runtime-action-review')).toBeNull();
    expect(invoke).not.toHaveBeenCalled();
  });

  it('does not reuse a prior observation review after freshness recovers or let an old form submit the new review', async () => {
    const invoke = vi.spyOn(resources, 'invokeAction').mockResolvedValue({});
    const form = await openAction({ name: 'restart', label: 'Restart service' });
    notify();
    submit(form);
    expect(invoke).not.toHaveBeenCalled();
    await settle();
    expect(target.querySelector('form.runtime-action-review')).toBeNull();
    submit(form);
    button('Restart service').click();
    await tick();
    const current = reviewForm();
    expect(current).not.toBe(form);
    submit(form);
    expect(invoke).not.toHaveBeenCalled();
    button('Run Restart service').click();
    expect(invoke).toHaveBeenCalledTimes(1);
    await settle();
  });

  it('binds consent to the exact route, including an old form dispatched before route redraw', async () => {
    const invoke = vi.spyOn(resources, 'invokeAction').mockResolvedValue({});
    vi.spyOn(resources, 'get').mockResolvedValue(observed);
    const route = writable({ ...props });
    const current = fromStore(route);
    component = mount(Detail, { target, props: {
      get apiVersion() { return current.current.apiVersion; },
      get kind() { return current.current.kind; },
      get resourceName() { return current.current.resourceName; },
    } });
    await settle();
    button('Start').click();
    await tick();
    const old = reviewForm();
    const next = { apiVersion: 'apps.openctl.io/v1', kind: 'Jellyfin', resourceName: 'other-server' };
    route.set(next);
    submit(old);
    expect(invoke).not.toHaveBeenCalled();
    await settle();
    expect(target.querySelector('form.runtime-action-review')).toBeNull();
    button('Start').click();
    await tick();
    expect([...reviewForm().querySelectorAll('code')].map((node) => node.textContent)).toEqual([next.apiVersion, next.kind, next.resourceName, 'start']);
    submit(old);
    expect(invoke).not.toHaveBeenCalled();
    button('Run Start').click();
    expect(invoke).toHaveBeenCalledExactlyOnceWith(next.apiVersion, next.kind, next.resourceName, 'start', undefined, { signal: expect.any(AbortSignal) });
    await settle();
  });

  it('renders a provider label as text without changing the raw invocation ID', async () => {
    const label = '<img src=x onerror=alert(1)>';
    const invoke = vi.spyOn(resources, 'invokeAction').mockResolvedValue({});
    const form = await openAction({ name: 'provider-custom-action', label });
    expect(form.textContent).toContain(`Review ${label}`);
    expect(target.querySelector('img')).toBeNull();
    expect(invoke).not.toHaveBeenCalled();
    button(`Run ${label}`).click();
    expect(invoke).toHaveBeenCalledExactlyOnceWith(
      props.apiVersion, props.kind, props.resourceName, 'provider-custom-action', undefined, { signal: expect.any(AbortSignal) },
    );
    await settle();
  });

  it('opens a reviewed URL result safely without invoking again', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null);
    const invoke = vi.spyOn(resources, 'invokeAction').mockResolvedValue({ url: 'https://console.example/session', message: 'Console opened' });
    await openAction({ name: 'console', label: 'Open console' });
    expect(open).not.toHaveBeenCalled();
    expect(invoke).not.toHaveBeenCalled();
    button('Run Open console').click();
    await settle();
    expect(open).toHaveBeenCalledExactlyOnceWith('https://console.example/session', '_blank', 'noopener,noreferrer');
    expect(target.textContent).toContain('Console opened');
    expect(invoke).toHaveBeenCalledTimes(1);
  });

  it('downloads only after explicit review submission and retains filename and content handling', async () => {
    const createURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:runtime-download');
    const revokeURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {});
    const downloads: { href: string; filename: string }[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      downloads.push({ href: this.href, filename: this.download });
    });
    const invoke = vi.spyOn(resources, 'invokeAction').mockResolvedValue({ downloadContent: 'sensitive credentials', downloadFilename: 'cluster.yaml' });
    await openAction({ name: 'get-kubeconfig', label: 'Download credentials' });
    expect(downloads).toEqual([]);
    expect(invoke).not.toHaveBeenCalled();
    button('Run Download credentials').click();
    await settle();
    expect(downloads).toEqual([{ href: 'blob:runtime-download', filename: 'cluster.yaml' }]);
    expect(createURL).toHaveBeenCalledExactlyOnceWith(expect.any(Blob));
    expect(revokeURL).toHaveBeenCalledExactlyOnceWith('blob:runtime-download');
    expect(target.textContent).toContain('Downloaded cluster.yaml');
    expect(invoke).toHaveBeenCalledTimes(1);
  });

  it.each([false, true])('does not deliver a prior session result or expire a replacement session (unauthorized=%s)', async (unauthorized) => {
    const pending = Promise.withResolvers<InvokeActionResponse>();
    const open = vi.spyOn(window, 'open').mockReturnValue(null);
    const invoke = vi.spyOn(resources, 'invokeAction').mockReturnValue(pending.promise);
    await openAction({ name: 'console', label: 'Open console' });
    button('Run Open console').click();
    auth.set({ kind: 'signed-in', me: { role: 'admin', userId: 'replacement', sessionId: 'replacement-session' } });
    const replacement = get(auth);
    if (unauthorized) pending.reject(new UnauthorizedError('old session expired'));
    else pending.resolve({ url: 'https://console.example/private-session', message: 'Private prior-session result' });
    await settle();
    expect(open).not.toHaveBeenCalled();
    expect(target.textContent).not.toContain('Private prior-session result');
    expect(get(auth)).toBe(replacement);
    expect(invoke).toHaveBeenCalledTimes(1);
  });

  it('does not retry an uncertain action outcome on observation refresh or old form submission', async () => {
    const invoke = vi.spyOn(resources, 'invokeAction').mockRejectedValue(new Error('Connection lost after submission'));
    const form = await openAction({ name: 'provider-task', label: 'Run provider task' });
    button('Run Run provider task').click();
    await settle();
    expect(target.textContent).toContain('Connection lost after submission');
    expect(invoke).toHaveBeenCalledTimes(1);
    notify();
    submit(form);
    await settle();
    expect(invoke).toHaveBeenCalledTimes(1);
    expect(target.querySelector('form.runtime-action-review')).toBeNull();
  });
});
