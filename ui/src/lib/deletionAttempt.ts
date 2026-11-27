import { writable, type Readable } from 'svelte/store';
import { ApiError, UnauthorizedError, operations, ping, resources, session, type DeleteResourceRequest, type DeleteResourceResponse, type Operation, type PingResponse, type RequestOptions, type WhoAmIResponse } from './api';
import type { DetailTarget } from './detailInspection';

export const DELETE_ATTEMPT_PREFIX = 'openctl.deleteAttempt:';
export interface DeleteBinding { controllerId: string; principalId: string }
export interface DeleteAttemptRecord {
  version: 1;
  binding: DeleteBinding;
  target: DetailTarget;
  requestId: string;
  operationId?: string;
}
export interface DeleteAttemptState {
  attempt: DeleteAttemptRecord | null;
  operation: Operation | null;
  ready: boolean;
  reviewable: boolean;
  busy: 'binding' | 'submitting' | 'recovering' | null;
  error: string;
}
export type DeleteAttemptStorage = Pick<Storage, 'getItem' | 'setItem'>;
export interface DeleteAttemptAPI {
  ping(opts: RequestOptions): Promise<PingResponse>;
  whoami(opts: RequestOptions): Promise<WhoAmIResponse>;
  delete(req: DeleteResourceRequest, opts: RequestOptions): Promise<DeleteResourceResponse>;
  byRequest(requestId: string, includeChildren: boolean, opts: RequestOptions): Promise<Operation>;
  requestId(): string;
}
export interface DeleteAttempt extends Readable<DeleteAttemptState> {
  open(target: DetailTarget, me: WhoAmIResponse | null): Promise<void>;
  verify(): Promise<void>;
  submit(): Promise<void>;
  reviewNew(): Promise<void>;
  recover(): Promise<void>;
  observe(operation: Operation): void;
  close(): void;
}
interface Scope {
  target: DetailTarget;
  me: WhoAmIResponse;
  fingerprint: string;
  controller: AbortController;
  binding: DeleteBinding | null;
}
const defaultAPI: DeleteAttemptAPI = {
  ping: (opts) => ping.ping('', opts),
  whoami: (opts) => session.whoami(opts),
  delete: (req, opts) => resources.delete(req, opts),
  byRequest: (key, children, opts) => operations.byRequest(key, children, opts),
  requestId: () => crypto.randomUUID(),
};
const empty = (): DeleteAttemptState => ({ attempt: null, operation: null, ready: false, reviewable: false, busy: null, error: '' });
const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v);
const text = (v: unknown, max = 512): v is string => typeof v === 'string' && v.trim().length > 0 && v.length <= max;
const sameTarget = (a: DetailTarget, b: DetailTarget) => a.apiVersion === b.apiVersion && a.kind === b.kind && a.name === b.name;
const sameBinding = (a: DeleteBinding, b: DeleteBinding) => a.controllerId === b.controllerId && a.principalId === b.principalId;
const terminal = (operation: Operation) => ['succeeded', 'failed', 'interrupted', 'canceled'].includes(operation.status);
export const deleteAttemptKey = (target: DetailTarget): string => DELETE_ATTEMPT_PREFIX + JSON.stringify([target.apiVersion, target.kind, target.name]);

function readAttempt(raw: string | null, binding: DeleteBinding, target: DetailTarget): DeleteAttemptRecord | null {
  if (raw === null) return null;
  const invalid = () => new Error('This tab has a deletion record that cannot be validated for the current controller, signed-in user and target. Return to its original binding to recover it. No Delete was sent.');
  if (raw.length > 8192) throw invalid();
  let v: unknown;
  try { v = JSON.parse(raw); } catch { throw invalid(); }
  if (!object(v) || v.version !== 1 || !object(v.binding) || !object(v.target)
    || !text(v.binding.controllerId) || !text(v.binding.principalId)
    || !text(v.target.apiVersion) || !text(v.target.kind) || !text(v.target.name)
    || !text(v.requestId, 128) || !/^[\x20-\x7e]+$/.test(v.requestId)
    || (v.operationId !== undefined && !text(v.operationId, 128))
    || Object.keys(v).some((key) => !['version', 'binding', 'target', 'requestId', 'operationId'].includes(key))
    || Object.keys(v.binding).some((key) => !['controllerId', 'principalId'].includes(key))
    || Object.keys(v.target).some((key) => !['apiVersion', 'kind', 'name'].includes(key))
    || !sameBinding(v.binding as unknown as DeleteBinding, binding) || !sameTarget(v.target as unknown as DetailTarget, target)) throw invalid();
  return { version: 1, binding: { ...binding }, target: { ...target }, requestId: v.requestId,
    ...(v.operationId === undefined ? {} : { operationId: v.operationId as string }) };
}

