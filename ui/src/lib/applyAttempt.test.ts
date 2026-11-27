import { describe, expect, it, vi } from 'vitest';
import { get } from 'svelte/store';
import { ApiError, UnauthorizedError, type ApplyRequest, type Operation } from './api';
import { applyAttemptKey, createApplyAttempt, type ApplyAttemptAPI, type ApplyAttemptRecord, type ApplyAttemptScope } from './applyAttempt';
import type { ReviewBinding } from './deploymentReview';

const scope: ApplyAttemptScope = { source: 'create', apiVersion: 'test.openctl.io/v1', kind: 'Machine' };
const identity: ReviewBinding = { controllerId: 'controller-one', userId: 'canonical-user' };
const me = { sessionId: 'session-one', principalId: identity.userId, role: 'admin' };
const request: ApplyRequest = { resource: { apiVersion: scope.apiVersion, kind: scope.kind, metadata: { name: 'first', labels: { env: 'lab' }, annotations: { purpose: 'exact' } }, spec: { password: 'literal-private-credential', upstream: { $ref: { apiVersion: 'test.openctl.io/v1', kind: 'Machine', name: 'upstream', field: 'status.ip' } } } }, createOnly: true, allowDestructive: true, iKnowThisBreaksTheCluster: true };
const record: ApplyAttemptRecord = { version: 1, binding: identity, scope, target: { apiVersion: scope.apiVersion, kind: scope.kind, name: 'first' }, requestId: 'request-first' };
const operation: Operation = { id: 'operation-first', requestId: record.requestId, type: 'apply', status: 'succeeded', apiVersion: record.target.apiVersion, kind: record.target.kind, resourceName: record.target.name };
function harness(saved?: ApplyAttemptRecord) {
  const values = new Map<string, string>();
  if (saved) values.set(applyAttemptKey(scope), JSON.stringify(saved));
  const storage = { getItem: vi.fn((key: string) => values.get(key) ?? null), setItem: vi.fn((key: string, value: string) => { values.set(key, value); }) };
  const api: ApplyAttemptAPI = {
    binding: vi.fn(async () => identity),
    apply: vi.fn(async (req) => ({ requestId: req.requestId, operationId: operation.id })),
    byRequest: vi.fn(async () => operation),
    requestId: vi.fn(() => record.requestId),
  };
  const policy = vi.fn(() => true);
  const reviewed = vi.fn(() => true);
  const unauthorized = vi.fn();
  const attempt = createApplyAttempt({ storage: () => storage, canSubmit: policy, onUnauthorized: unauthorized, api });
  return { attempt, values, storage, api, policy, reviewed, unauthorized };
}

