import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { mount, tick, unmount } from 'svelte';
import { get } from 'svelte/store';
import { strFromU8, unzipSync } from 'fflate';
import { parse } from 'yaml';
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
  it.each([false, true, 'true', undefined])('keeps failed/unknown checks blocking completion despite aggregate %j or a ready phase', async (ready) => {
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
      phase: 'ready', ready, checks: {
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

describe('blocking deployment prerequisites', () => {
  const app: ControllerAPI.Resource = { apiVersion: 'apps.openctl.io/v1', kind: 'Jellyfin', metadata: { name: 'media' }, spec: {} };
  function saveAtPublication() {
    window.sessionStorage.setItem(REVIEW_KEY, JSON.stringify({
      version: 1, binding: { controllerId: 'controller', userId: 'operator' }, cursor: 1,
      steps: [
        { title: 'Install Jellyfin', resource: app, createOnly: true, attempt: { requestId: 'accepted-app', operationId: 'app-op', createOnly: true, allowDestructive: false, iKnowThisBreaksTheCluster: false } },
        { title: 'Route public hostname', resource: { apiVersion: 'cloudflare.openctl.io/v1', kind: 'TunnelRoute', metadata: { name: 'media-route' }, spec: {} }, createOnly: true },
        { title: 'Publish DNS', resource: { apiVersion: 'cloudflare.openctl.io/v1', kind: 'DNSRecord', metadata: { name: 'media-dns' }, spec: {} }, createOnly: true },
        { title: 'Verify public health', resource: { ...app, spec: { publicEndpoint: 'https://media.example.invalid/System/Info/Public' } }, createOnly: false },
      ],
    }));
    vi.mocked(operations.get).mockResolvedValue({ id: 'app-op', type: 'apply', requestId: 'accepted-app', status: 'succeeded', apiVersion: app.apiVersion, kind: app.kind, resourceName: app.metadata.name, manifestJson: JSON.stringify(app) });
  }

  it('shows prerequisite checks and inspection without publication, then rechecks repair with fresh consent', async () => {
    saveAtPublication();
    let healthy = false;
    vi.mocked(resources.get).mockImplementation(async (av) => {
      if (av !== app.apiVersion) throw new ApiError(404, 'not found');
      return { applied: app, resource: { ...app, status: {
        ready: healthy, message: healthy ? 'Repaired' : 'Operator-controlled reboot required',
        checks: { gpu: { state: healthy ? 'ready' : 'not_ready', message: '<img src=x onerror=alert(1)>' }, http: { state: healthy ? 'ready' : 'unknown' }, publicEndpoint: { state: 'not_configured' } },
      } } };
    });
    await open(); button('Resume saved review').click(); await settle();
    const blocker = host.querySelector('[aria-label="Blocking deployment resource"]')!;
    expect(blocker.textContent).toContain('Jellyfin/media');
    expect(blocker.textContent).toContain('apps.openctl.io/v1');
    expect(blocker.textContent).toContain('gpu: Not ready');
    expect(blocker.textContent).toContain('http: Unknown');
    expect(blocker.textContent).toContain('publicEndpoint: Not configured');
    expect(blocker.textContent).toContain('Operator-controlled reboot required');
    expect(blocker.querySelector('img')).toBeNull();
    expect(blocker.querySelector('a')?.getAttribute('href')).toBe('#/k/apps.openctl.io/v1/Jellyfin/media');
    expect(button('Apply reviewed step').disabled).toBe(true);
    expect(resources.apply).not.toHaveBeenCalled();
    expect(window.sessionStorage.getItem(REVIEW_KEY)).not.toContain('Operator-controlled reboot');
    expect(window.sessionStorage.getItem(REVIEW_KEY)).not.toContain('<img');

    healthy = true;
    button('Recheck prerequisites').click(); await settle();
    await vi.waitFor(() => expect(button('Refresh preview').disabled).toBe(false));
    expect(host.querySelector('[aria-label="Blocking deployment resource"]')).toBeNull();
    expect(button('Apply reviewed step').disabled).toBe(true);
    expect((control('I confirm this dedicated guest') as HTMLInputElement).checked).toBe(false);
    expect((control('I verified this guest') as HTMLInputElement).checked).toBe(false);
    expect((control('I freshly authorize') as HTMLInputElement).checked).toBe(false);
    expect(JSON.parse(window.sessionStorage.getItem(REVIEW_KEY)!).steps[1].attempt).toBeUndefined();
    expect(resources.apply).not.toHaveBeenCalled();

    await unmount(component!); component = undefined;
    healthy = false;
    await open();
    expect(host.querySelector('[aria-label="Blocking deployment resource"]')).toBeNull();
    expect(host.textContent).not.toContain('Operator-controlled reboot required');
    button('Resume saved review').click(); await settle();
    expect(host.querySelector('[aria-label="Blocking deployment resource"]')?.textContent).toContain('Operator-controlled reboot required');
    expect(resources.apply).not.toHaveBeenCalled();
  });

  it('identifies an unreadable prerequisite without forwarding private provider errors or inventing checks', async () => {
    saveAtPublication();
    vi.mocked(resources.get).mockRejectedValue(new ApiError(503, 'PRIVATE-PROVIDER https://user:password@private.invalid/'));
    await open(); button('Resume saved review').click(); await settle();
    const blocker = host.querySelector('[aria-label="Blocking deployment resource"]')!;
    expect(blocker.textContent).toContain('Jellyfin/media');
    expect(blocker.textContent).toContain('observation_unavailable');
    expect(host.textContent).not.toContain('PRIVATE-PROVIDER');
    expect(host.textContent).not.toContain('private.invalid');
    expect(blocker.querySelector('[aria-label="Dependency readiness checks"]')).toBeNull();
    expect(button('Apply reviewed step').disabled).toBe(true);
    expect(resources.apply).not.toHaveBeenCalled();
  });
});

describe('staged manifest downloads', () => {
  function captureDownloads() {
    const files: { filename: string; blob: Blob }[] = [];
    let blob: Blob;
    vi.spyOn(URL, 'createObjectURL').mockImplementation((value) => { blob = value as Blob; return 'blob:export-fixture'; });
    vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {});
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      files.push({ filename: this.download, blob });
    });
    return files;
  }

  it('downloads CLI-sized manifests from a blocked preview without submitting or changing recovery state', async () => {
    vi.mocked(resources.dryRunApply).mockResolvedValueOnce({ fieldErrors: [{ path: 'spec.template', message: 'Repair template selection' }] });
    await prepareVMReview();
    const before = window.sessionStorage.getItem(REVIEW_KEY);
    const previews = vi.mocked(resources.dryRunApply).mock.calls.length;
    const reads = vi.mocked(resources.get).mock.calls.length;
    const files = captureDownloads();
    button('Download staged manifests (.zip)').click();
    button('Download current stage YAML').click();
    expect(files.map((file) => file.filename)).toEqual(['jellyfin-deployment.zip', '01-virtualmachine.yaml']);
    const archive = unzipSync(new Uint8Array(await files[0].blob.arrayBuffer()));
    expect(Object.keys(archive)).toEqual(['01-virtualmachine.yaml', '02-jellyfin.yaml']);
    expect(parse(strFromU8(archive['02-jellyfin.yaml'])).spec.nas.credentials).toEqual({ $secret: { provider: 'file', key: 'nas' } });
    const vm = parse(await files[1].blob.text());
    expect(vm.kind).toBe('VirtualMachine');
    expect(vm.metadata.name).toBe('gpu');
    expect(files[0].blob.type).toBe('application/zip');
    expect(files[1].blob.type).toBe('application/yaml');
    expect(window.sessionStorage.getItem(REVIEW_KEY)).toBe(before);
    expect(vi.mocked(resources.dryRunApply).mock.calls.length).toBe(previews);
    expect(vi.mocked(resources.get).mock.calls.length).toBe(reads);
    expect(resources.apply).not.toHaveBeenCalled();
    expect(button('Apply reviewed step').disabled).toBe(true);
  });

  it('exports saved declarations before recovery and the selected later stage after a blocked resume without leaking accepted identities', async () => {
    await prepareVMReview();
    const saved = JSON.parse(window.sessionStorage.getItem(REVIEW_KEY)!);
    saved.cursor = 1;
    saved.steps[0].attempt = { requestId: 'PRIVATE-ACCEPTED-REQUEST', operationId: 'PRIVATE-OPERATION', createOnly: true, allowDestructive: false, iKnowThisBreaksTheCluster: false };
    const before = JSON.stringify(saved);
    window.sessionStorage.setItem(REVIEW_KEY, before);
    await unmount(component!); component = undefined;
    await open();
    const previews = vi.mocked(resources.dryRunApply).mock.calls.length;
    const files = captureDownloads();
    button('Download saved manifests (.zip)').click();
    const archive = unzipSync(new Uint8Array(await files[0].blob.arrayBuffer()));
    expect(parse(strFromU8(archive['02-jellyfin.yaml'])).metadata.name).toBe('jellyfin');
    expect(Object.values(archive).map((bytes) => strFromU8(bytes)).join('')).not.toContain('PRIVATE-');
    expect(window.sessionStorage.getItem(REVIEW_KEY)).toBe(before);
    expect(operations.get).not.toHaveBeenCalled();
    expect(vi.mocked(resources.dryRunApply).mock.calls.length).toBe(previews);
    button('Resume saved review').click(); await settle();
    button('Download current stage YAML').click();
    expect(files[1].filename).toBe('02-jellyfin.yaml');
    const app = parse(await files[1].blob.text());
    expect(app.kind).toBe('Jellyfin');
    expect(app.spec.target.host.$ref.name).toBe('gpu');
    expect(JSON.stringify(app)).not.toContain('PRIVATE-');
    expect(resources.apply).not.toHaveBeenCalled();
    expect(button('Apply reviewed step').disabled).toBe(true);
  });
});
