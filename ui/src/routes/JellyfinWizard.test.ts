import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { mount, tick, unmount } from 'svelte';
import { get } from 'svelte/store';
import JellyfinWizard from './JellyfinWizard.svelte';
import { discovery, inventoryApi, operations, ping, resources, schemas, session, ApiError, UnauthorizedError, type DiscoveryContext, type ListSavedResourcesResponse } from '../lib/api';
import type * as ControllerAPI from '../lib/api';
import { auth } from '../lib/auth';
import { recordAuthority, resetAuthority } from '../lib/authority';
import { REVIEW_KEY } from '../lib/deploymentReview';

// DeploymentReview captures these functions at module initialization.
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
  vi.mocked(resources.get).mockReset().mockRejectedValue(new Error('No live observation should be requested while choosing a saved name'));
  vi.mocked(resources.apply).mockReset().mockRejectedValue(new Error('Unexpected Apply'));
  vi.mocked(schemas.validate).mockReset().mockResolvedValue({});
  vi.mocked(resources.dryRunApply).mockReset().mockResolvedValue({ operation: 'create', summary: 'Create reviewed resource' });
  vi.mocked(operations.get).mockReset().mockRejectedValue(new Error('Unexpected operation lookup'));
  vi.mocked(operations.byRequest).mockReset().mockRejectedValue(new Error('Unexpected request lookup'));
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
  return node.querySelector<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>('input,select,textarea')!;
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

async function prepareVMReview() {
  vi.mocked(resources.get).mockRejectedValue(new ApiError(404, 'not found'));
  await open(); check('Provision a new VM'); await tick();
  const fields = {
    'VM name': 'gpu', 'Proxmox node': 'pve1', 'Ubuntu 24.04 template VMID': '9000',
    'VM disk and EFI storage': 'local-lvm', 'Network bridge': 'vmbr0',
    'Verified PCI/GPU resource mapping': 'gpu', 'Guest SSH public key': 'ssh-ed25519 fixture',
    'Controller-side SSH private key file': '/keys/guest', 'Official pinned image': 'jellyfin/jellyfin@sha256:' + 'a'.repeat(64),
    'NAS hostname/address': 'nas.example.invalid', 'SMB share': 'media', 'SMB credentials secret key': 'nas',
    'Admin username': 'admin', 'Admin password secret key': 'admin', 'Viewer username': 'viewer', 'Viewer password secret key': 'viewer',
  };
  for (const [label, value] of Object.entries(fields)) {
    const field = control(label); field.value = value; field.dispatchEvent(new Event('input', { bubbles: true }));
  }
  check('This is a dedicated'); check('I will verify'); await tick();
  host.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle();
  await waitForReview();
}
async function waitForReview() {
  await tick();
  await vi.waitFor(() => expect(button('Refresh preview').disabled).toBe(false));
}

