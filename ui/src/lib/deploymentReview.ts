import { ApiError, operations, ping, resources, schemas, session, UnauthorizedError, type ApplyRequest, type DryRunApplyResponse, type GetResourceResponse, type Operation, type RequestOptions, type Resource } from './api';
import { APP_API, VM_API, type DeploymentStep } from './jellyfin';
import { withoutRequestConsent } from './retry';
import { watchOperations, type OperationEvent } from './watch';
import { applicationChecks, applicationReadiness } from './detailState';
import type { AppCheck } from './detailState';
import type { ResourceRef } from './api';

export const REVIEW_KEY = 'openctl.jellyfinReview';
export const MAX_REVIEW_BYTES = 128 * 1024;
type ReviewStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>;
export interface ReviewBinding { controllerId: string; userId: string }
interface Attempt {
  requestId: string;
  operationId?: string;
  createOnly: boolean;
  allowDestructive: boolean;
  iKnowThisBreaksTheCluster: boolean;
}
export interface SavedReview {
  version: 1;
  binding: ReviewBinding;
  cursor: number;
  steps: (DeploymentStep & { attempt?: Attempt })[];
}
export interface ReviewBlocker {
  target: ResourceRef;
  code: 'operation_unverified' | 'observation_unavailable' | 'baseline_changed' | 'readiness_unproven';
  message: string;
  detail: string;
  checks: AppCheck[];
}
export interface ReviewState {
  saved: SavedReview;
  operation: Operation | null;
  preview: DryRunApplyResponse | null;
  observed: Resource | null;
  blocker: ReviewBlocker | null;
  verified: boolean;
}
export interface ReviewConsent {
  dedicated: boolean;
  trustedKey: boolean;
  publicExposure: boolean;
  gates: Record<string, boolean>;
}
export interface ReviewAPI {
  binding: () => Promise<ReviewBinding>;
  getOperation: typeof operations.get;
  byRequest: typeof operations.byRequest;
  getResource: typeof resources.get;
  validate: typeof schemas.validate;
  preview: typeof resources.dryRunApply;
  apply: typeof resources.apply;
  watch: typeof watchOperations;
  requestId: () => string;
}
export async function loadReviewBinding(opts?: RequestOptions): Promise<ReviewBinding> {
  const [controller, me] = await Promise.all([ping.ping('', opts), session.whoami(opts)]);
  if (!controller.supportsIdempotentApply || !controller.controllerId || !me.principalId) {
    throw new Error('This controller cannot safely recover Apply requests. Upgrade to a controller with durable request identity before submitting.');
  }
  return { controllerId: controller.controllerId, userId: me.principalId };
}
const defaultAPI: ReviewAPI = {
  binding: loadReviewBinding, getOperation: operations.get, byRequest: operations.byRequest,
  getResource: resources.get, validate: schemas.validate, preview: resources.dryRunApply,
  apply: resources.apply, watch: watchOperations, requestId: () => crypto.randomUUID(),
};
const terminal = (op: Operation) => ['succeeded', 'failed', 'interrupted', 'canceled'].includes(op.status);
export const failedReviewOperation = (op: Operation | null) => !!op && ['failed', 'interrupted', 'canceled'].includes(op.status);
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === 'object' && !Array.isArray(value);
const text = (value: unknown, max = 512): value is string => typeof value === 'string' && value.length > 0 && value.length <= max;
const requestKey = (value: unknown): value is string => text(value, 128) && /^[\x20-\x7e]+$/.test(value);
const sameBinding = (a: ReviewBinding, b: ReviewBinding) => a.controllerId === b.controllerId && a.userId === b.userId;

