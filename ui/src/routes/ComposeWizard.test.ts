import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { mount, tick, unmount } from 'svelte';
import { parse } from 'yaml';
import ComposeWizard from './ComposeWizard.svelte';
import { ApiError, discovery, inventoryApi, operations, ping, resources, schemas, session, templates, type GetResourceResponse, type Operation, type Resource } from '../lib/api';
import type * as ControllerAPI from '../lib/api';
import { auth } from '../lib/auth';
import { recordAuthority, resetAuthority } from '../lib/authority';
import { APP_API, VM_API, REVIEW_KEYS } from '../lib/deployment';
import { createSavedReview } from '../lib/deploymentReview';

vi.mock('../lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof ControllerAPI>();
  return {
    ...actual,
    resources: { ...actual.resources, get: vi.fn(), apply: vi.fn(), dryRunApply: vi.fn() },
    schemas: { ...actual.schemas, validate: vi.fn() },
    operations: { ...actual.operations, get: vi.fn(), byRequest: vi.fn() },
  };
});
let component: Record<string, unknown> | undefined;
let host: HTMLDivElement;
let live: Map<string, GetResourceResponse>;
let accepted: Map<string, Operation>;
const binding = { controllerId: 'controller', userId: 'operator' };
const guest: Resource = { apiVersion: VM_API, kind: 'VirtualMachine', metadata: { name: 'docker' }, spec: {} };
const address = { state: 'running', ip: '192.0.2.8', ipObservation: { state: 'available', code: 'guest_ip_available', message: 'Unique guest address observed' } };
function app(params: Record<string, unknown>): Resource {
  return {
    apiVersion: APP_API, kind: 'ComposeApplication', metadata: { name: String(params.name), annotations: { 'openctl.io/template': 'compose-application' } },
    spec: { target: { host: { $ref: { apiVersion: VM_API, kind: 'VirtualMachine', name: params.vmName, field: 'status.ip' } }, user: params.sshUser, privateKeyFile: params.privateKeyFile, knownHostsFile: params.knownHostsFile, sudo: true }, compose: 'services:\n  welcome:\n    image: pinned\n', probe: { url: `http://127.0.0.1:${params.port}/` } },
  };
}
beforeEach(() => {
  host = document.createElement('div'); document.body.append(host);
  window.sessionStorage.clear(); live = new Map(); accepted = new Map();
  auth.set({ kind: 'signed-in', me: { userId: 'operator', principalId: 'operator', sessionId: 'session', role: 'admin' } }); recordAuthority('mirror');
  vi.spyOn(ping, 'ping').mockResolvedValue({ echo: '', serverVersion: 'test', supportsIdempotentApply: true, controllerId: 'controller' });
  vi.spyOn(session, 'whoami').mockResolvedValue({ userId: 'operator', principalId: 'operator', sessionId: 'session', role: 'admin' });
  vi.spyOn(discovery, 'contexts').mockResolvedValue({ contexts: [{ name: 'lab', defaultContext: true }] });
  vi.spyOn(inventoryApi, 'list').mockResolvedValue({ resources: [guest] });
  vi.spyOn(resources, 'list').mockRejectedValue(new Error('Whole-lab scans are forbidden'));
  vi.spyOn(templates, 'render').mockImplementation(async (name, params) => ({ resource: name === 'docker-host-vm' ? { ...guest, spec: { context: params.context, cpu: { cores: params.cpuCores }, memory: { size: params.memoryMiB }, diskGiB: params.diskGiB } } : app(params) }));
  vi.mocked(resources.get).mockReset().mockImplementation(async (_api, _kind, name) => { const result = live.get(name); if (!result) throw new ApiError(404, 'not found'); return result; });
  vi.mocked(schemas.validate).mockReset().mockResolvedValue({});
  vi.mocked(resources.dryRunApply).mockReset().mockResolvedValue({ operation: 'create', summary: 'Review resource' });
  vi.mocked(operations.get).mockReset().mockImplementation(async (id) => { const operation = accepted.get(id); if (!operation) throw new ApiError(404, 'not found'); return operation; });
  vi.mocked(operations.byRequest).mockReset().mockImplementation(async (id) => { const operation = [...accepted.values()].find((item) => item.requestId === id); if (!operation) throw new ApiError(404, 'not found'); return operation; });
  vi.mocked(resources.apply).mockReset().mockImplementation(async (request) => {
    const resource = request.resource as Resource; const id = `op-${request.requestId}`;
    accepted.set(id, { id, requestId: request.requestId, status: 'succeeded', type: 'apply', apiVersion: resource.apiVersion, kind: resource.kind, resourceName: resource.metadata.name, manifestJson: JSON.stringify(resource) });
    live.set(resource.metadata.name, { applied: resource, resource: { ...resource, status: resource.kind === 'VirtualMachine' ? address : { ready: true, url: 'http://127.0.0.1:18080/', checks: { docker: { state: 'ready' }, service: { state: 'ready' } } } } });
    return { operationId: id, requestId: request.requestId };
  });
  vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('Unexpected network request')));
});
afterEach(async () => {
  if (component) await unmount(component); component = undefined; host.remove(); window.sessionStorage.clear();
  resetAuthority(); auth.set({ kind: 'unknown' }); vi.restoreAllMocks(); vi.unstubAllGlobals();
});
async function settle() { for (let i = 0; i < 14; i++) { await Promise.resolve(); await tick(); } }
async function open() { component = mount(ComposeWizard, { target: host }); await settle(); }
function button(label: string) { const result = [...host.querySelectorAll<HTMLButtonElement>('button')].find((node) => node.textContent?.trim() === label); if (!result) throw new Error(`Missing button ${label}; alert: ${host.querySelector('[role="alert"]')?.textContent ?? 'none'}`); return result; }
function control(label: string) { const result = [...host.querySelectorAll('label')].find((node) => node.textContent?.trim().startsWith(label))?.querySelector<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>('input,select,textarea'); if (!result) throw new Error(`Missing field ${label}`); return result; }
async function fill(label: string, value: string) {
  const node = control(label);
  node.value = value;
  node.dispatchEvent(new Event(node.tagName === 'SELECT' ? 'change' : 'input', { bubbles: true }));
  await tick();
}
async function check(label: string, value = true) { const node = control(label) as HTMLInputElement; node.checked = value; node.dispatchEvent(new Event('change', { bubbles: true })); await tick(); }
async function prepare(existing = false) {
  await open();
  if (existing) { await check('Provision a new VM', false); await fill('Saved managed Docker guest', 'docker'); }
  else {
    await fill('VM name', 'docker'); await fill('Proxmox context', 'lab'); await fill('Proxmox node', 'pve1');
    await fill('Ubuntu 24.04 amd64 template VMID', '9000'); await fill('Guest SSH public key', 'ssh-ed25519 fixture');
  }
  await fill('Controller-host SSH private key file', '/controller/ssh/id'); await fill('Controller-host verified known_hosts file', '/controller/ssh/new-known-hosts');
  host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle();
  await vi.waitFor(() => expect(button('Refresh preview').disabled).toBe(false));
}
async function authorize(appStage: boolean) {
  await check('I authorize this reviewed resource'); if (appStage) await check('I verified the guest SSH host key');
  button('Apply reviewed step').click(); await settle();
}

