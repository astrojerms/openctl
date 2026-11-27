import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { mount, tick, unmount } from 'svelte';
import { get } from 'svelte/store';
import Discovery from './Discovery.svelte';
import { discovery, ownership, UnauthorizedError, type DiscoveredResource, type MigrationBundle } from '../lib/api';
import { auth } from '../lib/auth';
import { recordAuthority, resetAuthority } from '../lib/authority';
import * as router from '../lib/router';

let component: Record<string, unknown> | undefined;
let host: HTMLDivElement;
function discoveredVM(name = 'existing-vm'): DiscoveredResource {
  return {
    context: 'working', node: 'pve1', vmid: '101', identity: 'immutable-id', adoptable: true,
    resource: { apiVersion: 'proxmox.openctl.io/v1', kind: 'VirtualMachine', metadata: { name, annotations: { note: 'preserve reviewed metadata' } }, spec: { context: 'working', node: 'pve1', vmid: 101 } },
  };
}
beforeEach(() => {
  host = document.createElement('div'); document.body.append(host);
  auth.set({ kind: 'signed-in', me: { sessionId: 'reader-session', principalId: 'reader', role: 'viewer' } });
  recordAuthority('mirror');
  vi.spyOn(router, 'navigate').mockImplementation(() => {});
  vi.spyOn(discovery, 'contexts').mockResolvedValue({ contexts: [
    { name: 'private-ca', endpoint: 'https://private.example:8006', defaultContext: true },
    { name: 'working', endpoint: 'https://working.example:8006' },
  ] });
  vi.spyOn(discovery, 'test').mockImplementation(async (context) => context === 'private-ca'
    // Protobuf JSON omits false; failed responses must not require connected:false.
    ? { failureCode: 'tls_untrusted', message: 'Install a verified CA bundle on the controller host, set caFile and restart.' }
    : { connected: true, message: 'Authenticated read-only inventory succeeded' });
  vi.spyOn(discovery, 'inventory').mockResolvedValue({ resources: [discoveredVM()] });
  vi.spyOn(ownership, 'adopt').mockRejectedValue(new Error('Unexpected adoption'));
  vi.spyOn(ownership, 'adoptBundle').mockRejectedValue(new Error('Unexpected bundle adoption'));
  vi.spyOn(ownership, 'confirmIdentity').mockRejectedValue(new Error('Unexpected identity confirmation'));
  vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('Unexpected HTTP in isolated discovery test')));
});
afterEach(async () => {
  if (component) await unmount(component); component = undefined;
  host.remove(); resetAuthority(); auth.set({ kind: 'unknown' }); vi.restoreAllMocks(); vi.unstubAllGlobals();
});
async function settle() { for (let i = 0; i < 6; i++) { await Promise.resolve(); await tick(); } }
async function open() { component = mount(Discovery, { target: host }); await settle(); }
function buttons(label: string) {
  return [...host.querySelectorAll<HTMLButtonElement>('button')].filter((node) => node.textContent?.trim() === label);
}

describe('read-only discovery connection diagnostics', () => {
  it('shows the safe failure category and next step without inventing empty inventory or adopting', async () => {
    await open(); buttons('Test connection')[0].click(); await settle();
    const row = host.querySelector('tbody tr')!;
    expect(row.textContent).toContain('tls_untrusted');
    expect(row.textContent).toContain('verified CA bundle');
    expect(row.textContent).not.toContain('0 VMs');
    expect(discovery.inventory).not.toHaveBeenCalled();
    expect(ownership.adopt).not.toHaveBeenCalled();
    expect(ownership.adoptBundle).not.toHaveBeenCalled();
    expect(ownership.confirmIdentity).not.toHaveBeenCalled();
  });

  it('keeps failed context diagnostics while discovering other contexts independently', async () => {
    await open(); buttons('Test connections and discover all VMs')[0].click(); await settle();
    expect(vi.mocked(discovery.inventory).mock.calls.map(([context]) => context)).toEqual(['working']);
    expect(host.textContent).toContain('existing-vm');
    const rows = host.querySelectorAll('article table tbody tr');
    expect(rows[0].textContent).toContain('tls_untrusted');
    expect(rows[0].textContent).not.toContain('0 VMs');
    expect(rows[1].textContent).toContain('1 VMs');
    expect(ownership.adopt).not.toHaveBeenCalled();
  });

  it('clears the previous failure category after an explicit successful retest', async () => {
    await open(); buttons('Test connection')[0].click(); await settle();
    expect(host.textContent).toContain('tls_untrusted');
    vi.mocked(discovery.test).mockResolvedValueOnce({ connected: true, message: 'Read access verified' });
    buttons('Test connection')[0].click(); await settle();
    const row = host.querySelector('tbody tr')!;
    expect(row.textContent).toContain('Read access verified');
    expect(row.textContent).not.toContain('tls_untrusted');
    expect(row.textContent).not.toContain('verified CA bundle');
    expect(discovery.inventory).not.toHaveBeenCalled();
  });
});