export function validateDeleteOperation(operation: Operation, attempt: DeleteAttemptRecord): void {
  if (!text(operation.id, 128) || (attempt.operationId && operation.id !== attempt.operationId)
    || operation.parentId || operation.type !== 'delete' || operation.requestId !== attempt.requestId
    || operation.apiVersion !== attempt.target.apiVersion || operation.kind !== attempt.target.kind
    || operation.resourceName !== attempt.target.name
    || !['pending', 'running', 'succeeded', 'failed', 'interrupted', 'canceled'].includes(operation.status)) {
    throw new Error('The returned operation does not match this root Delete request, action and target. Recovery is blocked; no new Delete was sent.');
  }
}

// Storage contains only request identity. Restoring it grants neither consent,
// provider readiness, nor permission to send another mutation. Unknown admission
// stays recovery-only: a 404 can race the original request reaching the server.
export function createDeleteAttempt(options: {
  storage: () => DeleteAttemptStorage;
  canDelete: () => boolean;
  canReviewNew: (operation: Operation) => boolean;
  refreshAfterTerminal: () => Promise<void>;
  confirm: (target: DetailTarget, newAttempt: boolean) => string | null;
  onUnauthorized: () => void;
  api?: DeleteAttemptAPI;
}): DeleteAttempt {
  const api = options.api ?? defaultAPI;
  let scope: Scope | null = null;
  let state = empty();
  const store = writable(state);
  const publish = (next: DeleteAttemptState) => { state = next; store.set(next); };
  const owns = (s: Scope) => scope === s && !s.controller.signal.aborted;
  function close() { scope?.controller.abort(); scope = null; publish(empty()); }
  function revoke() { close(); options.onUnauthorized(); }
  function fail(s: Scope, err: unknown, prefix = '') {
    if (!owns(s)) return;
    if (err instanceof UnauthorizedError) { revoke(); return; }
    publish({ ...state, busy: null, error: prefix + (err instanceof Error ? err.message : String(err)) });
  }
  async function binding(s: Scope): Promise<DeleteBinding | null> {
    const opts = { signal: s.controller.signal };
    const guard = <T>(p: Promise<T>) => p.catch((err: unknown) => {
      if (owns(s) && err instanceof UnauthorizedError) revoke();
      throw err;
    });
    const [controller, me] = await Promise.all([guard(api.ping(opts)), guard(api.whoami(opts))]);
    if (!owns(s)) return null;
    if (!controller.supportsIdempotentDelete || !text(controller.controllerId) || !text(me.principalId)) {
      publish({ ...empty(), error: 'This controller cannot safely recover Delete requests. A durable-delete capable controller, stable controller ID and canonical signed-in principal are required. No Delete was sent.' });
      return null;
    }
    const next = { controllerId: controller.controllerId, principalId: me.principalId };
    if (me.principalId !== s.me.principalId || me.sessionId !== s.me.sessionId || (s.binding && !sameBinding(s.binding, next))) {
      publish({ ...empty(), error: 'The controller or signed-in session changed. Deletion data is hidden; return to the original binding to recover it. No Delete was sent.' });
      return null;
    }
    // A freshly revoked role must also gate submission, even before the shell
    // refreshes its shared auth store. Viewers may still perform recovery.
    s.me = { ...s.me, role: me.role };
    s.binding = next;
    return next;
  }
  async function verify() {
    const s = scope;
    if (!s || state.busy) return;
    publish({ ...empty(), busy: 'binding' });
    try {
      const current = await binding(s);
      if (!current || !owns(s)) return;
      const attempt = readAttempt(options.storage().getItem(deleteAttemptKey(s.target)), current, s.target);
      if (owns(s)) publish({ ...empty(), ready: true, attempt });
    } catch (err) { fail(s, err, 'Cannot verify this tab’s deletion recovery record. '); }
  }
  async function open(target: DetailTarget, me: WhoAmIResponse | null) {
    const fingerprint = JSON.stringify([target, me]);
    if (scope?.fingerprint === fingerprint) return;
    close();
    if (!me) return;
    if (!text(me.principalId)) {
      publish({ ...empty(), error: 'A canonical signed-in principal is required for recoverable deletion. No Delete was sent.' });
      return;
    }
    scope = { target: { ...target }, me: { ...me }, fingerprint, controller: new AbortController(), binding: null };
    await verify();
  }
  function persist(s: Scope, attempt: DeleteAttemptRecord) {
    if (!owns(s)) return;
    try { options.storage().setItem(deleteAttemptKey(s.target), JSON.stringify(attempt)); } catch {
      throw new Error('Cannot persist this tab’s deletion recovery record. Submission is blocked; accepted controller work, if any, is not canceled.');
    }
    if (owns(s)) publish({ ...state, attempt, ...(state.attempt?.requestId !== attempt.requestId ? { operation: null, reviewable: false } : {}) });
  }
  async function submit(reviewNew = false) {
    const s = scope;
    const previous = state.attempt;
    if (!s || !state.ready || state.busy || !options.canDelete()) return;
    if (previous ? !reviewNew || !state.reviewable || !state.operation || !options.canReviewNew(state.operation) : reviewNew) return;
    publish({ ...state, busy: 'submitting', error: '' });
    try {
      const current = await binding(s);
      if (!current || !owns(s) || !options.canDelete() || s.me.role === 'viewer') return;
      if (previous) {
        // Never turn cached status, a stream event, or an unknown/pruned lookup
        // into permission for a new key. Re-establish the original outcome now.
        publish({ ...state, reviewable: false });
        const operation = await api.byRequest(previous.requestId, true, { signal: s.controller.signal });
        if (!owns(s)) return;
        validateDeleteOperation(operation, previous);
        publish({ ...state, operation });
        if (!terminal(operation)) throw new Error('The original Delete is not terminal. Only recovery is available; no new Delete was sent.');
        await options.refreshAfterTerminal();
        if (!owns(s) || !options.canReviewNew(operation) || !options.canDelete()) return;
        publish({ ...state, reviewable: true });
      }
      const typed = options.confirm(s.target, !!previous);
      if (!owns(s)) return;
      if (typed !== s.target.name) {
        if (typed !== null) publish({ ...state, error: 'Name did not match; delete canceled.' });
        return;
      }
      if (!options.canDelete() || s.me.role === 'viewer' || (previous && (!state.operation || !options.canReviewNew(state.operation)))) return;
      const attempt: DeleteAttemptRecord = { version: 1, binding: { ...current }, target: { ...s.target }, requestId: api.requestId() };
      if (!text(attempt.requestId, 128) || !/^[\x20-\x7e]+$/.test(attempt.requestId) || attempt.requestId === previous?.requestId) {
        throw new Error('Cannot create a fresh valid Delete request identity. No new Delete was sent.');
      }
      persist(s, attempt); // Keep the original until this replacement safely persists.
      if (!owns(s) || !options.canDelete()) return;
      const response = await api.delete({ ...s.target, requestId: attempt.requestId }, { signal: s.controller.signal });
      if (!owns(s)) return;
      if (response.requestId !== attempt.requestId || !text(response.operationId, 128)) {
        throw new Error('The Delete acknowledgement did not match the saved request identity. Admission is uncertain; use Recover request.');
      }
      persist(s, { ...attempt, operationId: response.operationId });
    } catch (err) {
      if (owns(s) && previous && state.attempt?.requestId === previous.requestId) publish({ ...state, reviewable: false });
      fail(s, err, 'Keep the recorded request and use Recover request. ');
    } finally { if (owns(s)) publish({ ...state, busy: null }); }
  }
  async function recover() {
    const s = scope;
    const attempt = state.attempt;
    if (!s || !attempt || state.busy || !state.ready) return;
    publish({ ...state, busy: 'recovering', reviewable: false, error: '' });
    try {
      if (!await binding(s) || !owns(s)) return;
      const operation = await api.byRequest(attempt.requestId, true, { signal: s.controller.signal });
      if (!owns(s)) return;
      validateDeleteOperation(operation, attempt);
      // Keep verified evidence visible even if saving the acknowledgement fails;
      // the original request identity is already durable in this tab.
      publish({ ...state, operation });
      persist(s, { ...attempt, operationId: operation.id });
      if (terminal(operation)) {
        await options.refreshAfterTerminal();
        if (owns(s)) publish({ ...state, reviewable: options.canReviewNew(operation) });
      }
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        fail(s, new Error('Request not found. The original request may still be admitted later. Keep recovering this same request; no new Delete was sent.'));
      } else fail(s, err);
    } finally { if (owns(s)) publish({ ...state, busy: null }); }
  }
  function observe(operation: Operation) {
    const attempt = state.attempt;
    if (!scope || !owns(scope) || !attempt?.operationId || operation.id !== attempt.operationId) return;
    try { validateDeleteOperation(operation, attempt); } catch { return; }
    publish({ ...state, operation, reviewable: state.reviewable && terminal(operation) });
  }
  return { subscribe: store.subscribe, open, verify, submit, reviewNew: () => submit(true), recover, observe, close };
}
