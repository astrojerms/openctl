import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { mount, tick, unmount } from 'svelte';
import { get } from 'svelte/store';
import Detail from './Detail.svelte';
import { ApiError, UnauthorizedError, inventoryApi, operations, ping, repo, resources, session, type GetResourceResponse, type Operation, type PingResponse } from '../lib/api';
import { auth } from '../lib/auth';
import { recordAuthority, resetAuthority } from '../lib/authority';
import { deleteAttemptKey, type DeleteAttemptRecord } from '../lib/deletionAttempt';
import { ops } from '../lib/ops';
import { clearOpsDrawerFocus, opsDrawerFocus } from '../lib/opsDrawer';
import * as watch from '../lib/watch';

const props = { apiVersion: 'test.openctl.io/v1', kind: 'Machine', resourceName: 'durable-vm' };
const targetIdentity = { apiVersion: props.apiVersion, kind: props.kind, name: props.resourceName };
const me = { role: 'admin', userId: 'display-name', principalId: 'canonical-actor', sessionId: 'session-one' };
const controller: PingResponse = { echo: '', serverVersion: 'dev', controllerId: 'controller-one', supportsIdempotentDelete: true };
const observed: GetResourceResponse = {
  resource: { apiVersion: props.apiVersion, kind: props.kind, metadata: { name: props.resourceName }, spec: { password: 'do-not-persist-this', image: 'image-one' }, status: { phase: 'running' } },
};
const savedAttempt: DeleteAttemptRecord = { version: 1, binding: { controllerId: controller.controllerId!, principalId: me.principalId }, target: targetIdentity, requestId: 'saved-request-one' };
const operation: Operation = { id: 'delete-operation', type: 'delete', status: 'succeeded', apiVersion: props.apiVersion, kind: props.kind, resourceName: props.resourceName, requestId: savedAttempt.requestId };
let component: Record<string, unknown> | undefined;
let target: HTMLDivElement;
let notify: () => void;