describe('visible blocked deployment diagnostics', () => {
  it('shows controller field paths once, preserves other diagnostics, and blocks Apply without consent prompts', async () => {
    vi.mocked(resources.dryRunApply).mockResolvedValueOnce({
      operation: 'create', summary: 'invalid guest declaration',
      fieldErrors: [{ path: 'spec.memory.size', code: 'reference_field_unavailable', message: 'Unsupported guest capacity' }],
      validationErrors: ['spec.memory.size: Unsupported guest capacity', 'Selected provider is unavailable'],
    });
    await prepareVMReview();
    const list = host.querySelector('[aria-label="Preview validation errors"]')!;
    expect(list.textContent).toContain('spec.memory.size');
    expect(list.textContent).toContain('reference_field_unavailable');
    expect(list.textContent?.match(/Unsupported guest capacity/g)).toHaveLength(1);
    expect(list.textContent).toContain('Selected provider is unavailable');
    expect(button('Apply reviewed step').disabled).toBe(true);
    expect(host.querySelectorAll('input[type="checkbox"]')).toHaveLength(0);
    expect(resources.apply).not.toHaveBeenCalled();
    expect(JSON.parse(window.sessionStorage.getItem(REVIEW_KEY)!).steps[0].attempt).toBeUndefined();
  });

  it('shows schema failures before provider preview without making them saved recovery evidence', async () => {
    vi.mocked(schemas.validate).mockResolvedValueOnce({ errors: ['spec.template: template VMID is invalid'] });
    await prepareVMReview();
    expect(host.querySelector('[aria-label="Preview validation errors"]')?.textContent).toContain('spec.template');
    expect(resources.dryRunApply).not.toHaveBeenCalled();
    expect(button('Apply reviewed step').disabled).toBe(true);
    expect(window.sessionStorage.getItem(REVIEW_KEY)).not.toContain('template VMID is invalid');
    expect(resources.apply).not.toHaveBeenCalled();
  });

  it('replaces failed diagnostics after repair but requires new consent before Apply', async () => {
    vi.mocked(resources.dryRunApply).mockResolvedValueOnce({ validationErrors: ['Guest read unavailable'], summary: 'unresolved ref' });
    await prepareVMReview();
    expect(host.querySelector('[aria-label="Preview validation errors"]')).not.toBeNull();
    button('Refresh preview').click(); await waitForReview();
    expect(host.querySelector('[aria-label="Preview validation errors"]')).toBeNull();
    expect(button('Apply reviewed step').disabled).toBe(true);
    check('I confirm this dedicated'); check('I verified this guest'); await tick();
    expect(button('Apply reviewed step').disabled).toBe(false);
    expect(resources.apply).not.toHaveBeenCalled();
  });

  it('surfaces a field-only submission-time failure without sending Apply or saving a new request key', async () => {
    await prepareVMReview();
    expect(button('Apply reviewed step').disabled).toBe(false);
    vi.mocked(resources.dryRunApply).mockResolvedValueOnce({ operation: 'create', fieldErrors: [{ path: 'spec.target.host', message: 'Guest unavailable after review' }] });
    button('Apply reviewed step').click(); await waitForReview();
    expect(host.querySelector('[aria-label="Preview validation errors"]')?.textContent).toContain('Guest unavailable after review');
    expect(button('Apply reviewed step').disabled).toBe(true);
    expect(resources.apply).not.toHaveBeenCalled();
    expect(JSON.parse(window.sessionStorage.getItem(REVIEW_KEY)!).steps[0].attempt).toBeUndefined();
  });
});

describe('recovered application readiness evidence', () => {
  it('keeps unknown and optional evidence neutral and blocks completion after a successful Apply with aggregate false', async () => {
    const resource: ControllerAPI.Resource = {
      apiVersion: 'apps.openctl.io/v1', kind: 'Jellyfin', metadata: { name: 'jellyfin' }, spec: {},
    };
    window.sessionStorage.setItem(REVIEW_KEY, JSON.stringify({
      version: 1, binding: { controllerId: 'controller', userId: 'operator' }, cursor: 0,
      steps: [{ title: 'Install and configure Jellyfin', resource, createOnly: true,
        attempt: { requestId: 'saved-request', operationId: 'saved-operation', createOnly: true, allowDestructive: false, iKnowThisBreaksTheCluster: false } }],
    }));
    vi.mocked(operations.get).mockResolvedValue({
      id: 'saved-operation', type: 'apply', requestId: 'saved-request', status: 'succeeded',
      apiVersion: resource.apiVersion, kind: resource.kind, resourceName: resource.metadata.name,
      manifestJson: JSON.stringify(resource),
    });
    vi.mocked(resources.get).mockResolvedValue({ applied: resource, resource: { ...resource, status: {
      ready: false, checks: {
        application: { state: 'ready', message: '<img src=x onerror=alert(1)>' },
        http: { state: 'not_ready', message: 'Attempt failed' },
        gpu: { state: 'unknown', message: 'Skipped without SSH' },
        publicEndpoint: { state: 'not_configured' },
        future: { state: 'future' }, obsolete: { ready: true }, malformed: [],
      },
    } } });
    await open();
    button('Resume saved review').click(); await settle();
    const checks = [...host.querySelectorAll('.health li')];
    for (const [name, label, tone] of [
      ['application', 'Ready', 'good'], ['http', 'Not ready', 'warn'],
      ['gpu', 'Unknown', 'unknown'], ['publicEndpoint', 'Not configured', 'unknown'],
      ['future', 'Unknown', 'unknown'], ['obsolete', 'Unknown', 'unknown'], ['malformed', 'Unknown', 'unknown'],
    ]) {
      const check = checks.find((node) => node.querySelector('strong')?.textContent === `${name}: ${label}`);
      expect(check).toBeDefined();
      expect(check?.classList.contains(`health-${tone}`)).toBe(true);
    }
    expect(host.textContent).toContain('<img src=x onerror=alert(1)>');
    expect(host.querySelector('img')).toBeNull();
    expect(button('Refresh health').disabled).toBe(false);
    expect(host.textContent).not.toContain('Application readiness checks passed');
    expect([...host.querySelectorAll('button')].some((node) => node.textContent === 'Preview next resource')).toBe(false);
    expect(resources.apply).not.toHaveBeenCalled();
  });
});