async function adminDiscovery() {
  auth.set({ kind: 'signed-in', me: { sessionId: 'admin-session', principalId: 'admin', role: 'admin' } });
  await open(); buttons('Test connections and discover all VMs')[0].click(); await settle();
}
function consent() {
  const checkbox = host.querySelector<HTMLInputElement>('.confirmation input')!;
  checkbox.checked = true; checkbox.dispatchEvent(new Event('change', { bubbles: true }));
}
async function reviewedVM() {
  await adminDiscovery(); buttons('Review')[0].click(); await tick(); consent(); await tick();
}
function migrationBundle(secret = 'private-bundle-secret'): MigrationBundle {
  const vm = discoveredVM();
  return {
    version: 1, controllerId: 'source-controller', manifestDigest: 'a'.repeat(64), identity: vm.identity,
    resource: { ...vm.resource, spec: { ...vm.resource.spec, context: 'source', cloudInit: { password: secret } } },
  };
}
function chooseBundle(name: string, contents: Promise<string>) {
  const input = host.querySelector<HTMLInputElement>('input[type="file"]')!;
  const file = new File([''], name, { type: 'application/json' });
  const read = vi.fn(() => contents);
  Object.defineProperty(file, 'text', { value: read });
  Object.defineProperty(input, 'files', { configurable: true, value: [file] });
  input.dispatchEvent(new Event('change', { bubbles: true }));
  return read;
}

describe('fingerprint-bound adoption review', () => {
  it('submits exactly the reviewed VM fingerprint, placement and desired metadata', async () => {
    vi.mocked(ownership.adopt).mockResolvedValueOnce({ resource: discoveredVM().resource });
    await reviewedVM(); buttons('Adopt existing VM')[0].click(); await settle();
    const [resource, context, identity] = vi.mocked(ownership.adopt).mock.calls[0];
    expect(resource).toEqual(discoveredVM().resource);
    expect(context).toBe('working'); expect(identity).toBe('immutable-id');
    expect(router.navigate).toHaveBeenCalledWith({ name: 'detail', apiVersion: resource.apiVersion, kind: resource.kind, resourceName: resource.metadata.name });
  });

  it('invalidates a rejected review instead of resubmitting an old fingerprint', async () => {
    vi.mocked(ownership.adopt).mockRejectedValueOnce(new Error('VM identity changed; discover and review again'));
    await reviewedVM(); const adopt = buttons('Adopt existing VM')[0]; adopt.click(); await settle();
    expect(host.querySelector('[role="alert"]')).not.toBeNull();
    expect(buttons('Adopt existing VM')).toHaveLength(0);
    expect(buttons('Review')).toHaveLength(0);
    expect(host.querySelector('article table tbody tr:last-child')?.textContent).not.toContain('1 VMs');
    adopt.click(); await settle();
    expect(ownership.adopt).toHaveBeenCalledTimes(1);
    expect(router.navigate).not.toHaveBeenCalled();
  });

  it.each(['role', 'principal', 'authority'] as const)('rejects a cached consent button immediately after %s changes', async (change) => {
    await reviewedVM(); const adopt = buttons('Adopt existing VM')[0];
    if (change === 'role') auth.set({ kind: 'signed-in', me: { sessionId: 'admin-session', principalId: 'admin', role: 'viewer' } });
    if (change === 'principal') auth.set({ kind: 'signed-in', me: { sessionId: 'other-session', principalId: 'other-admin', role: 'admin' } });
    if (change === 'authority') recordAuthority('gitops');
    adopt.click(); await settle();
    expect(ownership.adopt).not.toHaveBeenCalled();
    expect(buttons('Adopt existing VM')).toHaveLength(0);
    expect(buttons('Review')).toHaveLength(0);
  });

  it('invalidates only the tested context and requires rediscovery after a retest', async () => {
    await reviewedVM(); const adopt = buttons('Adopt existing VM')[0];
    vi.mocked(discovery.test).mockResolvedValueOnce({ failureCode: 'timeout', message: 'Connection timed out' });
    buttons('Test connection')[1].click(); adopt.click(); await settle();
    expect(ownership.adopt).not.toHaveBeenCalled();
    expect(buttons('Adopt existing VM')).toHaveLength(0);
    expect(buttons('Review')).toHaveLength(0);
    vi.mocked(discovery.test).mockResolvedValueOnce({ connected: true });
    buttons('Test connection')[1].click(); await settle();
    expect(buttons('Review')).toHaveLength(0);
    expect(discovery.inventory).toHaveBeenCalledTimes(1);
  });

  it('does not discard an independent context review when another connection is tested', async () => {
    vi.mocked(ownership.adopt).mockResolvedValueOnce({ resource: discoveredVM().resource });
    await reviewedVM(); buttons('Test connection')[0].click(); await settle();
    expect(host.querySelector<HTMLInputElement>('.confirmation input')?.checked).toBe(true);
    buttons('Adopt existing VM')[0].click(); await settle();
    expect(ownership.adopt).toHaveBeenCalledTimes(1);
  });

  it('does not merge a previous principal inventory response into a new discovery', async () => {
    const pending = Promise.withResolvers<{ resources: DiscoveredResource[] }>();
    vi.mocked(discovery.inventory).mockReturnValueOnce(pending.promise);
    await adminDiscovery();
    const oldSignal = vi.mocked(discovery.inventory).mock.calls[0][2]?.signal;
    auth.set({ kind: 'signed-in', me: { sessionId: 'new-session', principalId: 'new-admin', role: 'admin' } }); await settle();
    expect(oldSignal?.aborted).toBe(true);
    vi.mocked(discovery.inventory).mockResolvedValueOnce({ resources: [discoveredVM('new-principal-vm')] });
    buttons('Test connections and discover all VMs')[0].click(); await settle();
    pending.resolve({ resources: [discoveredVM('old-private-vm')] }); await settle();
    expect(host.textContent).toContain('new-principal-vm');
    expect(host.textContent).not.toContain('old-private-vm');
  });

  it('ignores a late adoption acknowledgement after teardown and aborts waiting', async () => {
    const pending = Promise.withResolvers<{ resource: DiscoveredResource['resource'] }>();
    vi.mocked(ownership.adopt).mockReturnValueOnce(pending.promise);
    await reviewedVM(); buttons('Adopt existing VM')[0].click(); await tick();
    const signal = vi.mocked(ownership.adopt).mock.calls[0][3]?.signal;
    await unmount(component!); component = undefined;
    expect(signal?.aborted).toBe(true);
    pending.resolve({ resource: discoveredVM().resource }); await settle();
    expect(router.navigate).not.toHaveBeenCalled();
    expect(host.textContent).toBe('');
  });

  it('ignores an old unauthorized response without signing out the new principal', async () => {
    const pending = Promise.withResolvers<{ resource: DiscoveredResource['resource'] }>();
    vi.mocked(ownership.adopt).mockReturnValueOnce(pending.promise);
    await reviewedVM(); buttons('Adopt existing VM')[0].click(); await tick();
    auth.set({ kind: 'signed-in', me: { sessionId: 'new-session', principalId: 'new-admin', role: 'admin' } });
    pending.reject(new UnauthorizedError('old session expired')); await settle();
    const state = get(auth);
    expect(state.kind === 'signed-in' && state.me.principalId).toBe('new-admin');
    expect(router.navigate).not.toHaveBeenCalled();
  });

  it('checks live authority before confirming an already-tracked identity', async () => {
    vi.mocked(discovery.inventory).mockResolvedValueOnce({ resources: [{ ...discoveredVM(), identityConfirmationRequired: true, managed: true, adoptable: false }] });
    await reviewedVM(); const confirm = buttons('Confirm tracked VM identity')[0];
    recordAuthority('gitops'); confirm.click(); await settle();
    expect(ownership.confirmIdentity).not.toHaveBeenCalled();
    expect(ownership.adopt).not.toHaveBeenCalled();
  });
});

