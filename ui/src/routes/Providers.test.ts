import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { mount, tick, unmount } from 'svelte';
import { get } from 'svelte/store';
import Providers from './Providers.svelte';
import { providersApi, UnauthorizedError, type UpsertProviderRequest } from '../lib/api';
import { auth } from '../lib/auth';

const entry = { name: 'proxmox', endpoint: 'https://pve.example:8006', tokenId: 'root@pam!lab', hasSecret: true, usesSecretFile: true, caFile: '/controller/trusted.pem', insecureSkipVerify: false };
let component: Record<string, unknown> | undefined;
let host: HTMLDivElement;
beforeEach(() => {
  host = document.createElement('div'); document.body.append(host);
  auth.set({ kind: 'signed-in', me: { sessionId: 'session', principalId: 'operator', role: 'admin' } });
  vi.spyOn(providersApi, 'list').mockResolvedValue({ providers: [entry] });
  vi.spyOn(providersApi, 'upsert').mockResolvedValue({ provider: entry });
  vi.spyOn(providersApi, 'delete').mockRejectedValue(new Error('Unexpected delete'));
  vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('Unexpected HTTP in isolated credential test')));
});
afterEach(async () => {
  if (component) await unmount(component); component = undefined;
  host.remove(); auth.set({ kind: 'unknown' }); vi.restoreAllMocks(); vi.unstubAllGlobals();
});
async function settle() { for (let i = 0; i < 5; i++) { await Promise.resolve(); await tick(); } }
function button(label: string) {
  const found = [...host.querySelectorAll<HTMLButtonElement>('button')].find((node) => node.textContent?.trim() === label);
  if (!found) throw new Error(`Missing control ${label}`); return found;
}
function input(selector: string, value: string) {
  const node = host.querySelector<HTMLInputElement>(selector)!; node.value = value; node.dispatchEvent(new Event('input', { bubbles: true }));
}
async function open() { component = mount(Providers, { target: host }); await settle(); button('Edit').click(); await tick(); }
function insecure() {
  const checkbox = host.querySelector<HTMLInputElement>('#prov-insecure')!; checkbox.checked = true; checkbox.dispatchEvent(new Event('change', { bubbles: true }));
}

describe('provider certificate policy', () => {
  it('shows saved trust separately from connectivity and preserves file-backed credentials when editing CA trust', async () => {
    await open();
    expect(host.textContent).toContain('/controller/trusted.pem');
    expect(host.querySelector<HTMLInputElement>('#prov-token-secret')!.value).toBe('');
    input('#prov-ca-file', '/controller/rotated.pem');
    button('Save').click(); await settle();
    const request = vi.mocked(providersApi.upsert).mock.calls[0][0];
    expect(request).toMatchObject({ name: 'proxmox', caFile: '/controller/rotated.pem', insecureSkipVerify: false, tokenSecret: '' });
    expect(host.querySelector('#prov-token-secret')).toBeNull();
    expect(host.querySelector('[role="status"]')).not.toBeNull();
  });

  it('requires explicit insecure consent and a cleared CA path before persisting that policy', async () => {
    await open(); insecure(); await tick();
    expect(host.querySelector('[role="alert"]')).not.toBeNull();
    button('Save').click(); await settle();
    expect(providersApi.upsert).not.toHaveBeenCalled();
    input('#prov-ca-file', ''); button('Save').click(); await settle();
    expect(vi.mocked(providersApi.upsert).mock.calls[0][0]).toMatchObject({ caFile: '', insecureSkipVerify: true });
  });

  it.each(['http://pve.example:8006', 'https://user:private-password@pve.example:8006', 'https://pve.example:8006?token=private-token'])('blocks an unsafe endpoint %s even with explicit insecure certificate mode', async (endpoint) => {
    await open(); input('#prov-ca-file', ''); insecure(); input('#prov-endpoint', endpoint); button('Save').click(); await settle();
    expect(providersApi.upsert).not.toHaveBeenCalled();
    expect(host.querySelector('#prov-endpoint')).not.toBeNull();
  });

  it('does not save a stale admin form after the principal loses authority, and clears typed secrets', async () => {
    await open(); input('#prov-token-secret', 'private-draft-secret');
    const save = button('Save');
    auth.set({ kind: 'signed-in', me: { sessionId: 'viewer', principalId: 'reader', role: 'viewer' } });
    save.click(); await settle();
    expect(providersApi.upsert).not.toHaveBeenCalled();
    expect(host.querySelector('#prov-token-secret')).toBeNull();
    expect(host.textContent).not.toContain('private-draft-secret');
  });

  it('captures the submitted policy and ignores late completion after teardown', async () => {
    const pending = Promise.withResolvers<{ provider: typeof entry }>();
    vi.mocked(providersApi.upsert).mockReturnValueOnce(pending.promise);
    await open(); input('#prov-ca-file', '/controller/submitted.pem'); button('Save').click(); await tick();
    const request: UpsertProviderRequest = vi.mocked(providersApi.upsert).mock.calls[0][0];
    expect(request.caFile).toBe('/controller/submitted.pem');
    const signal = vi.mocked(providersApi.upsert).mock.calls[0][1]?.signal;
    await unmount(component!); component = undefined; expect(signal?.aborted).toBe(true);
    pending.resolve({ provider: entry }); await settle();
    expect(host.textContent).toBe('');
    expect(providersApi.list).toHaveBeenCalledTimes(1);
  });

  it('clears credential input and ends the session on authorization loss', async () => {
    vi.mocked(providersApi.upsert).mockRejectedValueOnce(new UnauthorizedError('expired'));
    await open(); input('#prov-token-secret', 'private-draft-secret'); button('Save').click(); await settle();
    expect(get(auth).kind).toBe('signed-out');
    expect(host.querySelector('#prov-token-secret')).toBeNull();
    expect(host.textContent).not.toContain('private-draft-secret');
  });
});