// Desired intent comparisons ignore object key ordering, observed fields and
// request-scoped consent, but never resolve or discard reference markers.
function ordered(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(ordered);
  if (!object(value)) return value;
  return Object.fromEntries(Object.keys(value).sort().map((key) => [key, ordered(value[key])]));
}
function desired(resource: Resource): Resource {
  const clean = withoutRequestConsent(resource);
  return { apiVersion: clean.apiVersion, kind: clean.kind, metadata: clean.metadata, spec: clean.spec ?? {} };
}
function signature(value: unknown): string { return JSON.stringify(ordered(value)); }
function desiredSignature(resource: Resource): string {
  const clean = desired(resource);
  // Protobuf JSON emits these empty collections even when authored intent omits them.
  clean.metadata = { ...clean.metadata, labels: clean.metadata.labels ?? {}, annotations: clean.metadata.annotations ?? {}, ownerRefs: clean.metadata.ownerRefs ?? [] };
  return signature(clean);
}
function sameDesired(a: Resource, b: Resource): boolean { return desiredSignature(a) === desiredSignature(b); }
function sameTarget(a: Resource, b: Resource): boolean {
  return a.apiVersion === b.apiVersion && a.kind === b.kind && a.metadata.name === b.metadata.name;
}
function safeJSON(value: unknown, depth = 0): void {
  if (depth > 24) throw new Error('The saved review is too deeply nested.');
  if (typeof value === 'string' && (/-----BEGIN .*PRIVATE KEY/.test(value) || value.length > 16384)) {
    throw new Error('The saved review contains unsupported credential material or oversized text.');
  }
  if (Array.isArray(value)) { value.forEach((entry) => safeJSON(entry, depth + 1)); return; }
  if (!object(value)) return;
  for (const [key, entry] of Object.entries(value)) {
    if (['__proto__', 'prototype', 'constructor'].includes(key)) throw new Error('Invalid saved review field.');
    if (/^(password|credentials|token|privateKey)$/i.test(key) && (!object(entry) || !object(entry.$secret))) {
      throw new Error('Only controller-side secret references may be saved.');
    }
    safeJSON(entry, depth + 1);
  }
}
function validateSaved(value: unknown, binding: ReviewBinding): SavedReview {
  if (!object(value) || value.version !== 1 || !object(value.binding)
    || !text(value.binding.controllerId) || !text(value.binding.userId)
    || !sameBinding(value.binding as unknown as ReviewBinding, binding)) {
    throw new Error('This saved review has an unsupported version or belongs to a different controller or signed-in user. Discard it to start a new review.');
  }
  if (!Array.isArray(value.steps) || value.steps.length < 1 || value.steps.length > 5
    || !Number.isInteger(value.cursor) || (value.cursor as number) < 0 || (value.cursor as number) >= value.steps.length
    || Object.keys(value).some((key) => !['version', 'binding', 'cursor', 'steps'].includes(key))) {
    throw new Error('The saved review is malformed. Discard it and prepare the declarations again.');
  }
  safeJSON(value);
  const seenRequests = new Set<string>();
  const steps = value.steps.map((entry) => {
    if (!object(entry) || !text(entry.title, 160) || typeof entry.createOnly !== 'boolean'
      || Object.keys(entry).some((key) => !['title', 'createOnly', 'resource', 'attempt'].includes(key))
      || !object(entry.resource) || !text(entry.resource.apiVersion) || !text(entry.resource.kind)
      || !object(entry.resource.metadata) || !text(entry.resource.metadata.name) || !object(entry.resource.spec)
      || Object.keys(entry.resource).some((key) => !['apiVersion', 'kind', 'metadata', 'spec'].includes(key))) {
      throw new Error('The saved review contains an invalid desired resource.');
    }
    if (entry.attempt !== undefined) {
      const attempt = entry.attempt;
      if (!object(attempt) || !requestKey(attempt.requestId) || seenRequests.has(attempt.requestId)
        || (attempt.operationId !== undefined && !text(attempt.operationId, 128))
        || ['createOnly', 'allowDestructive', 'iKnowThisBreaksTheCluster'].some((key) => typeof attempt[key] !== 'boolean')
        || Object.keys(attempt).some((key) => !['requestId', 'operationId', 'createOnly', 'allowDestructive', 'iKnowThisBreaksTheCluster'].includes(key))) {
        throw new Error('The saved review contains an invalid request identity.');
      }
      seenRequests.add(attempt.requestId);
    }
    return { ...entry, resource: desired(entry.resource as unknown as Resource) } as unknown as SavedReview['steps'][number];
  });
  const offset = steps[0].resource.kind === 'VirtualMachine' ? 1 : 0;
  const expected = offset ? [`${VM_API}/VirtualMachine`, `${APP_API}/Jellyfin`] : [`${APP_API}/Jellyfin`];
  if (steps.length > expected.length) expected.push('cloudflare.openctl.io/v1/TunnelRoute', 'cloudflare.openctl.io/v1/DNSRecord', `${APP_API}/Jellyfin`);
  if (steps.length !== expected.length || steps.some((step, i) => `${step.resource.apiVersion}/${step.resource.kind}` !== expected[i]
    || step.createOnly !== (i !== steps.length - 1 || steps.length === offset + 1))) {
    throw new Error('The saved review is not a supported Jellyfin deployment sequence.');
  }
  const app = steps[offset].resource;
  if (steps.length > offset + 1 && !sameTarget(app, steps[steps.length - 1].resource)) throw new Error('Saved application identities do not match.');
  if (steps.some((step, i) => i < (value.cursor as number) && !step.attempt)
    || steps.some((step, i) => i > (value.cursor as number) && step.attempt)) throw new Error('The saved review cursor does not match its requests.');
  return { version: 1, binding: { controllerId: binding.controllerId, userId: binding.userId }, cursor: value.cursor as number, steps };
}
export function createSavedReview(binding: ReviewBinding, steps: DeploymentStep[]): SavedReview {
  return validateSaved({ version: 1, binding, cursor: 0, steps: steps.map((step) => ({ ...step, resource: desired(step.resource) })) }, binding);
}
export function readSavedReview(storage: ReviewStorage, binding: ReviewBinding): SavedReview | null {
  const raw = storage.getItem(REVIEW_KEY);
  if (raw === null) return null;
  if (new TextEncoder().encode(raw).byteLength > MAX_REVIEW_BYTES) throw new Error('The saved review is too large. Discard it before starting again.');
  let value: unknown;
  try { value = JSON.parse(raw); } catch { throw new Error('The saved review is unreadable. Discard it before starting again.'); }
  return validateSaved(value, binding);
}
export function discardSavedReview(storage: ReviewStorage): void { storage.removeItem(REVIEW_KEY); }
export function reviewError(error: unknown): string {
  if (error instanceof ApiError && (error.status === 400 || error.status === 409)) return 'The controller rejected this recovery request. Operation history may have expired or the current intent may conflict. Inspect the original operation and existing resources before preparing a new review. No replacement Apply was submitted.';
  if (error instanceof ApiError) return `Controller request failed (HTTP ${error.status}). Recovery is blocked; restore access and look up the saved request again. No replacement Apply was submitted.`;
  return error instanceof Error ? error.message : 'The review could not be recovered. No replacement Apply was submitted.';
}