beforeEach(() => {
  const entries = new Map<string, string>();
  vi.stubGlobal('sessionStorage', {
    getItem: (key: string) => entries.get(key) ?? null,
    setItem: (key: string, value: string) => { entries.set(key, value); },
    removeItem: (key: string) => { entries.delete(key); },
    clear: () => entries.clear(),
    key: (index: number) => [...entries.keys()][index] ?? null,
    get length() { return entries.size; },
  } satisfies Storage);
  sessionStorage.clear();
  target = document.createElement('div');
  document.body.append(target);
  auth.set({ kind: 'signed-in', me });
  recordAuthority('mirror');
  ops.set([]);
  vi.spyOn(ping, 'ping').mockResolvedValue(controller);
  vi.spyOn(session, 'whoami').mockResolvedValue(me);
  vi.spyOn(resources, 'get').mockResolvedValue(observed);
  vi.spyOn(inventoryApi, 'get').mockRejectedValue(new ApiError(404, 'no saved declaration'));
  vi.spyOn(resources, 'listActions').mockResolvedValue({ actionSpecs: [] });
  vi.spyOn(resources, 'childrenGraph').mockResolvedValue({ nodes: [], edges: [] });
  vi.spyOn(repo, 'history').mockResolvedValue({ commits: [] });
  vi.spyOn(resources, 'delete').mockRejectedValue(new Error('Unexpected Delete in this isolated test'));
  vi.spyOn(operations, 'byRequest').mockRejectedValue(new Error('Unexpected recovery in this isolated test'));
  vi.spyOn(window, 'prompt').mockReturnValue(props.resourceName);
  vi.spyOn(watch, 'watchResources').mockImplementation((_av, _kind, _name, onEvent, opts) => {
    const pending = Promise.withResolvers<void>();
    notify = () => onEvent({ type: 'DELETED', resource: observed.resource });
    opts?.signal?.addEventListener('abort', () => pending.resolve(), { once: true });
    return pending.promise;
  });
});
afterEach(async () => {
  if (component) await unmount(component);
  component = undefined;
  target.remove();
  sessionStorage.clear();
  ops.set([]);
  clearOpsDrawerFocus();
  resetAuthority();
  auth.set({ kind: 'unknown' });
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
async function settle() {
  for (let i = 0; i < 8; i++) { await Promise.resolve(); await tick(); }
}
function button(label: string) {
  const found = [...target.querySelectorAll<HTMLButtonElement>('button')].find((node) => node.textContent?.trim() === label);
  if (!found) throw new Error(`Missing button: ${label}`);
  return found;
}

describe('detail durable deletion controls', () => {
  it('keeps a lost acknowledgement inspectable without saved/live data and recovers read-only with one Delete', async () => {
    const remove = vi.spyOn(resources, 'delete').mockImplementation(async (req) => {
      expect(JSON.parse(sessionStorage.getItem(deleteAttemptKey(targetIdentity))!)).toEqual({
        ...savedAttempt, requestId: req.requestId,
      });
      expect(req).toEqual({ ...targetIdentity, requestId: expect.any(String) });
      expect(req.requestId).toMatch(/^[0-9a-f-]{36}$/);
      throw new Error('acknowledgement dropped');
    });
    const recover = vi.spyOn(operations, 'byRequest').mockImplementation(async (requestId) => ({ ...operation, requestId, status: 'failed', error: 'provider deletion failed' }));
    component = mount(Detail, { target, props });
    await settle();
    button('Delete').click();
    await settle();
    const requestId = remove.mock.calls[0][0].requestId;
    expect(target.textContent).toContain(requestId);
    expect(target.textContent).toContain('acknowledgement dropped');
    expect(button('Delete').disabled).toBe(true);
    expect(recover).not.toHaveBeenCalled();
    vi.mocked(resources.get).mockRejectedValue(new ApiError(404, 'gone'));
    notify();
    await settle();
    expect(target.textContent).not.toContain('Saved desired intent');
    expect(target.textContent).not.toContain('Observed state');
    expect(target.textContent).toContain(requestId);
    expect(button('Recover request').disabled).toBe(false);
    button('Recover request').click();
    await settle();
    expect(target.textContent).toContain('Delete operation: failed');
    expect(target.textContent).toContain('provider deletion failed');
    expect(target.textContent).toContain(operation.id);
    expect(target.querySelector('h2')?.textContent).toBe(props.resourceName);
    button('Inspect delete operation').click();
    expect(get(opsDrawerFocus)).toEqual({ ...props, opId: operation.id });
    expect(remove).toHaveBeenCalledTimes(1);
    expect(recover).toHaveBeenCalledExactlyOnceWith(requestId, true, { signal: expect.any(AbortSignal) });
    expect(sessionStorage.getItem(deleteAttemptKey(targetIdentity))).not.toContain('do-not-persist-this');
  });

  it('restores only after binding validation and a reload never submits or prompts, even on unknown lookup', async () => {
    sessionStorage.setItem(deleteAttemptKey(targetIdentity), JSON.stringify(savedAttempt));
    vi.mocked(resources.get).mockRejectedValue(new Error('provider unavailable'));
    const remove = vi.spyOn(resources, 'delete');
    const recover = vi.spyOn(operations, 'byRequest').mockRejectedValue(new ApiError(404, 'unknown'));
    component = mount(Detail, { target, props });
    await settle();
    expect(target.textContent).toContain(savedAttempt.requestId);
    expect(target.textContent).not.toContain('Delete operation: succeeded');
    expect(remove).not.toHaveBeenCalled();
    expect(window.prompt).not.toHaveBeenCalled();
    expect(recover).not.toHaveBeenCalled();
    button('Recover request').click();
    await settle();
    expect(target.textContent).toContain('may still be admitted later');
    expect(target.textContent).toContain(savedAttempt.requestId);
    expect(button('Delete').disabled).toBe(true);
    expect(remove).not.toHaveBeenCalled();
    expect(window.prompt).not.toHaveBeenCalled();
  });

  it.each(['controller', 'principal', 'legacy', 'storage'])('does not expose private attempts or mutate with a %s mismatch/failure', async (scenario) => {
    const privateRecord = { ...savedAttempt, requestId: 'private-request-id', operationId: 'private-operation-id' };
    sessionStorage.setItem(deleteAttemptKey(targetIdentity), JSON.stringify(privateRecord));
    if (scenario === 'controller') vi.mocked(ping.ping).mockResolvedValue({ ...controller, controllerId: 'another-controller' });
    if (scenario === 'principal') {
      const next = { ...me, principalId: 'another-principal' };
      auth.set({ kind: 'signed-in', me: next });
      vi.mocked(session.whoami).mockResolvedValue(next);
    }
    if (scenario === 'legacy') vi.mocked(ping.ping).mockResolvedValue({ ...controller, supportsIdempotentDelete: false });
    if (scenario === 'storage') vi.spyOn(sessionStorage, 'getItem').mockImplementation(() => { throw new Error('storage denied'); });
    const remove = vi.spyOn(resources, 'delete');
    component = mount(Detail, { target, props });
    await settle();
    expect(target.textContent).not.toContain('private-request-id');
    expect(target.textContent).not.toContain('private-operation-id');
    expect(button('Delete').disabled).toBe(true);
    button('Delete').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await settle();
    expect(remove).not.toHaveBeenCalled();
    expect(window.prompt).not.toHaveBeenCalled();
  });

  it('blocks the RPC if persisting the exact request identity fails', async () => {
    vi.spyOn(sessionStorage, 'setItem').mockImplementation(() => { throw new Error('quota exceeded'); });
    const remove = vi.spyOn(resources, 'delete');
    component = mount(Detail, { target, props });
    await settle();
    button('Delete').click();
    await settle();
    expect(target.textContent).not.toContain('Request ID:');
    expect(remove).not.toHaveBeenCalled();
  });

  it.each([{ type: 'apply' }, { requestId: 'wrong-key' }, { resourceName: 'other-target' }])('does not display mismatched recovered evidence: %j', async (patch) => {
    sessionStorage.setItem(deleteAttemptKey(targetIdentity), JSON.stringify(savedAttempt));
    const remove = vi.spyOn(resources, 'delete');
    vi.spyOn(operations, 'byRequest').mockResolvedValue({ ...operation, ...patch, error: 'untrusted returned detail' });
    component = mount(Detail, { target, props });
    await settle();
    button('Recover request').click();
    await settle();
    expect(target.textContent).toContain('does not match');
    expect(target.textContent).not.toContain('untrusted returned detail');
    expect(target.textContent).not.toContain(operation.id);
    expect(remove).not.toHaveBeenCalled();
  });

  it('clears restored IDs on 401 and ignores a later recovery result', async () => {
    sessionStorage.setItem(deleteAttemptKey(targetIdentity), JSON.stringify(savedAttempt));
    const recovery = Promise.withResolvers<Operation>();
    vi.spyOn(operations, 'byRequest').mockReturnValue(recovery.promise);
    const remove = vi.spyOn(resources, 'delete');
    component = mount(Detail, { target, props });
    await settle();
    button('Recover request').click();
    await settle();
    expect(target.textContent).toContain(savedAttempt.requestId);
    ops.set([{ ...operation, id: 'private-op', status: 'running' }]);
    await settle();
    expect(target.textContent).toContain('private-op');
    vi.mocked(resources.get).mockRejectedValue(new UnauthorizedError('revoked'));
    notify();
    await settle();
    expect(get(auth).kind).toBe('signed-out');
    expect(target.textContent).not.toContain(savedAttempt.requestId);
    expect(target.textContent).not.toContain('private-op');
    recovery.resolve(operation);
    await settle();
    expect(target.textContent).not.toContain(savedAttempt.requestId);
    expect(target.textContent).not.toContain(operation.id);
    expect(remove).not.toHaveBeenCalled();
  });

  it('does not restore a late old-route binding response into the next mounted detail', async () => {
    sessionStorage.setItem(deleteAttemptKey(targetIdentity), JSON.stringify(savedAttempt));
    const oldBinding = Promise.withResolvers<PingResponse>();
    vi.mocked(ping.ping).mockReturnValueOnce(oldBinding.promise);
    const remove = vi.spyOn(resources, 'delete');
    component = mount(Detail, { target, props });
    await settle();
    await unmount(component);
    component = mount(Detail, { target, props: { ...props, resourceName: 'next-resource' } });
    await settle();
    oldBinding.resolve(controller);
    await settle();
    expect(target.querySelector('h2')?.textContent).toBe('next-resource');
    expect(target.textContent).not.toContain(savedAttempt.requestId);
    expect(remove).not.toHaveBeenCalled();
  });

  it('retains live freshness, typed consent, manifest authority and role gates', async () => {
    const remove = vi.spyOn(resources, 'delete');
    component = mount(Detail, { target, props });
    await settle();
    vi.mocked(window.prompt).mockReturnValue('wrong-name');
    button('Delete').click();
    await settle();
    expect(target.textContent).toContain('Name did not match');
    expect(remove).not.toHaveBeenCalled();
    vi.mocked(window.prompt).mockReturnValue(props.resourceName);
    recordAuthority('gitops');
    await tick();
    expect(target.querySelector('.delete-btn')).toBeNull();
    recordAuthority('mirror');
    await tick();
    vi.mocked(resources.get).mockRejectedValue(new Error('offline'));
    notify();
    // Handlers must reject even before stale enabled DOM has redrawn.
    button('Delete').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await settle();
    expect(remove).not.toHaveBeenCalled();
    auth.set({ kind: 'signed-in', me: { ...me, role: 'viewer' } });
    await settle();
    expect(target.querySelector('.delete-btn')).toBeNull();
  });

  it('uses matching shared progress without navigating away on operation success', async () => {
    vi.spyOn(resources, 'delete').mockResolvedValue({ operationId: operation.id, requestId: savedAttempt.requestId });
    sessionStorage.setItem(deleteAttemptKey(targetIdentity), JSON.stringify({ ...savedAttempt, operationId: operation.id }));
    component = mount(Detail, { target, props });
    await settle();
    ops.set([{ ...operation, status: 'running' }]);
    await settle();
    expect(target.textContent).toContain('Delete operation: running');
    ops.set([operation]);
    await settle();
    expect(target.textContent).toContain('Delete operation: succeeded');
    expect(target.textContent).toContain('The Delete operation succeeded');
    expect(target.querySelector('h2')?.textContent).toBe(props.resourceName);
    expect(button('Delete').disabled).toBe(true);
  });
});

describe('detail reviewed fresh-key deletions', () => {
  it('offers a separately confirmed NEW deletion after terminal failure, rechecking outcome and observation before persisting a different key', async () => {
    sessionStorage.setItem(deleteAttemptKey(targetIdentity), JSON.stringify(savedAttempt));
    const lookup = vi.spyOn(operations, 'byRequest').mockResolvedValue({ ...operation, status: 'failed', error: 'first deletion failed' });
    const remove = vi.spyOn(resources, 'delete').mockImplementation(async (request) => {
      expect(request.requestId).not.toBe(savedAttempt.requestId);
      expect(JSON.parse(sessionStorage.getItem(deleteAttemptKey(targetIdentity))!)).toEqual({ ...savedAttempt, requestId: request.requestId });
      return { operationId: 'fresh-delete-operation', requestId: request.requestId };
    });
    component = mount(Detail, { target, props });
    await settle();
    expect(target.textContent).not.toContain('Review new delete attempt');
    button('Recover request').click();
    await settle();
    expect(lookup).toHaveBeenCalledTimes(1);
    expect(resources.get).toHaveBeenCalledTimes(2);
    expect(remove).not.toHaveBeenCalled();
    expect(window.prompt).not.toHaveBeenCalled();
    button('Review new delete attempt').click();
    await settle();
    expect(lookup).toHaveBeenCalledTimes(2);
    expect(resources.get).toHaveBeenCalledTimes(3);
    expect(window.prompt).toHaveBeenCalledExactlyOnceWith(expect.stringContaining('Review a NEW deletion'));
    expect(window.prompt).toHaveBeenCalledWith(expect.stringContaining('NOT a replay'));
    expect(remove).toHaveBeenCalledTimes(1);
    expect(target.textContent).toContain('fresh-delete-operation');
    expect(target.textContent).not.toContain('first deletion failed');
    expect(target.querySelector('h2')?.textContent).toBe(props.resourceName);
  });

  it.each([false, true])('allows a new deletion after success only when fresh post-terminal observation has a tracked declaration (tracked=%s)', async (tracked) => {
    sessionStorage.setItem(deleteAttemptKey(targetIdentity), JSON.stringify({ ...savedAttempt, operationId: operation.id }));
    const lookup = vi.spyOn(operations, 'byRequest').mockResolvedValue(operation);
    const remove = vi.spyOn(resources, 'delete').mockImplementation(async (request) => ({ operationId: 'replacement-delete', requestId: request.requestId }));
    component = mount(Detail, { target, props });
    await settle();
    // Stream/cached success alone must not unlock another deletion.
    ops.set([operation]);
    await settle();
    expect(target.textContent).not.toContain('Review new delete attempt');
    if (tracked) vi.mocked(resources.get).mockResolvedValue({ ...observed, applied: observed.resource });
    button('Recover request').click();
    await settle();
    expect(lookup).toHaveBeenCalledTimes(1);
    expect(resources.get).toHaveBeenCalledTimes(2);
    if (tracked) {
      button('Review new delete attempt').click();
      await settle();
      expect(remove).toHaveBeenCalledTimes(1);
      expect(remove.mock.calls[0][0].requestId).not.toBe(savedAttempt.requestId);
      expect(window.prompt).toHaveBeenCalledWith(expect.stringContaining('NEW deletion'));
    } else {
      expect(target.textContent).not.toContain('Review new delete attempt');
      expect(remove).not.toHaveBeenCalled();
      expect(window.prompt).not.toHaveBeenCalled();
    }
  });

  it('keeps terminal recovery inspectable without permitting a new deletion when the post-terminal observation fails', async () => {
    sessionStorage.setItem(deleteAttemptKey(targetIdentity), JSON.stringify(savedAttempt));
    vi.spyOn(operations, 'byRequest').mockResolvedValue({ ...operation, status: 'failed' });
    const remove = vi.spyOn(resources, 'delete');
    component = mount(Detail, { target, props });
    await settle();
    vi.mocked(resources.get).mockRejectedValue(new Error('provider outage after deletion'));
    button('Recover request').click();
    await settle();
    expect(target.textContent).toContain('Delete operation: failed');
    expect(target.textContent).toContain(savedAttempt.requestId);
    expect(target.textContent).toContain('provider outage after deletion');
    expect(target.textContent).not.toContain('Review new delete attempt');
    expect(remove).not.toHaveBeenCalled();
    expect(window.prompt).not.toHaveBeenCalled();
  });
});
