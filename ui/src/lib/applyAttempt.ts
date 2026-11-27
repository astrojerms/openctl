import { writable, type Readable } from 'svelte/store';
import { ApiError, UnauthorizedError, operations, resources, type ApplyRequest, type ApplyResponse, type Operation, type RequestOptions, type ResourceRef, type WhoAmIResponse } from './api';
import { loadReviewBinding, type ReviewBinding } from './deploymentReview';

export const APPLY_ATTEMPT_PREFIX = 'openctl.applyAttempt:';
export type ApplyAttemptScope =
  | { source: 'resource'; apiVersion: string; kind: string; name: string }
  | { source: 'create'; apiVersion: string; kind: string }
  | { source: 'template'; template: string };
export interface ApplyAttemptRecord {
  version: 1;
  binding: ReviewBinding;
  scope: ApplyAttemptScope;
  target: ResourceRef;
  requestId: string;
  operationId?: string;
}
export interface ApplyAttemptState {
  attempt: ApplyAttemptRecord | null;
  operation: Operation | null;
  ready: boolean;
  busy: 'binding' | 'submitting' | 'recovering' | null;
  reviewingNew: boolean;
  error: string;
}
export type ApplyAttemptStorage = Pick<Storage, 'getItem' | 'setItem'>;
export interface ApplyAttemptAPI {
  binding(opts: RequestOptions): Promise<ReviewBinding>;
  apply(request: ApplyRequest, opts: RequestOptions): Promise<ApplyResponse>;
  byRequest(requestId: string, includeChildren: boolean, opts: RequestOptions): Promise<Operation>;
  requestId(): string;
}
export interface ApplyAttempt extends Readable<ApplyAttemptState> {
  open(scope: ApplyAttemptScope, me: WhoAmIResponse | null): Promise<void>;
  verify(): Promise<void>;
  submit(request: ApplyRequest, stillReviewed: () => boolean): Promise<void>;
  recover(): Promise<void>;
  reviewNew(): Promise<boolean>;
  observe(operation: Operation): void;
  close(): void;
}
interface Scope {
  scope: ApplyAttemptScope;
  principalId: string;
  fingerprint: string;
  controller: AbortController;
  binding: ReviewBinding | null;
}
const defaultAPI: ApplyAttemptAPI = {
  binding: loadReviewBinding,
  apply: (request, opts) => resources.apply(request, opts),
  byRequest: (key, children, opts) => operations.byRequest(key, children, opts),
  requestId: () => crypto.randomUUID(),
};
const empty = (): ApplyAttemptState => ({ attempt: null, operation: null, ready: false, busy: null, reviewingNew: false, error: '' });
const object = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v);
const text = (v: unknown, max = 512): v is string => typeof v === 'string' && v.length > 0 && v.length <= max;
const requestKey = (v: unknown): v is string => text(v, 128) && /^[\x20-\x7e]+$/.test(v);
const terminal = (op: Operation) => ['succeeded', 'failed', 'interrupted', 'canceled'].includes(op.status);
const sameBinding = (a: ReviewBinding, b: ReviewBinding) => a.controllerId === b.controllerId && a.userId === b.userId;
const keys = (v: Record<string, unknown>, allowed: string[]) => Object.keys(v).every((key) => allowed.includes(key));
function scopeParts(scope: ApplyAttemptScope): string[] {
  return scope.source === 'template' ? [scope.source, scope.template]
    : scope.source === 'resource' ? [scope.source, scope.apiVersion, scope.kind, scope.name]
      : [scope.source, scope.apiVersion, scope.kind];
}
export const applyAttemptKey = (scope: ApplyAttemptScope) => APPLY_ATTEMPT_PREFIX + JSON.stringify(scopeParts(scope));
function matchesTarget(scope: ApplyAttemptScope, target: ResourceRef) {
  return text(target.apiVersion) && text(target.kind) && text(target.name)
    && (scope.source === 'template' || (scope.apiVersion === target.apiVersion && scope.kind === target.kind
      && (scope.source === 'create' || scope.name === target.name)));
}
function readAttempt(raw: string | null, scope: ApplyAttemptScope, binding: ReviewBinding): ApplyAttemptRecord | null {
  if (raw === null) return null;
  const invalid = () => new Error('This tab’s Apply record cannot be validated for the current controller, principal and review surface. Return to its original binding to recover it. No Apply was sent.');
  if (raw.length > 8192) throw invalid();
  let value: unknown;
  try { value = JSON.parse(raw); } catch { throw invalid(); }
  if (!object(value) || value.version !== 1 || !keys(value, ['version', 'binding', 'scope', 'target', 'requestId', 'operationId'])
    || !object(value.binding) || !keys(value.binding, ['controllerId', 'userId'])
    || !text(value.binding.controllerId) || !text(value.binding.userId)
    || !sameBinding(value.binding as unknown as ReviewBinding, binding)
    || !object(value.scope) || JSON.stringify(value.scope) !== JSON.stringify(scope)
    || !object(value.target) || !keys(value.target, ['apiVersion', 'kind', 'name'])
    || !matchesTarget(scope, value.target as unknown as ResourceRef)
    || !requestKey(value.requestId) || (value.operationId !== undefined && !text(value.operationId, 128))) throw invalid();
  return { version: 1, binding: { ...binding }, scope: { ...scope },
    target: { apiVersion: String(value.target.apiVersion), kind: String(value.target.kind), name: String(value.target.name) },
    requestId: value.requestId, ...(value.operationId === undefined ? {} : { operationId: value.operationId as string }) };
}
export function validateApplyOperation(operation: Operation, record: ApplyAttemptRecord): void {
  if (!text(operation.id, 128) || (record.operationId && operation.id !== record.operationId)
    || operation.parentId || operation.type !== 'apply' || operation.requestId !== record.requestId
    || operation.apiVersion !== record.target.apiVersion || operation.kind !== record.target.kind || operation.resourceName !== record.target.name
    || !['pending', 'running', 'succeeded', 'failed', 'interrupted', 'canceled'].includes(operation.status)) {
    throw new Error('The returned operation does not match this root Apply request and target. No new Apply was sent.');
  }
}