describe('private migration bundle lifecycle', () => {
  it('keeps the bundle unchanged and local until explicit destination adoption', async () => {
    // happy-dom does not implement option :checked, which Svelte's select
    // binding needs. Destination selection is exercised in the real browser.
    vi.mocked(discovery.contexts).mockResolvedValueOnce({ contexts: [{ name: 'working', defaultContext: true }] });
    vi.mocked(ownership.adoptBundle).mockResolvedValueOnce({ resource: discoveredVM().resource });
    await adminDiscovery(); const bundle = migrationBundle();
    chooseBundle('source.json', Promise.resolve(JSON.stringify(bundle))); await settle();
    expect(ownership.adoptBundle).not.toHaveBeenCalled();
    consent(); await tick();
    buttons('Adopt bundle at destination')[0].click(); await settle();
    const [submitted, context] = vi.mocked(ownership.adoptBundle).mock.calls[0];
    expect(submitted).toEqual(bundle); expect(context).toBe('working');
    expect(submitted.resource.spec?.context).toBe('source');
  });

  it.each(['clear', 'authority', 'unmount'] as const)('does not restore a late private file after %s', async (change) => {
    await adminDiscovery(); const pending = Promise.withResolvers<string>();
    chooseBundle('private.json', pending.promise); await tick();
    if (change === 'clear') buttons('Clear bundle')[0].click();
    if (change === 'authority') recordAuthority('gitops');
    if (change === 'unmount') { await unmount(component!); component = undefined; }
    pending.resolve(JSON.stringify(migrationBundle())); await settle();
    expect(host.textContent).not.toContain('private-bundle-secret');
    expect(host.textContent).not.toContain('Loaded private.json');
    expect(ownership.adoptBundle).not.toHaveBeenCalled();
  });

  it('clears an already-loaded private bundle on a session change', async () => {
    await adminDiscovery(); chooseBundle('private.json', Promise.resolve(JSON.stringify(migrationBundle()))); await settle();
    expect(host.textContent).toContain('private-bundle-secret');
    auth.set({ kind: 'signed-in', me: { sessionId: 'replacement', principalId: 'other-admin', role: 'admin' } }); await settle();
    expect(host.textContent).not.toContain('private-bundle-secret');
    expect(host.textContent).not.toContain('Loaded private.json');
    expect(ownership.adoptBundle).not.toHaveBeenCalled();
  });
});