describe('metadata-only durable console Apply', () => {
  it('persists identity before sending exact intent and recovers a lost acknowledgement without another Apply', async () => {
    const h = harness();
    vi.mocked(h.api.apply).mockImplementation(async (wire) => {
      expect(JSON.parse(h.values.get(applyAttemptKey(scope))!)).toEqual(record);
      expect(wire).toEqual({ ...request, requestId: record.requestId });
      throw new Error('accepted acknowledgement lost');
    });
    await h.attempt.open(scope, me);
    await h.attempt.submit(request, h.reviewed);
    expect(get(h.attempt).attempt).toEqual(record);
    expect(get(h.attempt).error).toContain('acknowledgement lost');
    await h.attempt.recover();
    expect(get(h.attempt).operation).toEqual(operation);
    await h.attempt.submit(request, h.reviewed);
    expect(h.api.apply).toHaveBeenCalledTimes(1);
    const stored = h.values.get(applyAttemptKey(scope))!;
    expect(stored).not.toContain('literal-private-credential');
    expect(stored).not.toContain('$ref');
    expect(stored).not.toContain('allowDestructive');
    expect(stored).not.toContain('createOnly');
  });

  it('restores a different submitted name as metadata only, never as a new draft or automatic request', async () => {
    const h = harness({ ...record, operationId: operation.id });
    await h.attempt.open(scope, me);
    expect(get(h.attempt).attempt?.target.name).toBe('first');
    expect(get(h.attempt).operation).toBeNull();
    expect(get(h.attempt).reviewingNew).toBe(false);
    expect(h.api.apply).not.toHaveBeenCalled();
    expect(h.api.byRequest).not.toHaveBeenCalled();
    await h.attempt.submit({ ...request, resource: { ...request.resource, metadata: { name: 'another-draft' } } }, h.reviewed);
    expect(h.api.apply).not.toHaveBeenCalled();
    expect(h.api.requestId).not.toHaveBeenCalled();
  });

  it.each(['controller', 'principal', 'scope', 'secret-payload'] as const)('hides unvalidated persisted data for a %s mismatch', async (patch) => {
    const h = harness();
    const value = { ...record, requestId: 'private-request-id', operationId: 'private-operation-id',
      ...(patch === 'controller' ? { binding: { ...identity, controllerId: 'other-controller' } } : {}),
      ...(patch === 'principal' ? { binding: { ...identity, userId: 'other-user' } } : {}),
      ...(patch === 'scope' ? { scope: { ...scope, kind: 'Other' } } : {}),
      ...(patch === 'secret-payload' ? { resource: request.resource } : {}),
    };
    h.values.set(applyAttemptKey(scope), JSON.stringify(value));
    await h.attempt.open(scope, me);
    expect(get(h.attempt).ready).toBe(false);
    expect(JSON.stringify(get(h.attempt))).not.toContain('private-');
    await h.attempt.submit(request, h.reviewed);
    expect(h.api.apply).not.toHaveBeenCalled();
  });

  it('blocks unsupported capability and inaccessible storage before any mutation', async () => {
    const h = harness();
    vi.mocked(h.api.binding).mockRejectedValueOnce(new Error('upgrade controller for durable request identity'));
    await h.attempt.open(scope, me);
    await h.attempt.submit(request, h.reviewed);
    expect(h.storage.getItem).not.toHaveBeenCalled();
    expect(h.api.apply).not.toHaveBeenCalled();
    h.storage.getItem.mockImplementationOnce(() => { throw new Error('storage denied'); });
    await h.attempt.verify();
    expect(get(h.attempt).ready).toBe(false);
    await h.attempt.submit(request, h.reviewed);
    expect(h.api.apply).not.toHaveBeenCalled();
  });

  it('refuses dispatch if persisting its recovery identity fails', async () => {
    const h = harness();
    await h.attempt.open(scope, me);
    h.storage.setItem.mockImplementation(() => { throw new Error('quota exceeded'); });
    await h.attempt.submit(request, h.reviewed);
    expect(get(h.attempt).attempt).toBeNull();
    expect(h.api.apply).not.toHaveBeenCalled();
  });

  it.each(['policy', 'review'] as const)('rechecks changed %s after asynchronous capability verification', async (changed) => {
    const h = harness();
    await h.attempt.open(scope, me);
    const pending = Promise.withResolvers<ReviewBinding>();
    vi.mocked(h.api.binding).mockReturnValueOnce(pending.promise);
    const submitted = h.attempt.submit(request, h.reviewed);
    if (changed === 'policy') h.policy.mockReturnValue(false);
    else h.reviewed.mockReturnValue(false);
    pending.resolve(identity);
    await submitted;
    expect(h.api.apply).not.toHaveBeenCalled();
    expect(h.storage.setItem).not.toHaveBeenCalled();
  });

  it('captures immutable reviewed intent before waiting on capability', async () => {
    const h = harness();
    await h.attempt.open(scope, me);
    const pending = Promise.withResolvers<ReviewBinding>();
    vi.mocked(h.api.binding).mockReturnValueOnce(pending.promise);
    const mutable = structuredClone(request);
    const submitted = h.attempt.submit(mutable, h.reviewed);
    mutable.resource.metadata!.name = 'changed-after-click';
    mutable.allowDestructive = false;
    pending.resolve(identity);
    await submitted;
    expect(vi.mocked(h.api.apply).mock.calls[0][0]).toEqual({ ...request, requestId: record.requestId });
  });

  it.each([{ requestId: 'other-key' }, { type: 'delete' }, { resourceName: 'other-target' }, { parentId: 'parent' }, { status: 'unrecognized' }])('rejects unrelated recovery evidence %j', async (patch) => {
    const h = harness(record);
    vi.mocked(h.api.byRequest).mockResolvedValue({ ...operation, ...patch, error: 'untrusted private result' });
    await h.attempt.open(scope, me);
    await h.attempt.recover();
    expect(get(h.attempt).operation).toBeNull();
    expect(JSON.stringify(get(h.attempt))).not.toContain('untrusted private result');
    expect(await h.attempt.reviewNew()).toBe(false);
    expect(h.api.apply).not.toHaveBeenCalled();
  });

  it.each([new ApiError(404, 'not accepted yet'), new ApiError(412, 'history pruned')])('does not turn unknown/pruned lookup into permission for a new key', async (error) => {
    const h = harness(record);
    vi.mocked(h.api.byRequest).mockRejectedValue(error);
    await h.attempt.open(scope, me);
    await h.attempt.recover();
    expect(await h.attempt.reviewNew()).toBe(false);
    await h.attempt.submit(request, h.reviewed);
    expect(h.api.apply).not.toHaveBeenCalled();
    expect(h.api.requestId).not.toHaveBeenCalled();
    expect(get(h.attempt).attempt?.requestId).toBe(record.requestId);
  });

  it('requires a terminal read and a separate explicit submission for a newly reviewed key', async () => {
    const h = harness(record);
    vi.mocked(h.api.byRequest).mockResolvedValueOnce({ ...operation, status: 'running' }).mockResolvedValue({ ...operation, status: 'failed' });
    await h.attempt.open(scope, me);
    expect(await h.attempt.reviewNew()).toBe(false);
    expect(await h.attempt.reviewNew()).toBe(true);
    expect(h.api.apply).not.toHaveBeenCalled();
    expect(h.api.requestId).not.toHaveBeenCalled();
    vi.mocked(h.api.requestId).mockReturnValue('request-new-review');
    const next = { ...request, allowDestructive: false, iKnowThisBreaksTheCluster: false };
    await h.attempt.submit(next, h.reviewed);
    expect(vi.mocked(h.api.apply).mock.calls[0][0]).toEqual({ ...next, requestId: 'request-new-review' });
    expect(get(h.attempt).attempt?.requestId).toBe('request-new-review');
  });

  it('keeps the previous recoverable identity if replacing its metadata fails', async () => {
    const h = harness({ ...record, operationId: operation.id });
    await h.attempt.open(scope, me);
    expect(await h.attempt.reviewNew()).toBe(true);
    vi.mocked(h.api.requestId).mockReturnValue('request-new-review');
    h.storage.setItem.mockImplementationOnce(() => {}).mockImplementationOnce(() => { throw new Error('quota exceeded'); });
    await h.attempt.submit(request, h.reviewed);
    expect(h.api.apply).not.toHaveBeenCalled();
    expect(get(h.attempt).attempt?.requestId).toBe(record.requestId);
    expect(JSON.parse(h.values.get(applyAttemptKey(scope))!).requestId).toBe(record.requestId);
  });

  it('ignores an old route completion and terminates private state on current-session 401', async () => {
    const h = harness(record);
    await h.attempt.open(scope, me);
    const old = Promise.withResolvers<Operation>();
    const started = Promise.withResolvers<void>();
    vi.mocked(h.api.byRequest).mockImplementationOnce(() => { started.resolve(); return old.promise; });
    const recovery = h.attempt.recover();
    await started.promise;
    await h.attempt.open({ ...scope, kind: 'Another' }, me);
    old.resolve(operation);
    await recovery;
    expect(get(h.attempt).attempt).toBeNull();
    expect(get(h.attempt).operation).toBeNull();
    await h.attempt.open(scope, me);
    vi.mocked(h.api.byRequest).mockRejectedValueOnce(new UnauthorizedError('expired'));
    await h.attempt.recover();
    expect(h.unauthorized).toHaveBeenCalledOnce();
    expect(get(h.attempt).attempt).toBeNull();
    expect(get(h.attempt).ready).toBe(false);
  });
});