export function validateReviewOperation(op: Operation, step: SavedReview['steps'][number]): void {
  const attempt = step.attempt;
  if (!attempt || !text(op.id, 128) || (attempt.operationId && op.id !== attempt.operationId)
    || op.parentId || op.type !== 'apply' || op.requestId !== attempt.requestId
    || op.apiVersion !== step.resource.apiVersion || op.kind !== step.resource.kind || op.resourceName !== step.resource.metadata.name
    || !['pending', 'running', 'succeeded', 'failed', 'interrupted', 'canceled'].includes(op.status)) {
    throw new Error('The controller operation does not match this saved root Apply request. Inspect operation history; do not submit a replacement.');
  }
  let manifest: Resource;
  try { manifest = JSON.parse(op.manifestJson ?? ''); } catch { throw new Error('The operation has no valid submitted declaration. Recovery is blocked.'); }
  if (!object(manifest) || !object(manifest.metadata) || !sameTarget(manifest, step.resource) || !sameDesired(manifest, step.resource)) {
    throw new Error('The operation declaration differs from the saved review. Recovery is blocked.');
  }
}
export function isReviewRootEvent(event: OperationEvent, root: Operation): boolean {
  const op = event.operation;
  if (op.id !== root.id) return false; // A child can finish before its root.
  if (op.parentId || op.type !== 'apply' || op.apiVersion !== root.apiVersion || op.kind !== root.kind || op.resourceName !== root.resourceName) {
    throw new Error('The operation stream returned a mismatched root identity. Look up the saved request again.');
  }
  return true;
}

