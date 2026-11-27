import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { mount, tick, unmount } from 'svelte';
import Discovery from './Discovery.svelte';
import { discovery, ownership } from '../lib/api';
import { auth } from '../lib/auth';

let component: Record<string, unknown> | undefined;
let host: HTMLDivElement;
beforeEach(() => {
  host = document.createElement('div'); document.body.append(host);
  auth.set({ kind: 'signed-in', me: { sessionId: 'reader-session', principalId: 'reader', role: 'viewer' } });
  vi.spyOn(discovery, 'contexts').mockResolvedValue({ contexts: [
    { name: 'private-ca', endpoint: 'https://private.example:8006', defaultContext: true },
    { name: 'working', endpoint: 'https://working.example:8006' },
  ] });
  vi.spyOn(discovery, 'test').mockImplementation(async (context) => context === 'private-ca'
    // Protobuf JSON omits false; failed responses must not require connected:false.
    ? { failureCode: 'tls_untrusted', message: 'Install a verified CA bundle on the controller host, set caFile and restart.' }
    : { connected: true, message: 'Authenticated read-only inventory succeeded' });
  vi.spyOn(discovery, 'inventory').mockResolvedValue({ resources: [{
    context: 'working', node: 'pve1', vmid: '101', identity: 'immutable-id', adoptable: true,
    resource: { apiVersion: 'proxmox.openctl.io/v1', kind: 'VirtualMachine', metadata: { name: 'existing-vm' }, spec: { context: 'working', node: 'pve1' } },
  }] });
  vi.spyOn(ownership, 'adopt').mockRejectedValue(new Error('Unexpected adoption'));
  vi.spyOn(ownership, 'adoptBundle').mockRejectedValue(new Error('Unexpected bundle adoption'));
  vi.spyOn(ownership, 'confirmIdentity').mockRejectedValue(new Error('Unexpected identity confirmation'));
  vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('Unexpected HTTP in isolated discovery test')));
});
afterEach(async () => {
  if (component) await unmount(component); component = undefined;
  host.remove(); auth.set({ kind: 'unknown' }); vi.restoreAllMocks(); vi.unstubAllGlobals();
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
    expect(discovery.inventory).toHaveBeenCalledExactlyOnceWith('working');
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
