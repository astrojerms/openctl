import { describe, expect, it, vi } from 'vitest';
import { get } from 'svelte/store';
import { ApiError, UnauthorizedError, type Operation, type PingResponse, type WhoAmIResponse } from './api';
import { createDeleteAttempt, deleteAttemptKey, type DeleteAttemptAPI, type DeleteAttemptRecord, type DeleteAttemptStorage } from './deletionAttempt';

const target = { apiVersion: 'test.openctl.io/v1', kind: 'Machine', name: 'one' };
const me: WhoAmIResponse = { userId: 'display-user', sessionId: 'session', principalId: 'canonical-user', role: 'admin' };
const controller: PingResponse = { echo: '', serverVersion: 'dev', controllerId: 'controller-one', supportsIdempotentDelete: true };
const record: DeleteAttemptRecord = { version: 1, binding: { controllerId: controller.controllerId!, principalId: me.principalId! }, target, requestId: 'request-one' };
const operation: Operation = { id: 'delete-operation', type: 'delete', apiVersion: target.apiVersion, kind: target.kind, resourceName: target.name, requestId: record.requestId, status: 'succeeded' };
function harness(saved?: DeleteAttemptRecord) {
  const data = new Map<string, string>();
  if (saved) data.set(deleteAttemptKey(target), JSON.stringify(saved));
  const storage: DeleteAttemptStorage = {
    getItem: vi.fn((key: string) => data.get(key) ?? null),
    setItem: vi.fn((key: string, value: string) => { data.set(key, value); }),
  };
  const api: DeleteAttemptAPI = {
    ping: vi.fn(async () => controller),
    whoami: vi.fn(async () => me),
    delete: vi.fn(async () => ({ operationId: operation.id, requestId: record.requestId })),
    byRequest: vi.fn(async () => operation),
    requestId: vi.fn(() => record.requestId),
  };
  const canDelete = vi.fn(() => true);
  const canReviewNew = vi.fn((_operation: Operation) => canDelete());
  const refreshAfterTerminal = vi.fn(async () => {});
  const confirm = vi.fn((): string | null => target.name);
  const onUnauthorized = vi.fn();
  const attempt = createDeleteAttempt({ storage: () => storage, canDelete, canReviewNew, refreshAfterTerminal, confirm, onUnauthorized, api });
  return { attempt, data, storage, api, canDelete, canReviewNew, refreshAfterTerminal, confirm, onUnauthorized };
}