/** Owns recovery transitions; storage contains intent/identities, never evidence. */
export class DeploymentReview {
  state: ReviewState;
  private planBaseline: string | null = null;
  private plannedCreateOnly = true;
  private watcher: AbortController | null = null;
  private disposed = false;
  constructor(private storage: ReviewStorage, saved: SavedReview, private options: {
    canApply: () => boolean; changed?: (state: ReviewState) => void; api?: ReviewAPI;
  }) {
    this.state = { saved: validateSaved(saved, saved.binding), operation: null, preview: null, observed: null, blocker: null, verified: false };
  }
  private get api(): ReviewAPI { return this.options.api ?? defaultAPI; }
  private get step() { return this.state.saved.steps[this.state.saved.cursor]; }
  private emit(): void { if (this.disposed) return; this.state = { ...this.state }; this.options.changed?.(this.state); }
  private invalidate(): void {
    this.planBaseline = null;
    this.state.preview = null;
    this.state.observed = null;
    this.state.blocker = null;
    this.state.verified = false;
    this.emit();
  }
  private blockPreview(preview: DryRunApplyResponse, reason: string): never {
    this.planBaseline = null;
    this.state.verified = false;
    this.state.preview = preview;
    this.emit();
    throw new Error(reason);
  }
  private ensureActive(): void {
    if (this.disposed) throw new Error('This browser review was closed. Inspect its original request in Operations; accepted controller work was not canceled.');
  }
  private store(saved: SavedReview): void {
    this.ensureActive();
    const raw = JSON.stringify(saved);
    if (new TextEncoder().encode(raw).byteLength > MAX_REVIEW_BYTES) throw new Error('This review exceeds browser recovery storage limits. Download the manifests. Further submissions are blocked; accepted controller work is not canceled.');
    try { this.storage.setItem(REVIEW_KEY, raw); } catch {
      throw new Error('Cannot save this tab’s recovery record. Further submissions are blocked. Download the manifests and free session storage before looking up the saved request again. Existing controller work may still be running and is not canceled.');
    }
    this.state.saved = saved;
    this.emit();
  }
  save(): void { this.store(this.state.saved); }
  private async checkBinding(): Promise<void> {
    this.ensureActive();
    const binding = await this.api.binding();
    this.ensureActive();
    if (!sameBinding(binding, this.state.saved.binding)) throw new Error('The controller or signed-in user changed. This review is blocked; return to its original controller/user or discard the browser review.');
  }
  private saveOperation(index: number, op: Operation): void {
    const saved = this.state.saved;
    const step = saved.steps[index];
    validateReviewOperation(op, step);
    if (step.attempt!.operationId !== op.id) {
      this.store({ ...saved, steps: saved.steps.map((item, i) => i === index ? { ...item, attempt: { ...item.attempt!, operationId: op.id } } : item) });
    }
  }
  private async lookup(index: number): Promise<Operation | null> {
    const step = this.state.saved.steps[index];
    if (!step.attempt) return null;
    let op: Operation;
    if (step.attempt.operationId) {
      try { op = await this.api.getOperation(step.attempt.operationId); } catch (error) {
        if (!(error instanceof ApiError) || error.status !== 404) throw error;
        op = await this.api.byRequest(step.attempt.requestId);
      }
    } else {
      try { op = await this.api.byRequest(step.attempt.requestId); } catch (error) {
        if (error instanceof ApiError && error.status === 404) return null;
        throw error;
      }
    }
    this.saveOperation(index, op);
    return op;
  }
  private async baseline(resource: Resource): Promise<GetResourceResponse | null> {
    try {
      const result = await this.api.getResource(resource.apiVersion, resource.kind, resource.metadata.name);
      if (!result.resource || !sameTarget(result.resource, resource)) throw new Error('The controller returned a different resource identity. Recovery is blocked.');
      return result;
    } catch (error) {
      if (error instanceof ApiError && error.status === 404) return null;
      throw error;
    }
  }
  private assertBaseline(result: GetResourceResponse | null, expected: Resource): void {
    if (!result || !result.applied || !sameDesired(result.applied, expected)
      || signature(result.resource.metadata.ownerRefs ?? []) !== signature(expected.metadata.ownerRefs ?? [])) {
      throw new Error('The managed baseline is absent, changed, or owned elsewhere. Inspect the resource and recover through its editor; this guided review will not overwrite it.');
    }
  }
  private blockDependency(resource: Resource, code: ReviewBlocker['code'], message: string, observed?: Resource): never {
    this.planBaseline = null;
    this.state.verified = false;
    this.state.preview = null;
    this.state.blocker = {
      target: { apiVersion: resource.apiVersion, kind: resource.kind, name: resource.metadata.name },
      code, message,
      detail: typeof observed?.status?.message === 'string' ? observed.status.message : '',
      checks: applicationChecks(observed?.status),
    };
    this.emit();
    throw new Error(message);
  }
  private async verifyPrevious(): Promise<void> {
    const latest = new Map<string, Resource>();
    for (let i = 0; i < this.state.saved.cursor; i += 1) {
      const resource = this.state.saved.steps[i].resource;
      let op: Operation | null;
      try { op = await this.lookup(i); } catch (error) {
        if (error instanceof UnauthorizedError) throw error;
        this.blockDependency(resource, 'operation_unverified', 'A prerequisite has no verifiable successful operation. Inspect its original request before proceeding; no replacement work was submitted.');
      }
      if (!op || op.status !== 'succeeded') this.blockDependency(resource, 'operation_unverified', 'A prerequisite has no authoritative successful operation. Inspect its original request before proceeding; no replacement work was submitted.');
      latest.set(`${resource.apiVersion}/${resource.kind}/${resource.metadata.name}`, resource);
    }
    if (this.state.operation?.status === 'succeeded') {
      const resource = this.step.resource;
      latest.set(`${resource.apiVersion}/${resource.kind}/${resource.metadata.name}`, resource);
    }
    for (const resource of latest.values()) {
      let result: GetResourceResponse | null;
      try { result = await this.baseline(resource); } catch (error) {
        if (error instanceof UnauthorizedError) throw error;
        this.blockDependency(resource, 'observation_unavailable', 'A prerequisite cannot be observed or its identity could not be verified. Inspect that resource and restore read access before proceeding.');
      }
      const expected = failedReviewOperation(this.state.operation) && sameTarget(resource, this.step.resource)
        && result?.applied && sameDesired(result.applied, this.step.resource) ? this.step.resource : resource;
      try { this.assertBaseline(result, expected); } catch {
        this.blockDependency(resource, 'baseline_changed', 'A prerequisite baseline is missing, changed or owned elsewhere. Inspect it before proceeding; this review will not overwrite or recreate it.');
      }
      if (resource.apiVersion === APP_API && !sameTarget(resource, this.step.resource) && applicationReadiness(result!.resource.status) !== 'ready') {
        this.blockDependency(resource, 'readiness_unproven', 'The previously accepted application has not established current readiness. Inspect its checks and repair it before proceeding or publishing.', result!.resource);
      }
    }
  }
  private async loadCurrent(): Promise<void> {
    // Lookup may persist its newly recovered operation ID and replace state.
    // Resolve first; assigning through a pre-await state object loses that result.
    const operation = await this.lookup(this.state.saved.cursor);
    this.state.operation = operation;
    this.emit();
    if (this.state.operation?.status === 'succeeded') {
      const result = await this.baseline(this.step.resource);
      this.assertBaseline(result, this.step.resource);
      this.state.observed = result!.resource;
    }
    this.emit();
  }
  async resume(): Promise<void> {
    this.invalidate();
    await this.checkBinding();
    await this.loadCurrent();
    await this.verifyPrevious();
    this.state.verified = true;
    this.emit();
  }
  private async reviewBaseline(): Promise<{ result: GetResourceResponse | null; createOnly: boolean }> {
    const result = await this.baseline(this.step.resource);
    const previous = this.state.saved.steps.slice(0, this.state.saved.cursor).reverse().find((step) => sameTarget(step.resource, this.step.resource));
    if (result) {
      const expected = failedReviewOperation(this.state.operation) && result.applied && sameDesired(result.applied, this.step.resource)
        ? this.step.resource : previous?.resource;
      if (!expected) throw new Error('This resource already exists without a matching reviewed baseline. Inspect it before making changes; this guided flow will not overwrite it.');
      this.assertBaseline(result, expected);
    } else if (previous || !this.step.createOnly) {
      throw new Error('The previously managed resource is missing. Inspect it before retrying; this review will not silently recreate it.');
    }
    // A failed first Apply may already own guest files or a VM without a saved
    // manifest. An explicitly reviewed retry must recover that bound resource,
    // not require its physical absence. Unknown submissions retain their flags.
    return { result, createOnly: this.step.attempt && !this.state.operation ? this.step.attempt.createOnly : !result && this.step.createOnly && !failedReviewOperation(this.state.operation) };
  }
  async review(): Promise<void> {
    this.invalidate();
    await this.checkBinding();
    await this.loadCurrent();
    await this.verifyPrevious();
    if (this.state.operation && !failedReviewOperation(this.state.operation)) {
      this.state.verified = true; this.emit(); return;
    }
    const validation = await this.api.validate(this.step.resource);
    if (validation.errors?.length) {
      this.blockPreview({ summary: 'Schema validation failed', validationErrors: validation.errors }, 'The declaration failed schema validation. Inspect the diagnostics and downloadable manifest before reviewing again.');
    }
    const baseline = await this.reviewBaseline();
    const preview = await this.api.preview(this.step.resource);
    if (!preview.operation || preview.validationErrors?.length || preview.fieldErrors?.length || (baseline.createOnly && preview.operation !== 'create')) {
      this.blockPreview(preview, 'The preview is blocked. Inspect its diagnostics, repair the selected resource or declaration, and refresh the review before Apply.');
    }
    this.planBaseline = signature(baseline.result?.applied ? desired(baseline.result.applied) : null);
    this.plannedCreateOnly = baseline.createOnly;
    this.state.preview = preview;
    this.state.verified = true;
    this.emit();
  }
  async submit(consent: ReviewConsent): Promise<void> {
    const preview = this.state.preview;
    if (!this.options.canApply()) throw new Error('The running controller authority or your role does not permit Apply. Observation and downloads remain available.');
    if (!preview || !this.state.verified || this.planBaseline === null) throw new Error('Refresh the preview and confirm this review before submitting.');
    if (!consent.dedicated || !consent.trustedKey || (this.state.saved.steps.some((step) => step.resource.kind === 'TunnelRoute') && !consent.publicExposure)
      || (preview.requiredGates ?? []).some((gate) => !consent.gates[gate])) throw new Error('Confirm the dedicated guest, verified SSH host key, public exposure and every required gate again before Apply.');
    const reviewedBaseline = this.planBaseline;
    const reviewedCreateOnly = this.plannedCreateOnly;
    this.invalidate();
    await this.checkBinding();
    const known = await this.lookup(this.state.saved.cursor);
    this.state.operation = known;
    if (known && !failedReviewOperation(known)) { this.invalidate(); this.emit(); return; }
    await this.verifyPrevious();
    const baseline = await this.reviewBaseline();
    const fresh = await this.api.preview(this.step.resource);
    if (!fresh.operation || fresh.validationErrors?.length || fresh.fieldErrors?.length) {
      this.blockPreview(fresh, 'The fresh preview is blocked. Inspect its diagnostics and refresh the review before any new Apply request.');
    }
    if (signature(baseline.result?.applied ? desired(baseline.result.applied) : null) !== reviewedBaseline
      || baseline.createOnly !== reviewedCreateOnly || signature(fresh) !== signature(preview)) {
      this.blockPreview(fresh, 'The resource baseline or preview changed. Refresh the review and confirm the new changes before applying.');
    }
    const flags = { createOnly: reviewedCreateOnly, allowDestructive: consent.gates.allow_destructive === true, iKnowThisBreaksTheCluster: consent.gates.i_know_this_breaks_the_cluster === true };
    const old = this.step.attempt;
    if (old && !known && (flags.createOnly !== old.createOnly || flags.allowDestructive !== old.allowDestructive || flags.iKnowThisBreaksTheCluster !== old.iKnowThisBreaksTheCluster)) {
      throw new Error('This uncertain request can only be retransmitted with its original exact intent and freshly confirmed permissions. Inspect the saved request; no new request ID can be used.');
    }
    const attempt: Attempt = old && !known ? old : { requestId: this.api.requestId(), ...flags };
    if (!requestKey(attempt.requestId)) throw new Error('A secure Apply request identity could not be created. No Apply was submitted.');
    if (!this.options.canApply()) throw new Error('The controller authority or your role changed during review. No new Apply was submitted.');
    const saved = this.state.saved;
    this.store({ ...saved, steps: saved.steps.map((step, i) => i === saved.cursor ? { ...step, attempt } : step) });
    this.invalidate(); // Authorization and previews never survive a submission.
    this.ensureActive();
    const request: ApplyRequest = { resource: this.step.resource, requestId: attempt.requestId, ...flags };
    let acknowledgementMismatch = false;
    try {
      const accepted = await this.api.apply(request);
      if (!accepted.operationId || accepted.requestId !== attempt.requestId) {
        acknowledgementMismatch = true;
        throw new Error('The controller did not acknowledge the exact saved request. Look it up before retransmitting.');
      }
      this.store({ ...this.state.saved, steps: this.state.saved.steps.map((step, i) => i === this.state.saved.cursor ? { ...step, attempt: { ...attempt, operationId: accepted.operationId } } : step) });
    } catch (error) {
      // Read-only reconciliation is safe even when the HTTP acknowledgement was
      // lost. Never issue a second Apply, and never replace an uncertain key.
      await this.loadCurrent();
      if (!this.state.operation) throw new Error('The submission outcome is unknown. Look up the saved request again. If it is still not found, refresh the preview and explicitly retransmit the SAME request; do not start another deployment.');
      if (acknowledgementMismatch) throw new Error('The controller acknowledgement did not match the saved request. Recovery performed only a read-only lookup; inspect the accepted operation before continuing.');
      return;
    }
    await this.loadCurrent();
  }
  async observe(): Promise<void> {
    this.invalidate();
    await this.checkBinding();
    await this.loadCurrent();
    if (!this.state.operation) throw new Error('No accepted operation was found for the saved request. Do not replace its request ID.');
    if (!terminal(this.state.operation)) {
      this.watcher?.abort();
      const controller = new AbortController();
      this.watcher = controller;
      const root = this.state.operation;
      let terminalHint = false;
      try {
        await this.api.watch({ id: root.id, includeChildren: false }, (event) => {
          if (!isReviewRootEvent(event, root)) return;
          if (['succeeded', 'failed', 'interrupted', 'canceled'].includes(event.operation.status)) {
            terminalHint = true;
            controller.abort();
          }
        }, { signal: controller.signal });
      } catch (error) {
        if (this.disposed) return;
        if (!terminalHint || !controller.signal.aborted) throw error;
      }
      if (this.disposed) return;
      await this.loadCurrent(); // Stream terminal hints are not success evidence.
      if (this.state.operation && !terminal(this.state.operation)) throw new Error('Operation monitoring disconnected. Look up this saved request again; do not submit a duplicate.');
    }
    await this.verifyPrevious();
    this.state.verified = true;
    this.emit();
  }
  async refreshHealth(): Promise<void> { await this.resume(); }
  async next(): Promise<void> {
    await this.resume();
    if (!this.state.verified || this.state.operation?.status !== 'succeeded'
      || (this.step.resource.apiVersion === APP_API && applicationReadiness(this.state.observed?.status) !== 'ready')) throw new Error('Fresh successful operation and readiness evidence are required before proceeding.');
    if (this.state.saved.cursor >= this.state.saved.steps.length - 1) return;
    this.store({ ...this.state.saved, cursor: this.state.saved.cursor + 1 });
    this.state.operation = null;
    this.invalidate();
    await this.review();
  }
  dispose(): void { this.disposed = true; this.watcher?.abort(); }
}