// Recovery storage contains identity only: never drafts, credentials, flags or
// consent. A restored or unknown request grants no authority to submit again.
export function createApplyAttempt(options: {
  storage: () => ApplyAttemptStorage;
  canSubmit: () => boolean;
  onUnauthorized: () => void;
  api?: ApplyAttemptAPI;
}): ApplyAttempt {
  const api = options.api ?? defaultAPI;
  let scope: Scope | null = null;
  let state = empty();
  const store = writable(state);
  const publish = (next: ApplyAttemptState) => { state = next; store.set(next); };
  const owns = (current: Scope) => scope === current && !current.controller.signal.aborted;
  function close() { scope?.controller.abort(); scope = null; publish(empty()); }
  function fail(current: Scope, error: unknown) {
    if (!owns(current)) return;
    if (error instanceof UnauthorizedError) { close(); options.onUnauthorized(); return; }
    publish({ ...state, busy: null, error: error instanceof Error ? error.message : String(error) });
  }
  async function binding(current: Scope) {
    const next = await api.binding({ signal: current.controller.signal });
    if (!owns(current)) return null;
    if (!text(next.controllerId) || !text(next.userId) || next.userId !== current.principalId
      || (current.binding && !sameBinding(current.binding, next))) {
      publish({ ...empty(), error: 'Controller or principal changed. Apply recovery is hidden; return to its original binding. No Apply was sent.' });
      return null;
    }
    current.binding = next;
    return next;
  }
  async function verify() {
    const current = scope;
    if (!current || state.busy) return;
    publish({ ...empty(), busy: 'binding' });
    try {
      const identity = await binding(current);
      if (!identity || !owns(current)) return;
      const record = readAttempt(options.storage().getItem(applyAttemptKey(current.scope)), current.scope, identity);
      if (owns(current)) publish({ ...empty(), attempt: record, ready: true });
    } catch (error) { fail(current, error); }
  }
  async function open(target: ApplyAttemptScope, me: WhoAmIResponse | null) {
    const fingerprint = JSON.stringify([target, me]);
    if (scope?.fingerprint === fingerprint) return;
    close();
    if (!me) return;
    if (!text(me.principalId)) {
      publish({ ...empty(), error: 'A canonical signed-in principal is required for recoverable Apply. No Apply was sent.' });
      return;
    }
    scope = { scope: { ...target }, principalId: me.principalId, fingerprint, controller: new AbortController(), binding: null };
    await verify();
  }
  function persist(current: Scope, record: ApplyAttemptRecord) {
    options.storage().setItem(applyAttemptKey(current.scope), JSON.stringify(record));
    if (owns(current)) publish({ ...state, attempt: record,
      ...(state.attempt?.requestId !== record.requestId ? { operation: null, reviewingNew: false } : {}) });
  }
  async function accepted(current: Scope, record: ApplyAttemptRecord) {
    const operation = await api.byRequest(record.requestId, true, { signal: current.controller.signal });
    if (!owns(current)) return null;
    validateApplyOperation(operation, record);
    publish({ ...state, operation });
    persist(current, { ...record, operationId: operation.id });
    return operation;
  }
  async function submit(request: ApplyRequest, stillReviewed: () => boolean) {
    const current = scope;
    const previous = state.attempt;
    if (!current || !state.ready || state.busy || !options.canSubmit() || !stillReviewed()
      || (previous && !state.reviewingNew)) return;
    // Capture before capability/recovery awaits; form models may remain mutable.
    const snapshot = structuredClone(request);
    const resource = snapshot.resource;
    const target = { apiVersion: resource.apiVersion ?? '', kind: resource.kind ?? '', name: resource.metadata?.name ?? '' };
    publish({ ...state, busy: 'submitting', error: '' });
    try {
      if (!matchesTarget(current.scope, target)) throw new Error('Reviewed Apply target does not match this surface. No Apply was sent.');
      const identity = await binding(current);
      if (!identity || !owns(current) || !options.canSubmit() || !stillReviewed()) return;
      if (previous) {
        const operation = await accepted(current, previous);
        if (!operation || !owns(current)) return;
        if (!terminal(operation)) throw new Error('The original Apply is not terminal. Recover it before reviewing new work.');
      }
      if (!owns(current) || !options.canSubmit() || !stillReviewed()) return;
      const record: ApplyAttemptRecord = { version: 1, binding: { ...identity }, scope: { ...current.scope }, target, requestId: api.requestId() };
      if (!requestKey(record.requestId) || record.requestId === previous?.requestId) throw new Error('A fresh valid request identity is required. No Apply was sent.');
      persist(current, record); // A failed write leaves the previous identity intact.
      if (!owns(current) || !options.canSubmit() || !stillReviewed()) return;
      const response = await api.apply({ ...snapshot, requestId: record.requestId }, { signal: current.controller.signal });
      if (!owns(current)) return;
      if (response.requestId !== record.requestId || !text(response.operationId, 128)) {
        throw new Error('Apply acknowledgement did not match the saved identity. Admission is uncertain; recover the recorded request.');
      }
      persist(current, { ...record, operationId: response.operationId });
    } catch (error) { fail(current, error); }
    finally { if (owns(current)) publish({ ...state, busy: null }); }
  }
  async function recover() {
    const current = scope;
    const record = state.attempt;
    if (!current || !record || !state.ready || state.busy) return;
    publish({ ...state, busy: 'recovering', reviewingNew: false, error: '' });
    try {
      if (!await binding(current) || !owns(current)) return;
      await accepted(current, record);
    } catch (error) {
      fail(current, error instanceof ApiError && error.status === 404
        ? new Error('Request not found. The original Apply may still be admitted later. Keep recovering this identity; no new Apply was sent.') : error);
    } finally { if (owns(current)) publish({ ...state, busy: null }); }
  }
  async function reviewNew() {
    const current = scope;
    const record = state.attempt;
    if (!current || !record || !state.ready || state.busy || !options.canSubmit()) return false;
    publish({ ...state, busy: 'recovering', reviewingNew: false, error: '' });
    try {
      if (!await binding(current) || !owns(current)) return false;
      const operation = await accepted(current, record);
      if (!operation || !owns(current)) return false;
      if (!terminal(operation)) throw new Error('Only a confirmed terminal Apply permits a new review. Unknown or active requests remain recovery-only.');
      publish({ ...state, reviewingNew: true });
      return true;
    } catch (error) { fail(current, error); return false; }
    finally { if (owns(current)) publish({ ...state, busy: null }); }
  }
  function observe(operation: Operation) {
    const record = state.attempt;
    if (!scope || !owns(scope) || !record?.operationId || operation.id !== record.operationId) return;
    try { validateApplyOperation(operation, record); } catch { return; }
    publish({ ...state, operation, reviewingNew: state.reviewingNew && terminal(operation) });
  }
  return { subscribe: store.subscribe, open, verify, submit, recover, reviewNew, observe, close };
}