describe('durable deletion attempts', () => {
  it('persists only bound request metadata before invocation and recovers a dropped acknowledgement without a second Delete', async () => {
    const h = harness();
    vi.mocked(h.api.delete).mockImplementation(async (request) => {
      expect(JSON.parse(h.data.get(deleteAttemptKey(target))!)).toEqual(record);
      expect(request).toEqual({ ...target, requestId: record.requestId });
      throw new Error('acknowledgement lost');
    });
    await h.attempt.open(target, me);
    await h.attempt.submit();
    expect(get(h.attempt).attempt).toEqual(record);
    expect(get(h.attempt).error).toContain('acknowledgement lost');
    await h.attempt.recover();
    expect(get(h.attempt).operation).toEqual(operation);
    expect(get(h.attempt).attempt?.operationId).toBe(operation.id);
    expect(h.api.byRequest).toHaveBeenCalledExactlyOnceWith(record.requestId, true, { signal: expect.any(AbortSignal) });
    await h.attempt.submit();
    expect(h.api.delete).toHaveBeenCalledTimes(1);
    expect(h.confirm).toHaveBeenCalledTimes(1);
    expect(h.api.requestId).toHaveBeenCalledTimes(1);
    expect(JSON.parse(h.data.get(deleteAttemptKey(target))!)).toEqual({ ...record, operationId: operation.id });
  });

  it('restores a reload as identity only and never submits or restores consent/status', async () => {
    const h = harness({ ...record, operationId: operation.id });
    await h.attempt.open(target, me);
    expect(get(h.attempt).attempt).toEqual({ ...record, operationId: operation.id });
    expect(get(h.attempt).operation).toBeNull();
    expect(h.api.delete).not.toHaveBeenCalled();
    expect(h.api.byRequest).not.toHaveBeenCalled();
    expect(h.confirm).not.toHaveBeenCalled();
    await h.attempt.submit();
    expect(h.api.delete).not.toHaveBeenCalled();
    expect(h.api.requestId).not.toHaveBeenCalled();
  });

  it('keeps an unknown lookup recovery-only because the original admission may still arrive', async () => {
    const h = harness(record);
    vi.mocked(h.api.byRequest).mockRejectedValue(new ApiError(404, 'unknown request'));
    await h.attempt.open(target, me);
    await h.attempt.recover();
    expect(get(h.attempt).error).toContain('may still be admitted later');
    expect(get(h.attempt).attempt).toEqual(record);
    await h.attempt.submit();
    expect(h.api.delete).not.toHaveBeenCalled();
    expect(h.api.requestId).not.toHaveBeenCalled();
    vi.mocked(h.api.byRequest).mockResolvedValue(operation);
    await h.attempt.recover();
    expect(get(h.attempt).operation?.status).toBe('succeeded');
  });

  it.each([
    { binding: { ...record.binding, principalId: 'different-private-user' } },
    { binding: { ...record.binding, controllerId: 'different-private-controller' } },
    { target: { ...target, name: 'different-private-target' } },
    { version: 2 },
    { credentials: 'must-not-be-restored' },
  ])('rejects unbound or unsupported storage without revealing its request identity: %j', async (patch) => {
    const h = harness();
    h.data.set(deleteAttemptKey(target), JSON.stringify({ ...record, requestId: 'private-request-id', operationId: 'private-operation-id', ...patch }));
    await h.attempt.open(target, me);
    await h.attempt.submit();
    expect(get(h.attempt).attempt).toBeNull();
    expect(get(h.attempt).ready).toBe(false);
    expect(JSON.stringify(get(h.attempt))).not.toContain('private-');
    expect(h.api.delete).not.toHaveBeenCalled();
    expect(h.confirm).not.toHaveBeenCalled();
  });

  it.each([
    { supportsIdempotentDelete: false },
    { controllerId: '' },
  ])('refuses unsafe controllers before restoring or sending Delete: %j', async (patch) => {
    const h = harness(record);
    vi.mocked(h.api.ping).mockResolvedValue({ ...controller, ...patch });
    await h.attempt.open(target, me);
    await h.attempt.submit();
    expect(get(h.attempt).attempt).toBeNull();
    expect(h.storage.getItem).not.toHaveBeenCalled();
    expect(h.api.delete).not.toHaveBeenCalled();
  });

  it('requires a canonical current principal rather than userId and checks whoami binding', async () => {
    const h = harness(record);
    await h.attempt.open(target, { ...me, principalId: '' });
    expect(h.api.ping).not.toHaveBeenCalled();
    expect(h.storage.getItem).not.toHaveBeenCalled();
    vi.mocked(h.api.whoami).mockResolvedValue({ ...me, principalId: 'other' });
    await h.attempt.open(target, me);
    await h.attempt.submit();
    expect(get(h.attempt).attempt).toBeNull();
    expect(h.api.delete).not.toHaveBeenCalled();
  });

  it.each(['read', 'write'])('blocks deletion when session storage fails to %s', async (failure) => {
    const h = harness();
    if (failure === 'read') vi.mocked(h.storage.getItem).mockImplementation(() => { throw new Error('storage denied'); });
    else vi.mocked(h.storage.setItem).mockImplementation(() => { throw new Error('quota'); });
    await h.attempt.open(target, me);
    await h.attempt.submit();
    expect(h.api.delete).not.toHaveBeenCalled();
    expect(h.data.size).toBe(0);
    expect(get(h.attempt).error).toContain(failure === 'read' ? 'storage denied' : 'Cannot persist');
  });

  it.each([
    { requestId: 'another-request', operationId: operation.id },
    { requestId: record.requestId, operationId: '' },
    { operationId: operation.id },
  ])('does not accept a mismatched or incomplete Delete acknowledgement: %j', async (response) => {
    const h = harness();
    vi.mocked(h.api.delete).mockResolvedValue(response);
    await h.attempt.open(target, me);
    await h.attempt.submit();
    expect(get(h.attempt).attempt).toEqual(record);
    expect(get(h.attempt).operation).toBeNull();
    expect(get(h.attempt).error).toContain('did not match');
    await h.attempt.submit();
    expect(h.api.delete).toHaveBeenCalledTimes(1);
  });

  it.each([
    { type: 'apply' }, { requestId: 'different-key' }, { resourceName: 'replacement' },
    { apiVersion: 'other/v1' }, { kind: 'OtherKind' }, { parentId: 'parent' },
    { id: '' }, { status: 'unknown' },
  ])('rejects recovery for the wrong root action, target, request or status: %j', async (patch) => {
    const h = harness(record);
    vi.mocked(h.api.byRequest).mockResolvedValue({ ...operation, ...patch });
    await h.attempt.open(target, me);
    await h.attempt.recover();
    expect(get(h.attempt).operation).toBeNull();
    expect(get(h.attempt).attempt).toEqual(record);
    expect(get(h.attempt).error).toContain('does not match');
    expect(h.api.delete).not.toHaveBeenCalled();
  });

  it('rejects a different operation ID after acknowledgement', async () => {
    const h = harness({ ...record, operationId: 'original-operation' });
    await h.attempt.open(target, me);
    await h.attempt.recover();
    expect(get(h.attempt).operation).toBeNull();
    expect(get(h.attempt).attempt?.operationId).toBe('original-operation');
  });

  it('ignores late binding and storage restoration after a route switch', async () => {
    const h = harness(record);
    const slowPing = Promise.withResolvers<PingResponse>();
    vi.mocked(h.api.ping).mockReturnValueOnce(slowPing.promise);
    const first = h.attempt.open(target, me);
    const firstSignal = vi.mocked(h.api.ping).mock.calls[0][0].signal;
    const next = { ...target, name: 'two' };
    await h.attempt.open(next, me);
    slowPing.resolve(controller);
    await first;
    expect(firstSignal?.aborted).toBe(true);
    expect(get(h.attempt).attempt).toBeNull();
    expect(h.storage.getItem).toHaveBeenCalledExactlyOnceWith(deleteAttemptKey(next));
    expect(h.api.delete).not.toHaveBeenCalled();
  });

  it('ignores late RPC acknowledgement after route navigation but preserves its original durable record', async () => {
    const h = harness();
    const pending = Promise.withResolvers<{ operationId: string; requestId: string }>();
    vi.mocked(h.api.delete).mockReturnValue(pending.promise);
    await h.attempt.open(target, me);
    const sending = h.attempt.submit();
    await vi.waitFor(() => expect(h.api.delete).toHaveBeenCalledTimes(1));
    await h.attempt.open({ ...target, name: 'two' }, me);
    expect(vi.mocked(h.api.delete).mock.calls[0][1].signal?.aborted).toBe(true);
    pending.resolve({ operationId: operation.id, requestId: record.requestId });
    await sending;
    expect(get(h.attempt).attempt).toBeNull();
    expect(JSON.parse(h.data.get(deleteAttemptKey(target))!)).toEqual(record);
    expect(h.data.has(deleteAttemptKey({ ...target, name: 'two' }))).toBe(false);
  });

  it('clears revoked-session data immediately and ignores late read-only recovery', async () => {
    const h = harness(record);
    const pending = Promise.withResolvers<Operation>();
    vi.mocked(h.api.byRequest).mockReturnValue(pending.promise);
    await h.attempt.open(target, me);
    const recovery = h.attempt.recover();
    await vi.waitFor(() => expect(h.api.byRequest).toHaveBeenCalledTimes(1));
    await h.attempt.open(target, null);
    expect(get(h.attempt).attempt).toBeNull();
    pending.resolve(operation);
    await recovery;
    expect(get(h.attempt).attempt).toBeNull();
    expect(get(h.attempt).operation).toBeNull();
  });

  it('treats a binding 401 as terminal even while another binding response remains pending', async () => {
    const h = harness(record);
    const pending = Promise.withResolvers<PingResponse>();
    vi.mocked(h.api.ping).mockReturnValue(pending.promise);
    vi.mocked(h.api.whoami).mockRejectedValue(new UnauthorizedError('revoked'));
    await h.attempt.open(target, me);
    expect(h.onUnauthorized).toHaveBeenCalledTimes(1);
    expect(get(h.attempt).attempt).toBeNull();
    pending.resolve(controller);
    await Promise.resolve();
    expect(get(h.attempt).attempt).toBeNull();
    expect(h.storage.getItem).not.toHaveBeenCalled();
  });

  it('clears a restored request on recovery 401 and never republishes it', async () => {
    const h = harness(record);
    vi.mocked(h.api.byRequest).mockRejectedValue(new UnauthorizedError('expired'));
    await h.attempt.open(target, me);
    await h.attempt.recover();
    expect(h.onUnauthorized).toHaveBeenCalledTimes(1);
    expect(get(h.attempt).attempt).toBeNull();
    expect(get(h.attempt).operation).toBeNull();
    await h.attempt.verify();
    expect(h.storage.getItem).toHaveBeenCalledTimes(1);
  });

  it('hides a restored request when controller identity changes during recovery', async () => {
    const h = harness(record);
    await h.attempt.open(target, me);
    vi.mocked(h.api.ping).mockResolvedValue({ ...controller, controllerId: 'controller-two' });
    await h.attempt.recover();
    expect(get(h.attempt).attempt).toBeNull();
    expect(h.api.byRequest).not.toHaveBeenCalled();
    expect(h.api.delete).not.toHaveBeenCalled();
  });

  it('rechecks live mutation authority and role after asynchronous binding and typed consent', async () => {
    const h = harness();
    await h.attempt.open(target, me);
    h.canDelete.mockReturnValue(false);
    await h.attempt.submit();
    expect(h.confirm).not.toHaveBeenCalled();
    h.canDelete.mockReturnValue(true);
    vi.mocked(h.api.whoami).mockResolvedValue({ ...me, role: 'viewer' });
    await h.attempt.submit();
    expect(h.confirm).not.toHaveBeenCalled();
    vi.mocked(h.api.whoami).mockResolvedValue(me);
    h.confirm.mockImplementation(() => { h.canDelete.mockReturnValue(false); return target.name; });
    await h.attempt.submit();
    expect(h.api.delete).not.toHaveBeenCalled();
    expect(h.storage.setItem).not.toHaveBeenCalled();
  });

  it('retains the typed-name confirmation and does not persist consent or send on mismatch', async () => {
    const h = harness();
    h.confirm.mockReturnValue('wrong-name');
    await h.attempt.open(target, me);
    await h.attempt.submit();
    expect(get(h.attempt).error).toContain('Name did not match');
    expect(h.api.delete).not.toHaveBeenCalled();
    expect(h.storage.setItem).not.toHaveBeenCalled();
  });

  it('uses strictly matching shared operation progress and retains terminal failures without retrying', async () => {
    const h = harness();
    await h.attempt.open(target, me);
    await h.attempt.submit();
    h.attempt.observe({ ...operation, type: 'apply', status: 'running' });
    expect(get(h.attempt).operation).toBeNull();
    h.attempt.observe({ ...operation, requestId: 'wrong-key', status: 'running' });
    expect(get(h.attempt).operation).toBeNull();
    h.attempt.observe({ ...operation, status: 'running' });
    expect(get(h.attempt).operation?.status).toBe('running');
    h.attempt.observe({ ...operation, status: 'failed', error: 'provider refused deletion' });
    expect(get(h.attempt).operation?.error).toBe('provider refused deletion');
    await h.attempt.submit();
    await h.attempt.reviewNew();
    expect(get(h.attempt).reviewable).toBe(false);
    expect(h.api.delete).toHaveBeenCalledTimes(1);
    expect(h.api.requestId).toHaveBeenCalledTimes(1);
  });

  it.each(['failed', 'interrupted', 'canceled'])('requires a fresh terminal %s lookup, refreshed observation and typed consent before persisting a new key', async (status) => {
    const h = harness(record);
    vi.mocked(h.api.byRequest).mockResolvedValue({ ...operation, status });
    vi.mocked(h.api.requestId).mockReturnValue('fresh-request-two');
    vi.mocked(h.api.delete).mockImplementation(async (request) => {
      expect(JSON.parse(h.data.get(deleteAttemptKey(target))!)).toEqual({ ...record, requestId: 'fresh-request-two' });
      expect(request.requestId).toBe('fresh-request-two');
      return { requestId: request.requestId, operationId: 'second-operation' };
    });
    await h.attempt.open(target, me);
    await h.attempt.reviewNew();
    expect(h.api.byRequest).not.toHaveBeenCalled();
    await h.attempt.recover();
    expect(get(h.attempt).reviewable).toBe(true);
    expect(h.api.delete).not.toHaveBeenCalled();
    expect(h.refreshAfterTerminal).toHaveBeenCalledTimes(1);
    await h.attempt.reviewNew();
    expect(h.api.byRequest).toHaveBeenCalledTimes(2);
    expect(h.refreshAfterTerminal).toHaveBeenCalledTimes(2);
    expect(h.confirm).toHaveBeenCalledExactlyOnceWith(target, true);
    expect(h.api.delete).toHaveBeenCalledTimes(1);
    expect(get(h.attempt).operation).toBeNull();
    expect(get(h.attempt).reviewable).toBe(false);
    expect(get(h.attempt).attempt).toEqual({ ...record, requestId: 'fresh-request-two', operationId: 'second-operation' });
  });

  it.each([404, 412])('revokes review permission if an earlier terminal recovery is now unknown/pruned (HTTP %i)', async (status) => {
    const h = harness(record);
    vi.mocked(h.api.byRequest).mockResolvedValue({ ...operation, status: 'failed' });
    await h.attempt.open(target, me);
    await h.attempt.recover();
    vi.mocked(h.api.byRequest).mockRejectedValue(new ApiError(status, 'original evidence unavailable'));
    await h.attempt.reviewNew();
    expect(get(h.attempt).reviewable).toBe(false);
    expect(h.api.requestId).not.toHaveBeenCalled();
    expect(h.api.delete).not.toHaveBeenCalled();
    expect(h.confirm).not.toHaveBeenCalled();
    expect(JSON.parse(h.data.get(deleteAttemptKey(target))!)).toEqual({ ...record, operationId: operation.id });
  });

  it('preserves the original recoverable attempt if fresh-key persistence fails', async () => {
    const h = harness(record);
    vi.mocked(h.api.byRequest).mockResolvedValue({ ...operation, status: 'failed' });
    vi.mocked(h.api.requestId).mockReturnValue('fresh-request-two');
    await h.attempt.open(target, me);
    await h.attempt.recover();
    vi.mocked(h.storage.setItem).mockImplementation(() => { throw new Error('quota'); });
    await h.attempt.reviewNew();
    expect(h.api.delete).not.toHaveBeenCalled();
    expect(get(h.attempt).attempt).toEqual({ ...record, operationId: operation.id });
    expect(JSON.parse(h.data.get(deleteAttemptKey(target))!)).toEqual({ ...record, operationId: operation.id });
  });

  it('never reuses the original key as a new deletion after terminal failure', async () => {
    const h = harness(record);
    vi.mocked(h.api.byRequest).mockResolvedValue({ ...operation, status: 'failed' });
    await h.attempt.open(target, me);
    await h.attempt.recover();
    await h.attempt.reviewNew();
    expect(h.api.delete).not.toHaveBeenCalled();
    expect(get(h.attempt).error).toContain('fresh valid Delete request identity');
    expect(JSON.parse(h.data.get(deleteAttemptKey(target))!)).toEqual({ ...record, operationId: operation.id });
  });

  it('requires fresh post-terminal observation/authority, not merely a terminal lookup', async () => {
    const h = harness(record);
    vi.mocked(h.api.byRequest).mockResolvedValue({ ...operation, status: 'succeeded' });
    h.canReviewNew.mockReturnValue(false);
    await h.attempt.open(target, me);
    await h.attempt.recover();
    expect(h.refreshAfterTerminal).toHaveBeenCalledTimes(1);
    expect(get(h.attempt).reviewable).toBe(false);
    await h.attempt.reviewNew();
    expect(h.api.delete).not.toHaveBeenCalled();
  });
});