describe('first application operator path', () => {
  it('loads contexts and saved names independently without live scans or implicitly selecting a new VM context', async () => {
    vi.mocked(discovery.contexts).mockRejectedValueOnce(new Error('Context registry unavailable'));
    await open(); await check('Provision a new VM', false);
    expect(control('Saved managed Docker guest').disabled).toBe(false);
    expect([...((control('Saved managed Docker guest') as HTMLSelectElement).options)].map((option) => option.value)).toContain('docker');
    expect(resources.list).not.toHaveBeenCalled(); expect(resources.get).not.toHaveBeenCalled(); expect(resources.apply).not.toHaveBeenCalled();
    await unmount(component!); component = undefined; await open();
    expect(control('Proxmox context').value).toBe('');
  });

  it('reviews two controller-returned stages, obtains no implicit trust, and reaches only a guest-local ready summary', async () => {
    await prepare();
    expect(templates.render).toHaveBeenCalledTimes(2);
    const vm = parse(host.querySelector('pre')!.textContent!);
    expect(vm.spec).toMatchObject({ context: 'lab', cpu: { cores: 2 }, memory: { size: 2048 }, diskGiB: 16 });
    expect(control('I authorize this reviewed resource')).toBeDefined();
    expect(host.textContent).toContain('ssh-host-keys'); expect(host.textContent).toContain("--download '/controller/ssh/new-known-hosts'");
    expect(host.querySelector('a[target="_blank"]')?.getAttribute('rel')).toContain('noopener');
    expect(host.querySelector('input[type="checkbox"]')?.parentElement?.textContent).not.toContain('I verified');
    await authorize(false); await vi.waitFor(() => expect(button('Preview next resource').disabled).toBe(false));
    button('Preview next resource').click(); await settle();
    expect(button('Apply reviewed step').disabled).toBe(true);
    expect(parse(host.querySelector('pre')!.textContent!).spec.target.host.$ref.name).toBe('docker');
    await authorize(true); await vi.waitFor(() => expect(host.textContent).toContain('Application readiness checks passed'));
    expect(resources.apply).toHaveBeenCalledTimes(2);
    expect(vi.mocked(resources.apply).mock.calls.map(([request]) => [request.resource.kind, request.createOnly])).toEqual([['VirtualMachine', true], ['ComposeApplication', true]]);
    expect(host.querySelector('a[href^="http://127.0.0.1"]')).toBeNull();
    expect(host.textContent).toContain('StrictHostKeyChecking=yes'); expect(host.textContent).toContain('guest-local');
    expect(resources.list).not.toHaveBeenCalled();
  });

  it('offers an existing guest without rendering or applying any VM upgrade, resize or public stage', async () => {
    live.set('docker', { applied: guest, resource: { ...guest, status: address } }); await prepare(true);
    expect(templates.render).toHaveBeenCalledTimes(1); expect(vi.mocked(templates.render).mock.calls[0][0]).toBe('compose-application');
    expect(host.querySelectorAll('ol li')).toHaveLength(1);
    expect(JSON.parse(window.sessionStorage.getItem(REVIEW_KEYS.compose)!).steps[0].resource.spec.target).toMatchObject({ privateKeyFile: '/controller/ssh/id', knownHostsFile: '/controller/ssh/new-known-hosts', host: { $ref: { name: 'docker' } } });
    expect(resources.apply).not.toHaveBeenCalled();
  });

  it('keeps unknown address and strict-SSH/bootstrap preflight failures visible without Apply or automatic repair', async () => {
    live.set('docker', { applied: guest, resource: { ...guest, status: { state: 'running', ip: '192.0.2.8' } } }); await prepare(true);
    expect(host.querySelector('[aria-label="Blocking deployment resource"]')?.textContent).toContain('address_unavailable');
    expect(resources.dryRunApply).not.toHaveBeenCalled();
    live.set('docker', { applied: guest, resource: { ...guest, status: address } });
    vi.mocked(resources.dryRunApply).mockResolvedValueOnce({ fieldErrors: [{ path: 'spec.target.knownHostsFile', message: 'Verified SSH trust or supported Compose version unavailable' }] });
    button('Recheck prerequisites').click(); await settle();
    expect(host.querySelector('[aria-label="Preview validation errors"]')?.textContent).toContain('Verified SSH trust');
    expect(button('Apply reviewed step').disabled).toBe(true); expect(resources.apply).not.toHaveBeenCalled();
    expect(JSON.parse(window.sessionStorage.getItem(REVIEW_KEYS.compose)!).steps[0].attempt).toBeUndefined();
  });

  it('isolates Jellyfin recovery and never duplicates accepted Apply when reloading Compose', async () => {
    const jellyfin = createSavedReview('jellyfin', binding, [{ title: 'Jellyfin', createOnly: true, resource: { apiVersion: APP_API, kind: 'Jellyfin', metadata: { name: 'media' }, spec: {} } }]);
    window.sessionStorage.setItem(REVIEW_KEYS.jellyfin, JSON.stringify(jellyfin));
    live.set('docker', { applied: guest, resource: { ...guest, status: address } }); await prepare(true); await authorize(true);
    await vi.waitFor(() => expect(host.textContent).toContain('Application readiness checks passed'));
    const original = window.sessionStorage.getItem(REVIEW_KEYS.compose);
    await unmount(component!); component = undefined; await open();
    expect(host.textContent).toContain('Saved application review'); expect(resources.apply).toHaveBeenCalledTimes(1);
    expect(window.sessionStorage.getItem(REVIEW_KEYS.jellyfin)).toBe(JSON.stringify(jellyfin));
    button('Resume saved review').click(); await settle();
    expect(resources.apply).toHaveBeenCalledTimes(1); expect(window.sessionStorage.getItem(REVIEW_KEYS.compose)).toBe(original);
  });

  it('rejects stale actors during fresh admission and preserves the unsubmitted returned marker manifest', async () => {
    live.set('docker', { applied: guest, resource: { ...guest, status: address } }); await prepare(true);
    const before = JSON.parse(window.sessionStorage.getItem(REVIEW_KEYS.compose)!);
    vi.mocked(resources.dryRunApply).mockImplementationOnce(async () => {
      vi.mocked(session.whoami).mockResolvedValue({ userId: 'other', principalId: 'other', sessionId: 'other', role: 'admin' });
      return { operation: 'create', summary: 'Review resource' };
    });
    await authorize(true);
    expect(host.querySelector('[role="alert"]')?.textContent).toContain('signed-in user changed');
    expect(resources.apply).not.toHaveBeenCalled();
    const after = JSON.parse(window.sessionStorage.getItem(REVIEW_KEYS.compose)!);
    expect(after.steps[0].attempt).toBeUndefined(); expect(after.steps[0].resource).toEqual(before.steps[0].resource);
    expect(after.steps[0].resource.spec.target.host.$ref).toEqual({ apiVersion: VM_API, kind: 'VirtualMachine', name: 'docker', field: 'status.ip' });
  });
});
